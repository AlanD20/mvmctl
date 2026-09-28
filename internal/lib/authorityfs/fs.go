package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	directoryMode  = uint32(0700)
	baseMode       = uint32(0711)
	lockMode       = uint32(0600)
	lockPoll       = 10 * time.Millisecond

	resolveBeneath = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	resolveManaged = resolveBeneath | unix.RESOLVE_NO_XDEV
)

type dependencies struct {
	open     func(context.Context, string, int, uint32) (int, error)
	openAt2  func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt  func(context.Context, int, string, uint32) error
	fstat    func(context.Context, int, *unix.Stat_t) error
	statx    func(context.Context, int, int, int, *unix.Statx_t) error
	fchown   func(context.Context, int, int, int) error
	fchmod   func(context.Context, int, uint32) error
	fsync    func(context.Context, int) error
	unlinkAt func(context.Context, int, string, int) error
	close    func(context.Context, int) error
	flock    func(context.Context, int, int) error
	waitLock func(context.Context) error
}

type policy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

type authorityRoot struct {
	authority int
	retained  []int
}

// Jailer pins the fixed Jailer authority directories and creates only Jailer locks and UID capabilities.
type Jailer struct {
	state *jailerState
}

type jailerState struct {
	// mu guards every descriptor and lifecycle field below.
	mu           sync.Mutex
	deps         dependencies
	policy       policy
	authority    int
	releases     int
	retained     []int
	active       bool
	released     bool
	releaseErr   error
	existingOnly bool
}

// JailerUID pins one fixed root-owned Jailer UID directory.
type JailerUID struct {
	state *jailerUIDState
}

type jailerUIDState struct {
	// mu guards fd and every lifecycle field below. ownerUID is immutable after construction.
	mu           sync.Mutex
	deps         dependencies
	policy       policy
	ownerUID     uint32
	fd           int
	active       bool
	released     bool
	releaseErr   error
	existingOnly bool
}

// OpenJailer opens or creates the fixed root-owned Jailer authority tree.
func OpenJailer(ctx context.Context) (*Jailer, error) {
	return openJailerWithPolicy(ctx, realDependencies(), productionPolicy())
}

// OpenExistingJailer pins existing authority and locks without creating or repairing anything.
// Missing runtime authority (including after reboot) fails closed.
func OpenExistingJailer(ctx context.Context) (*Jailer, error) {
	return openJailerMode(ctx, realDependencies(), productionPolicy(), false)
}

func productionPolicy() policy {
	return policy{rootPath: "/", expectedUID: 0, expectedGID: 0}
}

func openJailerWithPolicy(
	ctx context.Context,
	deps dependencies,
	policy policy,
) (_ *Jailer, returnErr error) {
	return openJailerMode(ctx, deps, policy, true)
}

func openJailerMode(ctx context.Context, deps dependencies, policy policy, create bool) (_ *Jailer, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: open Jailer authority: %w", err)
	}
	root, err := openAuthorityRootMode(ctx, deps, policy, create)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open Jailer authority: %w", err)
	}
	retained := root.retained
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("authorityfs: close rejected Jailer authority: %w", err))
		}
	}()

	releasesFD, err := openManagedDirectoryMode(
		ctx,
		deps,
		root.authority,
		"releases",
		directoryMode,
		resolveManaged,
		policy,
		create,
	)
	if err != nil {
		return nil, err
	}
	retained = append(retained, releasesFD)

	return &Jailer{state: &jailerState{
		deps: deps, policy: policy, authority: root.authority, releases: releasesFD, retained: retained, active: true,
		existingOnly: !create,
	}}, nil
}

func openAuthorityRoot(
	ctx context.Context,
	deps dependencies,
	policy policy,
) (_ authorityRoot, returnErr error) {
	return openAuthorityRootMode(ctx, deps, policy, true)
}

func openAuthorityRootMode(
	ctx context.Context,
	deps dependencies,
	policy policy,
	create bool,
) (_ authorityRoot, returnErr error) {
	if err := ctx.Err(); err != nil {
		return authorityRoot{}, fmt.Errorf("authorityfs: open authority root: %w", err)
	}
	retained := make([]int, 0, 4)
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("authorityfs: close rejected authority root: %w", err))
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, directoryFlags, 0)
	if err != nil {
		return authorityRoot{}, fmt.Errorf("authorityfs: open filesystem root: %w", err)
	}
	retained = append(retained, rootFD)
	if err := verifyDirectory(ctx, deps, rootFD, "filesystem root", policy, 0, false); err != nil {
		return authorityRoot{}, err
	}
	runFD, err := openExistingDirectory(ctx, deps, rootFD, "run", resolveBeneath, policy)
	if err != nil {
		return authorityRoot{}, err
	}
	retained = append(retained, runFD)
	mvmctlFD, err := openManagedDirectoryMode(ctx, deps, runFD, "mvmctl", baseMode, resolveManaged, policy, create)
	if err != nil {
		return authorityRoot{}, err
	}
	retained = append(retained, mvmctlFD)
	authorityFD, err := openManagedDirectoryMode(
		ctx,
		deps,
		mvmctlFD,
		"authority",
		directoryMode,
		resolveManaged,
		policy,
		create,
	)
	if err != nil {
		return authorityRoot{}, err
	}
	retained = append(retained, authorityFD)
	return authorityRoot{authority: authorityFD, retained: retained}, nil
}

// OpenUID opens or creates the fixed root-owned directory for uid.
func (jailer *Jailer) OpenUID(ctx context.Context, uid uint32) (*JailerUID, error) {
	if uid == 0 {
		return nil, fmt.Errorf("authorityfs: Jailer UID must be non-zero")
	}
	if jailer == nil || jailer.state == nil {
		return nil, fmt.Errorf("authorityfs: Jailer authority is not active")
	}
	state := jailer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: Jailer authority is released")
	}
	name := formatUID(uid)
	fd, err := openManagedDirectoryMode(
		ctx,
		state.deps,
		state.authority,
		name,
		directoryMode,
		resolveManaged,
		state.policy,
		!state.existingOnly,
	)
	if err != nil {
		return nil, err
	}
	return &JailerUID{state: &jailerUIDState{
		deps: state.deps, policy: state.policy, ownerUID: uid, fd: fd, active: true,
		existingOnly: state.existingOnly,
	}}, nil
}

// Release closes the descriptors owned by the Jailer capability. Children and acquired locks remain valid.
func (jailer *Jailer) Release(ctx context.Context) error {
	if jailer == nil || jailer.state == nil {
		return nil
	}
	state := jailer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	err := closeFDs(context.WithoutCancel(ctx), state.deps, state.retained)
	state.retained = nil
	state.authority = -1
	state.releases = -1
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release Jailer authority: %w", err)
	}
	return state.releaseErr
}

// Release closes the descriptor owned by the Jailer UID capability. Acquired locks remain valid.
func (uid *JailerUID) Release(ctx context.Context) error {
	if uid == nil || uid.state == nil {
		return nil
	}
	state := uid.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	err := state.deps.close(context.WithoutCancel(ctx), state.fd)
	state.fd = -1
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release Jailer UID authority: %w", err)
	}
	return state.releaseErr
}

func openExistingDirectory(
	ctx context.Context,
	deps dependencies,
	parentFD int,
	name string,
	resolve uint64,
	policy policy,
) (int, error) {
	fd, err := deps.openAt2(ctx, parentFD, name, &unix.OpenHow{Flags: directoryFlags, Resolve: resolve})
	if err != nil {
		return -1, fmt.Errorf("authorityfs: open fixed directory %s: %w", name, err)
	}
	if err := verifyDirectory(ctx, deps, fd, name, policy, 0, false); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, err)
	}
	return fd, nil
}

func openManagedDirectory(
	ctx context.Context,
	deps dependencies,
	parentFD int,
	name string,
	mode uint32,
	resolve uint64,
	policy policy,
) (int, error) {
	return openManagedDirectoryMode(ctx, deps, parentFD, name, mode, resolve, policy, true)
}

func openManagedDirectoryMode(
	ctx context.Context,
	deps dependencies,
	parentFD int,
	name string,
	mode uint32,
	resolve uint64,
	policy policy,
	create bool,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, fmt.Errorf("authorityfs: open managed directory %s: %w", name, err)
	}
	how := &unix.OpenHow{Flags: directoryFlags, Resolve: resolve}
	fd, err := deps.openAt2(ctx, parentFD, name, how)
	if err == nil {
		if err := verifyDirectory(ctx, deps, fd, name, policy, mode, true); err != nil {
			return -1, closeRejectedFD(ctx, deps, fd, err)
		}
		return fd, nil
	}
	if !create || !errors.Is(err, unix.ENOENT) {
		return -1, fmt.Errorf("authorityfs: open managed directory %s: %w", name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, mode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, fmt.Errorf("authorityfs: create managed directory %s: %w", name, mkdirErr)
	}
	fd, err = deps.openAt2(ctx, parentFD, name, how)
	if err != nil {
		return -1, fmt.Errorf("authorityfs: open created managed directory %s: %w", name, err)
	}
	if !created {
		if err := verifyDirectory(ctx, deps, fd, name, policy, mode, true); err != nil {
			return -1, closeRejectedFD(ctx, deps, fd, err)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, fmt.Errorf("authorityfs: set owner on %s: %w", name, err))
	}
	if err := deps.fchmod(ctx, fd, mode); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, fmt.Errorf("authorityfs: set mode on %s: %w", name, err))
	}
	if err := verifyDirectory(ctx, deps, fd, name, policy, mode, true); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, fmt.Errorf("authorityfs: sync created directory %s: %w", name, err))
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedFD(ctx, deps, fd, fmt.Errorf("authorityfs: sync parent of %s: %w", name, err))
	}
	return fd, nil
}

func verifyDirectory(
	ctx context.Context,
	deps dependencies,
	fd int,
	name string,
	policy policy,
	mode uint32,
	exact bool,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return fmt.Errorf("authorityfs: inspect directory %s: %w", name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("authorityfs: %s is not a directory", name)
	}
	if stat.Uid != policy.expectedUID || stat.Mode&0022 != 0 {
		return fmt.Errorf("authorityfs: directory %s has unsafe ownership or mode", name)
	}
	if exact && (stat.Gid != policy.expectedGID || stat.Mode&07777 != mode) {
		return fmt.Errorf("authorityfs: managed directory %s has unexpected owner or mode", name)
	}
	return nil
}

func closeRejectedFD(ctx context.Context, deps dependencies, fd int, primary error) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return errors.Join(primary, fmt.Errorf("authorityfs: close rejected descriptor: %w", err))
	}
	return primary
}

func closeFDs(ctx context.Context, deps dependencies, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(context.WithoutCancel(ctx), fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func formatUID(uid uint32) string {
	return fmt.Sprintf("%d", uid)
}

func waitForLock(ctx context.Context) error {
	timer := time.NewTimer(lockPoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func realDependencies() dependencies {
	return dependencies{
		open: func(_ context.Context, name string, flags int, mode uint32) (int, error) {
			return unix.Open(name, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		close:    func(_ context.Context, fd int) error { return unix.Close(fd) },
		flock:    func(_ context.Context, fd, how int) error { return unix.Flock(fd, how) },
		waitLock: waitForLock,
	}
}
