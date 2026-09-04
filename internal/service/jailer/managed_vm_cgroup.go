package jailer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

const (
	managedVMCgroupDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedVMCgroupResolveFlags   = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV
	managedVMCgroupRootMode   = uint32(0o755)
	managedVMCgroupParentMode = uint32(0o755)
	managedVMCgroupLeafMode   = uint32(0o755)
	managedVMCgroupProcsMode  = uint32(0o644)
	managedVMCgroupMaxProcs   = 64 * 1024
	managedVMCgroupStatxMask  = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
)

type managedVMCgroupDeps struct {
	openRoot func(context.Context) (int, error)
	openAt2  func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt  func(context.Context, int, string, uint32) error
	fchown   func(context.Context, int, int, int) error
	fchmod   func(context.Context, int, uint32) error
	statx    func(context.Context, int, *unix.Statx_t) error
	statfs   func(context.Context, int, *unix.Statfs_t) error
	pread    func(context.Context, int, []byte, int64) (int, error)
	unlinkAt func(context.Context, int, string, int) error
	close    func(context.Context, int) error

	trustedUID uint32
	trustedGID uint32
}

type managedVMCgroupMountIdentity struct {
	kind uint8
	id   uint64
}

type managedVMCgroupIdentity struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mount       managedVMCgroupMountIdentity
}

type preparedManagedVMCgroup struct {
	state *preparedManagedVMCgroupState
}

type preparedManagedVMCgroupState struct {
	mu sync.Mutex

	deps       managedVMCgroupDeps
	vmID       string
	rootFD     int
	parentFD   int
	root       managedVMCgroupIdentity
	parent     managedVMCgroupIdentity
	retained   []int
	leafAbsent bool
	binding    bool
	released   bool
	releaseErr error
}

type preparedManagedVMCgroupSnapshot struct {
	leafAbsent bool
	released   bool
}

// retainedCgroupProcessVerifier bridges to process authority that already retains the pidfd. Its implementation must
// prove the locator both before and after this component reads membership; a numeric PID alone cannot satisfy it.
type retainedCgroupProcessVerifier interface {
	verifyBeforeCgroupMembership(context.Context, instanceProcessLocator, processObservation) error
	verifyAfterCgroupMembership(context.Context, instanceProcessLocator, processObservation) error
}

type managedVMCgroupLease struct {
	state *managedVMCgroupLeaseState
}

type managedVMCgroupLeaseState struct {
	mu sync.Mutex

	deps       managedVMCgroupDeps
	vmID       string
	rootFD     int
	parentFD   int
	leafFD     int
	parent     managedVMCgroupIdentity
	leaf       managedVMCgroupIdentity
	retained   []int
	removed    bool
	released   bool
	releaseErr error
}

type managedVMCgroupLeaseSnapshot struct {
	removed  bool
	released bool
}

func prepareManagedVMCgroup(
	ctx context.Context,
	deps managedVMCgroupDeps,
	vmID string,
) (_ *preparedManagedVMCgroup, returnErr error) {
	if ctx == nil {
		return nil, instanceAtomicError("prepare managed VM cgroup: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("prepare managed VM cgroup", err)
	}
	if err := validateManagedVMCgroupDeps(deps); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("managed VM cgroup VM ID is invalid")
	}

	retained := make([]int, 0, 2)
	defer func() {
		if retained == nil {
			return
		}
		returnErr = appendInstanceOperationError(
			returnErr,
			"close rejected managed VM cgroup descriptors",
			closeManagedVMCgroupFDs(context.WithoutCancel(ctx), deps, retained),
		)
	}()

	rootFD, err := deps.openRoot(ctx)
	if err != nil {
		return nil, instanceAtomicError("open fixed cgroup-v2 root", err)
	}
	retained = append(retained, rootFD)
	root, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		rootFD,
		"cgroup-v2 root",
		managedVMCgroupRootMode,
	)
	if err != nil {
		return nil, err
	}
	var filesystem unix.Statfs_t
	if err := deps.statfs(ctx, rootFD, &filesystem); err != nil {
		return nil, instanceAtomicError("inspect cgroup-v2 root filesystem", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, instanceAtomicError("fixed cgroup root is not a cgroup-v2 mount", nil)
	}

	parentFD, parentCreated, err := openOrCreateManagedVMCgroupParent(ctx, deps, rootFD)
	if err != nil {
		return nil, err
	}
	retained = append(retained, parentFD)
	if parentCreated {
		if err := deps.fchown(ctx, parentFD, int(deps.trustedUID), int(deps.trustedGID)); err != nil {
			return nil, instanceAtomicError("set fresh managed cgroup parent ownership", err)
		}
		if err := deps.fchmod(ctx, parentFD, managedVMCgroupParentMode); err != nil {
			return nil, instanceAtomicError("set fresh managed cgroup parent mode", err)
		}
	}
	parent, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		parentFD,
		"managed cgroup parent",
		managedVMCgroupParentMode,
	)
	if err != nil {
		return nil, err
	}
	if parent.mount != root.mount || parent.deviceMajor != root.deviceMajor || parent.deviceMinor != root.deviceMinor {
		return nil, instanceAtomicError("managed cgroup parent is outside the pinned cgroup-v2 mount", nil)
	}

	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	leafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if err == nil {
		closeErr := deps.close(context.WithoutCancel(ctx), leafFD)
		primary := instanceStateError(vmID, "managed VM cgroup already exists")
		return nil, appendInstanceOperationError(primary, "close preexisting managed VM cgroup", closeErr)
	}
	if !errors.Is(err, unix.ENOENT) {
		return nil, instanceAtomicError("prove managed VM cgroup leaf absent", err)
	}

	prepared := &preparedManagedVMCgroup{state: &preparedManagedVMCgroupState{
		deps:       deps,
		vmID:       vmID,
		rootFD:     rootFD,
		parentFD:   parentFD,
		root:       root,
		parent:     parent,
		retained:   retained,
		leafAbsent: true,
	}}
	retained = nil
	return prepared, nil
}

func openOrCreateManagedVMCgroupParent(
	ctx context.Context,
	deps managedVMCgroupDeps,
	rootFD int,
) (int, bool, error) {
	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	fd, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if err == nil {
		return fd, false, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, false, instanceAtomicError("open fixed managed cgroup parent", err)
	}
	created := true
	if err := deps.mkdirAt(ctx, rootFD, infra.JailerCgroupParent, managedVMCgroupParentMode); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, false, instanceAtomicError("create fixed managed cgroup parent", err)
		}
		created = false
	}
	fd, err = deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if err != nil {
		return -1, false, instanceAtomicError("open created managed cgroup parent", err)
	}
	return fd, created, nil
}

// Bind pins the exact Jailer-created leaf after preparation proved its fixed name absent. After safe leaf admission,
// ownership transfers to the returned lease and the prepared value becomes inert. Every subsequent verification
// failure returns that non-nil lease with the error so exact cleanup authority cannot be lost. Membership is read only
// between two checks performed by retained process authority.
func (prepared *preparedManagedVMCgroup) Bind(
	ctx context.Context,
	locator instanceProcessLocator,
	observation processObservation,
	verifier retainedCgroupProcessVerifier,
) (*managedVMCgroupLease, error) {
	if prepared == nil || prepared.state == nil {
		return nil, instanceStateError("", "prepared managed VM cgroup is not active")
	}
	state := prepared.state
	state.mu.Lock()
	if ctx == nil {
		state.mu.Unlock()
		return nil, instanceAtomicError("bind managed VM cgroup: context is required", nil)
	}
	if state.released || state.binding || !state.leafAbsent || state.rootFD < 0 || state.parentFD < 0 {
		state.mu.Unlock()
		return nil, instanceStateError(state.vmID, "prepared managed VM cgroup is not active")
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, instanceAtomicError("bind managed VM cgroup", err)
	}
	if verifier == nil {
		state.mu.Unlock()
		return nil, instanceAtomicError("retained cgroup process verifier is required", nil)
	}
	state.binding = true
	deps := state.deps
	vmID := state.vmID
	rootFD := state.rootFD
	parentFD := state.parentFD
	parent := state.parent
	retained := append([]int{}, state.retained...)
	state.mu.Unlock()

	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	leafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if err != nil {
		finishRejectedManagedVMCgroupBind(state)
		return nil, instanceAtomicError("open Jailer-created managed VM cgroup", err)
	}
	leaf, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		leafFD,
		"Jailer-created managed VM cgroup",
		managedVMCgroupLeafMode,
	)
	if err != nil {
		closeErr := closeRejectedManagedVMCgroupFD(ctx, deps, leafFD, err)
		finishRejectedManagedVMCgroupBind(state)
		return nil, closeErr
	}
	if leaf.mount != parent.mount || leaf.deviceMajor != parent.deviceMajor || leaf.deviceMinor != parent.deviceMinor {
		closeErr := closeRejectedManagedVMCgroupFD(
			ctx,
			deps,
			leafFD,
			instanceAtomicError("Jailer-created managed VM cgroup is outside the pinned cgroup-v2 mount", nil),
		)
		finishRejectedManagedVMCgroupBind(state)
		return nil, closeErr
	}

	retained = append(retained, leafFD)
	lease := &managedVMCgroupLease{state: &managedVMCgroupLeaseState{
		deps: deps, vmID: vmID, rootFD: rootFD, parentFD: parentFD, leafFD: leafFD,
		parent: parent, leaf: leaf, retained: retained,
	}}
	state.mu.Lock()
	state.rootFD = -1
	state.parentFD = -1
	state.retained = nil
	state.leafAbsent = false
	state.binding = false
	state.released = true
	state.mu.Unlock()

	if locator.pid <= 0 || locator.startTimeTicks == 0 || observation.startTimeTicks != locator.startTimeTicks {
		return lease, processVerificationError(vmID, "cgroup process observation does not match its locator", nil)
	}
	wantCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + vmID
	if observation.cgroupPath != wantCgroup {
		return lease, processVerificationError(
			vmID,
			"process observation does not match the derived VM cgroup",
			nil,
		)
	}
	if err := verifier.verifyBeforeCgroupMembership(ctx, locator, observation); err != nil {
		return lease, processVerificationError(vmID, "verify retained process before cgroup membership", err)
	}
	members, err := readManagedVMCgroupMembership(ctx, lease.state)
	if err != nil {
		return lease, err
	}
	if len(members) != 1 || members[0] != locator.pid {
		return lease, processVerificationError(
			vmID,
			"managed VM cgroup membership is not the retained process",
			nil,
		)
	}
	if err := verifier.verifyAfterCgroupMembership(ctx, locator, observation); err != nil {
		return lease, processVerificationError(vmID, "verify retained process after cgroup membership", err)
	}
	return lease, nil
}

func finishRejectedManagedVMCgroupBind(state *preparedManagedVMCgroupState) {
	state.mu.Lock()
	state.binding = false
	state.mu.Unlock()
}

func readManagedVMCgroupMembership(
	ctx context.Context,
	state *managedVMCgroupLeaseState,
) (_ []int, returnErr error) {
	how := unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	fd, err := state.deps.openAt2(ctx, state.leafFD, "cgroup.procs", &how)
	if err != nil {
		return nil, processVerificationError(state.vmID, "open pinned cgroup membership", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close pinned cgroup membership",
			state.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	identity, err := inspectManagedVMCgroupFile(ctx, state.deps, fd, "cgroup.procs")
	if err != nil {
		return nil, err
	}
	if identity.mount != state.leaf.mount || identity.deviceMajor != state.leaf.deviceMajor ||
		identity.deviceMinor != state.leaf.deviceMinor {
		return nil, processVerificationError(state.vmID, "cgroup membership is outside the pinned cgroup-v2 mount", nil)
	}
	buffer, err := readAllManagedVMCgroupMembership(ctx, state, fd)
	if err != nil {
		return nil, err
	}
	members, err := parseManagedVMCgroupMembership(buffer)
	if err != nil {
		return nil, processVerificationError(state.vmID, "parse pinned cgroup membership", err)
	}
	return members, nil
}

func readAllManagedVMCgroupMembership(
	ctx context.Context,
	state *managedVMCgroupLeaseState,
	fd int,
) ([]byte, error) {
	const chunkBytes = 4096
	raw := make([]byte, 0, chunkBytes)
	var offset int64
	for {
		remaining := managedVMCgroupMaxProcs + 1 - len(raw)
		readSize := min(chunkBytes, remaining)
		chunk := make([]byte, readSize)
		count, err := state.deps.pread(ctx, fd, chunk, offset)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, processVerificationError(state.vmID, "read pinned cgroup membership", err)
		}
		if count < 0 || count > len(chunk) {
			return nil, processVerificationError(state.vmID, "pinned cgroup membership read count is invalid", nil)
		}
		if count == 0 {
			return raw, nil
		}
		raw = append(raw, chunk[:count]...)
		if len(raw) > managedVMCgroupMaxProcs {
			return nil, processVerificationError(state.vmID, "pinned cgroup membership exceeds its bound", nil)
		}
		offset += int64(count)
	}
}

func inspectManagedVMCgroupFile(
	ctx context.Context,
	deps managedVMCgroupDeps,
	fd int,
	description string,
) (managedVMCgroupIdentity, error) {
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, &stat); err != nil {
		return managedVMCgroupIdentity{}, instanceAtomicError("inspect "+description, err)
	}
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO)
	if stat.Mask&required != required || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint32(stat.Mode)&0o7777 != managedVMCgroupProcsMode || stat.Uid != deps.trustedUID ||
		stat.Gid != deps.trustedGID || stat.Ino == 0 {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" metadata is not exact", nil)
	}
	mount, err := managedVMCgroupMountFromStatx(stat)
	if err != nil {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" mount identity is unavailable", err)
	}
	return managedVMCgroupIdentity{
		deviceMajor: stat.Dev_major, deviceMinor: stat.Dev_minor, inode: stat.Ino, mount: mount,
	}, nil
}

func parseManagedVMCgroupMembership(raw []byte) ([]int, error) {
	if len(raw) == 0 {
		return []int{}, nil
	}
	if raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("cgroup membership is not newline terminated")
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	members := make([]int, 0, len(lines))
	for _, line := range lines {
		if line == "" || (len(line) > 1 && line[0] == '0') {
			return nil, fmt.Errorf("cgroup membership is not canonical")
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("cgroup membership contains an invalid process ID")
		}
		members = append(members, pid)
	}
	return members, nil
}

// Remove consumes cleanup independently of caller cancellation and removes only the retained exact empty leaf.
func (lease *managedVMCgroupLease) Remove(ctx context.Context) error {
	if ctx == nil {
		return instanceAtomicError("remove managed VM cgroup: context is required", nil)
	}
	if lease == nil || lease.state == nil {
		return instanceStateError("", "managed VM cgroup lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return instanceStateError(state.vmID, "managed VM cgroup lease is released")
	}
	if state.removed {
		return nil
	}
	if state.rootFD < 0 || state.parentFD < 0 || state.leafFD < 0 || len(state.retained) != 3 {
		return instanceStateError(state.vmID, "managed VM cgroup cleanup authority is incomplete")
	}
	cleanupCtx := context.WithoutCancel(ctx)
	retained, err := inspectManagedVMCgroupDirectory(
		cleanupCtx,
		state.deps,
		state.leafFD,
		"retained managed VM cgroup",
		managedVMCgroupLeafMode,
	)
	if err != nil {
		return err
	}
	if retained != state.leaf {
		return instanceAtomicError("retained managed VM cgroup identity changed", nil)
	}

	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	currentFD, err := state.deps.openAt2(cleanupCtx, state.parentFD, state.vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		removed, reconcileErr := retainedManagedVMCgroupWasUnlinked(cleanupCtx, state)
		if reconcileErr != nil {
			return appendInstanceOperationError(
				instanceAtomicError("reopen managed VM cgroup for removal", err),
				"reconcile absent managed VM cgroup",
				reconcileErr,
			)
		}
		if !removed {
			return instanceAtomicError("managed VM cgroup disappeared without retained unlink proof", nil)
		}
		state.removed = true
		return nil
	}
	if err != nil {
		return instanceAtomicError("reopen managed VM cgroup for removal", err)
	}
	current, inspectErr := inspectManagedVMCgroupDirectory(
		cleanupCtx,
		state.deps,
		currentFD,
		"reopened managed VM cgroup",
		managedVMCgroupLeafMode,
	)
	closeErr := state.deps.close(cleanupCtx, currentFD)
	if inspectErr != nil {
		return appendInstanceOperationError(inspectErr, "close rejected reopened managed VM cgroup", closeErr)
	}
	if closeErr != nil {
		return instanceAtomicError("close reopened managed VM cgroup", closeErr)
	}
	if current != state.leaf {
		return instanceAtomicError("managed VM cgroup entry identity changed", nil)
	}
	members, err := readManagedVMCgroupMembership(cleanupCtx, state)
	if err != nil {
		return err
	}
	if len(members) != 0 {
		return instanceAtomicError("refusing to remove a nonempty managed VM cgroup", nil)
	}
	retained, err = inspectManagedVMCgroupDirectory(
		cleanupCtx,
		state.deps,
		state.leafFD,
		"retained managed VM cgroup before removal",
		managedVMCgroupLeafMode,
	)
	if err != nil {
		return err
	}
	if retained != state.leaf {
		return instanceAtomicError("retained managed VM cgroup identity changed before removal", nil)
	}
	if err := state.deps.unlinkAt(cleanupCtx, state.parentFD, state.vmID, unix.AT_REMOVEDIR); err != nil {
		primary := instanceAtomicError("remove exact managed VM cgroup", err)
		removed, reconcileErr := reconcileManagedVMCgroupRemoval(cleanupCtx, state)
		if removed {
			state.removed = true
		}
		return appendInstanceOperationError(primary, "reconcile managed VM cgroup removal", reconcileErr)
	}
	state.removed = true
	return nil
}

func reconcileManagedVMCgroupRemoval(ctx context.Context, state *managedVMCgroupLeaseState) (bool, error) {
	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	currentFD, err := state.deps.openAt2(ctx, state.parentFD, state.vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		return retainedManagedVMCgroupWasUnlinked(ctx, state)
	}
	if err != nil {
		return false, instanceAtomicError("reopen managed VM cgroup after removal error", err)
	}
	current, inspectErr := inspectManagedVMCgroupDirectory(
		ctx,
		state.deps,
		currentFD,
		"reconciled managed VM cgroup",
		managedVMCgroupLeafMode,
	)
	closeErr := state.deps.close(ctx, currentFD)
	if inspectErr != nil {
		return false, appendInstanceOperationError(inspectErr, "close reconciled managed VM cgroup", closeErr)
	}
	if closeErr != nil {
		return false, instanceAtomicError("close reconciled managed VM cgroup", closeErr)
	}
	if current == state.leaf {
		return false, nil
	}
	removed, err := retainedManagedVMCgroupWasUnlinked(ctx, state)
	if err != nil {
		return false, err
	}
	if !removed {
		return false, instanceAtomicError("managed VM cgroup replacement raced removal", nil)
	}
	return true, instanceAtomicError("managed VM cgroup was replaced after the retained leaf was removed", nil)
}

func retainedManagedVMCgroupWasUnlinked(ctx context.Context, state *managedVMCgroupLeaseState) (bool, error) {
	var stat unix.Statx_t
	if err := state.deps.statx(ctx, state.leafFD, &stat); err != nil {
		return false, instanceAtomicError("inspect retained managed VM cgroup link state", err)
	}
	if stat.Mask&unix.STATX_NLINK == 0 {
		return false, instanceAtomicError("retained managed VM cgroup link count is unavailable", nil)
	}
	identity, err := managedVMCgroupIdentityFromDirectoryStatx(
		state.deps,
		stat,
		"retained managed VM cgroup link state",
		managedVMCgroupLeafMode,
	)
	if err != nil {
		return false, err
	}
	if identity != state.leaf {
		return false, instanceAtomicError("retained managed VM cgroup identity changed after removal error", nil)
	}
	return stat.Nlink == 0, nil
}

func (lease *managedVMCgroupLease) snapshot() managedVMCgroupLeaseSnapshot {
	if lease == nil || lease.state == nil {
		return managedVMCgroupLeaseSnapshot{}
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return managedVMCgroupLeaseSnapshot{removed: state.removed, released: state.released}
}

// Release closes retained descriptors exactly once after removal. Copies share and cache one first result.
func (lease *managedVMCgroupLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.removed {
		return instanceStateError(state.vmID, "managed VM cgroup must be removed before release")
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseErr = closeManagedVMCgroupFDs(cleanupCtx, state.deps, state.retained)
	if state.releaseErr != nil {
		state.releaseErr = instanceAtomicError("release managed VM cgroup descriptors", state.releaseErr)
	}
	state.rootFD = -1
	state.parentFD = -1
	state.leafFD = -1
	state.retained = nil
	state.released = true
	return state.releaseErr
}

func closeRejectedManagedVMCgroupFD(
	ctx context.Context,
	deps managedVMCgroupDeps,
	fd int,
	primary error,
) error {
	return appendInstanceOperationError(
		primary,
		"close rejected managed VM cgroup descriptor",
		deps.close(context.WithoutCancel(ctx), fd),
	)
}

func inspectManagedVMCgroupDirectory(
	ctx context.Context,
	deps managedVMCgroupDeps,
	fd int,
	description string,
	wantMode uint32,
) (managedVMCgroupIdentity, error) {
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, &stat); err != nil {
		return managedVMCgroupIdentity{}, instanceAtomicError("inspect "+description, err)
	}
	return managedVMCgroupIdentityFromDirectoryStatx(deps, stat, description, wantMode)
}

func managedVMCgroupIdentityFromDirectoryStatx(
	deps managedVMCgroupDeps,
	stat unix.Statx_t,
	description string,
	wantMode uint32,
) (managedVMCgroupIdentity, error) {
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO)
	if stat.Mask&required != required {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" metadata is incomplete", nil)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" is not a directory", nil)
	}
	if uint32(stat.Mode)&0o7777 != wantMode {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" mode is not exact", nil)
	}
	if stat.Uid != deps.trustedUID || stat.Gid != deps.trustedGID {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" is not root-owned", nil)
	}
	mount, err := managedVMCgroupMountFromStatx(stat)
	if err != nil {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" mount identity is unavailable", err)
	}
	if stat.Ino == 0 {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" inode identity is invalid", nil)
	}
	return managedVMCgroupIdentity{
		deviceMajor: stat.Dev_major,
		deviceMinor: stat.Dev_minor,
		inode:       stat.Ino,
		mount:       mount,
	}, nil
}

func managedVMCgroupMountFromStatx(stat unix.Statx_t) (managedVMCgroupMountIdentity, error) {
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0 && stat.Mnt_id != 0:
		return managedVMCgroupMountIdentity{kind: 2, id: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0 && stat.Mnt_id != 0:
		return managedVMCgroupMountIdentity{kind: 1, id: stat.Mnt_id}, nil
	default:
		return managedVMCgroupMountIdentity{}, fmt.Errorf("statx did not return a nonzero mount ID")
	}
}

func validateManagedVMCgroupDeps(deps managedVMCgroupDeps) error {
	if deps.openRoot == nil || deps.openAt2 == nil || deps.mkdirAt == nil || deps.fchown == nil ||
		deps.fchmod == nil || deps.statx == nil || deps.statfs == nil || deps.pread == nil || deps.unlinkAt == nil ||
		deps.close == nil {
		return instanceAtomicError("managed VM cgroup dependencies are incomplete", nil)
	}
	return nil
}

func (prepared *preparedManagedVMCgroup) snapshot() preparedManagedVMCgroupSnapshot {
	if prepared == nil || prepared.state == nil {
		return preparedManagedVMCgroupSnapshot{}
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return preparedManagedVMCgroupSnapshot{leafAbsent: state.leafAbsent, released: state.released}
}

func (prepared *preparedManagedVMCgroup) Release(ctx context.Context) error {
	if prepared == nil || prepared.state == nil {
		return nil
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.binding {
		return instanceStateError(state.vmID, "prepared managed VM cgroup bind is in progress")
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseErr = closeManagedVMCgroupFDs(cleanupCtx, state.deps, state.retained)
	if state.releaseErr != nil {
		state.releaseErr = instanceAtomicError("release prepared managed VM cgroup descriptors", state.releaseErr)
	}
	state.rootFD = -1
	state.parentFD = -1
	state.retained = nil
	state.released = true
	return state.releaseErr
}

func closeManagedVMCgroupFDs(ctx context.Context, deps managedVMCgroupDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(ctx, fds[index]); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func realManagedVMCgroupDeps() managedVMCgroupDeps {
	return managedVMCgroupDeps{
		openRoot: func(_ context.Context) (int, error) {
			return unix.Open(infra.CgroupV2Root, managedVMCgroupDirectoryFlags, 0)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		fchown: func(_ context.Context, fd int, uid int, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		statx: func(_ context.Context, fd int, stat *unix.Statx_t) error {
			return unix.Statx(
				fd,
				"",
				unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_FORCE_SYNC,
				managedVMCgroupStatxMask,
				stat,
			)
		},
		statfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error { return unix.Fstatfs(fd, stat) },
		pread: func(_ context.Context, fd int, buffer []byte, offset int64) (int, error) {
			return unix.Pread(fd, buffer, offset)
		},
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		close:      func(_ context.Context, fd int) error { return unix.Close(fd) },
		trustedUID: 0,
		trustedGID: 0,
	}
}
