package jailer

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/unix"
)

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

	return lease, lease.VerifyMembership(ctx, locator, observation, verifier)
}

// BindForCleanup pins a leaf created after this preparation proved its fixed name absent, without proving process
// membership. The caller must hold the VM lifecycle lock continuously since preparation and must have terminated and
// reaped every retained launch task before removing the returned leaf. An absent leaf leaves preparation active and
// returns nil, false, nil. Safe admission consumes preparation and returns the exact cleanup lease with present true.
func (prepared *preparedManagedVMCgroup) BindForCleanup(
	ctx context.Context,
) (*managedVMCgroupLease, bool, error) {
	if prepared == nil || prepared.state == nil {
		return nil, false, instanceStateError("", "prepared managed VM cgroup is not active")
	}
	state := prepared.state
	state.mu.Lock()
	if ctx == nil {
		state.mu.Unlock()
		return nil, false, instanceAtomicError("bind managed VM cgroup for cleanup: context is required", nil)
	}
	if state.released || state.binding || !state.leafAbsent || state.rootFD < 0 || state.parentFD < 0 {
		state.mu.Unlock()
		return nil, false, instanceStateError(state.vmID, "prepared managed VM cgroup is not active")
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, false, instanceAtomicError("bind managed VM cgroup for cleanup", err)
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
	if errors.Is(err, unix.ENOENT) {
		finishRejectedManagedVMCgroupBind(state)
		return nil, false, nil
	}
	if err != nil {
		finishRejectedManagedVMCgroupBind(state)
		return nil, false, instanceAtomicError("open Jailer-created managed VM cgroup for cleanup", err)
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
		return nil, false, closeErr
	}
	if leaf.mount != parent.mount || leaf.deviceMajor != parent.deviceMajor || leaf.deviceMinor != parent.deviceMinor {
		closeErr := closeRejectedManagedVMCgroupFD(
			ctx,
			deps,
			leafFD,
			instanceAtomicError("Jailer-created managed VM cgroup is outside the pinned cgroup-v2 mount", nil),
		)
		finishRejectedManagedVMCgroupBind(state)
		return nil, false, closeErr
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
	return lease, true, nil
}

func finishRejectedManagedVMCgroupBind(state *preparedManagedVMCgroupState) {
	state.mu.Lock()
	state.binding = false
	state.mu.Unlock()
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
