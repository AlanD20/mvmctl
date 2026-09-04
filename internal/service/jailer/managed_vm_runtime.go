package jailer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	managedVMRuntimeDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedVMRuntimeResolveFixed   = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS
	managedVMRuntimeResolveManaged = managedVMRuntimeResolveFixed | unix.RESOLVE_NO_XDEV
	managedVMRuntimeParentMode     = uint32(0o711)
	managedVMRuntimeVMMode         = uint32(0o700)
)

type managedVMRuntimePolicy struct {
	rootPath   string
	trustedUID uint32
	trustedGID uint32
}

type managedVMRuntimeDeps struct {
	open     func(context.Context, string, int, uint32) (int, error)
	openAt2  func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt  func(context.Context, int, string, uint32) error
	fstat    func(context.Context, int, *unix.Stat_t) error
	fchown   func(context.Context, int, int, int) error
	fchmod   func(context.Context, int, uint32) error
	fsync    func(context.Context, int) error
	unlinkAt func(context.Context, int, string, int) error
	close    func(context.Context, int) error
}

type managedVMRuntimeIdentity struct {
	device uint64
	inode  uint64
}

// managedVMRuntimeLease pins one newly created caller-owned VM output directory beneath root-controlled parents.
// It is deliberately private and exposes no path or descriptor outside the Jailer service.
// THREAD-SAFE: copies share one remove-and-release state guarded by an internal mutex.
type managedVMRuntimeLease struct {
	state *managedVMRuntimeLeaseState
}

type managedVMRuntimeLeaseState struct {
	// mu guards every descriptor, identity, binding, and lifecycle field below.
	mu sync.Mutex

	deps              managedVMRuntimeDeps
	caller            instanceCaller
	vmID              string
	uidDirectoryFD    int
	vmDirectoryFD     int
	identity          managedVMRuntimeIdentity
	retained          []int
	leafCreated       bool
	parentSyncPending bool
	removed           bool
	released          bool
	releaseErr        error
}

func productionManagedVMRuntimePolicy() managedVMRuntimePolicy {
	return managedVMRuntimePolicy{rootPath: "/", trustedUID: 0, trustedGID: 0}
}

// createManagedVMRuntime exclusively creates and pins the fixed runtime directory for one authenticated VM owner.
// Once the VM leaf exists, later failures return a non-nil lease so the caller retains exact cleanup authority.
func createManagedVMRuntime(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	policy managedVMRuntimePolicy,
	caller instanceCaller,
	vmID string,
) (_ *managedVMRuntimeLease, returnErr error) {
	if ctx == nil {
		return nil, instanceAtomicError("create managed VM runtime: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("create managed VM runtime", err)
	}
	if err := validateManagedVMRuntimeDeps(deps); err != nil {
		return nil, err
	}
	if err := validateManagedVMRuntimePolicy(policy); err != nil {
		return nil, err
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("managed VM runtime VM ID is invalid")
	}

	retained := make([]int, 0, 6)
	defer func() {
		if returnErr == nil || retained == nil {
			return
		}
		if closeErr := closeManagedVMRuntimeFDs(ctx, deps, retained); closeErr != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close rejected managed VM runtime descriptors",
				closeErr,
			)
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, managedVMRuntimeDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open filesystem root for managed VM runtime", err)
	}
	retained = append(retained, rootFD)
	if _, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		rootFD,
		"filesystem root",
		policy.trustedUID,
		policy.trustedGID,
		0,
		false,
	); err != nil {
		return nil, err
	}

	runFD, err := openExistingManagedVMRuntimeDirectory(ctx, deps, rootFD, "run", policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runFD)
	mvmctlFD, err := openOrCreateManagedVMRuntimeParent(ctx, deps, runFD, "mvmctl", policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, mvmctlFD)
	runtimeFD, err := openOrCreateManagedVMRuntimeParent(ctx, deps, mvmctlFD, "runtime", policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runtimeFD)
	uidName := managedVMRuntimeUIDName(caller.uid)
	uidFD, err := openOrCreateManagedVMRuntimeParent(ctx, deps, runtimeFD, uidName, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, uidFD)

	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("create managed VM runtime directory", err)
	}
	if err := deps.mkdirAt(ctx, uidFD, vmID, managedVMRuntimeVMMode); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, instanceStateError(vmID, "managed VM runtime directory already exists")
		}
		return nil, instanceAtomicError("create managed VM runtime directory", err)
	}
	how := unix.OpenHow{
		Flags:   uint64(managedVMRuntimeDirectoryFlags),
		Resolve: uint64(managedVMRuntimeResolveManaged),
	}
	vmFD, err := deps.openAt2(ctx, uidFD, vmID, &how)
	if err != nil {
		var primary error = instanceAtomicError("open created managed VM runtime directory", err)
		removeErr := deps.unlinkAt(context.WithoutCancel(ctx), uidFD, vmID, unix.AT_REMOVEDIR)
		if removeErr != nil {
			primary = appendInstanceOperationError(primary, "remove unpinned managed VM runtime directory", removeErr)
		}
		return nil, primary
	}
	retained = append(retained, vmFD)
	identity, err := inspectManagedVMRuntimeIdentity(ctx, deps, vmFD, "created VM runtime directory")
	if err != nil {
		primary := err
		closeErr := deps.close(context.WithoutCancel(ctx), vmFD)
		retained = retained[:len(retained)-1]
		removeErr := deps.unlinkAt(context.WithoutCancel(ctx), uidFD, vmID, unix.AT_REMOVEDIR)
		primary = appendInstanceOperationError(primary, "close unadmitted managed VM runtime directory", closeErr)
		primary = appendInstanceOperationError(primary, "remove unadmitted managed VM runtime directory", removeErr)
		return nil, primary
	}
	lease := &managedVMRuntimeLease{state: &managedVMRuntimeLeaseState{
		deps:           deps,
		caller:         caller,
		vmID:           vmID,
		uidDirectoryFD: uidFD,
		vmDirectoryFD:  vmFD,
		identity:       identity,
		retained:       retained,
		leafCreated:    true,
	}}
	retained = nil
	if err := prepareManagedVMRuntimeDirectory(ctx, lease.state); err != nil {
		return lease, err
	}
	return lease, nil
}

func openExistingManagedVMRuntimeDirectory(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	parentFD int,
	name string,
	policy managedVMRuntimePolicy,
) (int, error) {
	how := unix.OpenHow{Flags: uint64(managedVMRuntimeDirectoryFlags), Resolve: uint64(managedVMRuntimeResolveFixed)}
	fd, err := deps.openAt2(ctx, parentFD, name, &how)
	if err != nil {
		return -1, instanceAtomicError("open fixed managed VM runtime directory "+name, err)
	}
	if _, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		fd,
		name,
		policy.trustedUID,
		policy.trustedGID,
		0,
		false,
	); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(ctx, deps, fd, err)
	}
	return fd, nil
}

func openOrCreateManagedVMRuntimeParent(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	parentFD int,
	name string,
	policy managedVMRuntimePolicy,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, instanceAtomicError("open managed VM runtime parent "+name, err)
	}
	how := unix.OpenHow{Flags: uint64(managedVMRuntimeDirectoryFlags), Resolve: uint64(managedVMRuntimeResolveManaged)}
	fd, err := deps.openAt2(ctx, parentFD, name, &how)
	if err == nil {
		if _, err := inspectManagedVMRuntimeDirectory(
			ctx,
			deps,
			fd,
			name,
			policy.trustedUID,
			policy.trustedGID,
			managedVMRuntimeParentMode,
			true,
		); err != nil {
			return -1, closeRejectedManagedVMRuntimeFD(ctx, deps, fd, err)
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, instanceAtomicError("open managed VM runtime parent "+name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, managedVMRuntimeParentMode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, instanceAtomicError("create managed VM runtime parent "+name, mkdirErr)
	}
	fd, err = deps.openAt2(ctx, parentFD, name, &how)
	if err != nil {
		return -1, instanceAtomicError("open created managed VM runtime parent "+name, err)
	}
	if !created {
		if _, err := inspectManagedVMRuntimeDirectory(
			ctx,
			deps,
			fd,
			name,
			policy.trustedUID,
			policy.trustedGID,
			managedVMRuntimeParentMode,
			true,
		); err != nil {
			return -1, closeRejectedManagedVMRuntimeFD(ctx, deps, fd, err)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.trustedUID), int(policy.trustedGID)); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("set owner on managed VM runtime parent "+name, err),
		)
	}
	if err := deps.fchmod(ctx, fd, managedVMRuntimeParentMode); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("set mode on managed VM runtime parent "+name, err),
		)
	}
	if _, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		fd,
		name,
		policy.trustedUID,
		policy.trustedGID,
		managedVMRuntimeParentMode,
		true,
	); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("sync managed VM runtime parent "+name, err),
		)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedManagedVMRuntimeFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("sync parent of managed VM runtime directory "+name, err),
		)
	}
	return fd, nil
}

func prepareManagedVMRuntimeDirectory(ctx context.Context, state *managedVMRuntimeLeaseState) error {
	if err := state.deps.fchown(ctx, state.vmDirectoryFD, int(state.caller.uid), int(state.caller.gid)); err != nil {
		return instanceAtomicError("set managed VM runtime owner", err)
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("prepare managed VM runtime directory", err)
	}
	if err := state.deps.fchmod(ctx, state.vmDirectoryFD, managedVMRuntimeVMMode); err != nil {
		return instanceAtomicError("set managed VM runtime mode", err)
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("prepare managed VM runtime directory", err)
	}
	identity, err := inspectManagedVMRuntimeDirectory(
		ctx,
		state.deps,
		state.vmDirectoryFD,
		"VM runtime",
		state.caller.uid,
		state.caller.gid,
		managedVMRuntimeVMMode,
		true,
	)
	if err != nil {
		return err
	}
	if identity != state.identity {
		return instanceAtomicError("managed VM runtime directory identity changed during preparation", nil)
	}
	if err := state.deps.fsync(ctx, state.vmDirectoryFD); err != nil {
		return instanceAtomicError("sync managed VM runtime directory", err)
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("prepare managed VM runtime directory", err)
	}
	if err := state.deps.fsync(ctx, state.uidDirectoryFD); err != nil {
		return instanceAtomicError("sync managed VM runtime UID directory", err)
	}
	return nil
}

func inspectManagedVMRuntimeDirectory(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	fd int,
	name string,
	expectedUID uint32,
	expectedGID uint32,
	expectedMode uint32,
	exactMode bool,
) (managedVMRuntimeIdentity, error) {
	if err := ctx.Err(); err != nil {
		return managedVMRuntimeIdentity{}, instanceAtomicError("inspect managed VM runtime directory "+name, err)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return managedVMRuntimeIdentity{}, instanceAtomicError("inspect managed VM runtime directory "+name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Dev == 0 || stat.Ino == 0 {
		return managedVMRuntimeIdentity{}, instanceAtomicError(
			"managed VM runtime directory "+name+" has invalid identity",
			nil,
		)
	}
	if stat.Uid != expectedUID || stat.Gid != expectedGID {
		return managedVMRuntimeIdentity{}, instanceAtomicError(
			"managed VM runtime directory "+name+" has unexpected ownership",
			nil,
		)
	}
	mode := stat.Mode & 0o7777
	if mode&0o022 != 0 || mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 ||
		(exactMode && mode != expectedMode) {
		return managedVMRuntimeIdentity{}, instanceAtomicError(
			"managed VM runtime directory "+name+" has unsafe mode",
			nil,
		)
	}
	return managedVMRuntimeIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func inspectManagedVMRuntimeIdentity(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	fd int,
	name string,
) (managedVMRuntimeIdentity, error) {
	if err := ctx.Err(); err != nil {
		return managedVMRuntimeIdentity{}, instanceAtomicError("inspect managed VM runtime directory "+name, err)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return managedVMRuntimeIdentity{}, instanceAtomicError("inspect managed VM runtime directory "+name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Dev == 0 || stat.Ino == 0 {
		return managedVMRuntimeIdentity{}, instanceAtomicError(
			"managed VM runtime directory "+name+" has invalid identity",
			nil,
		)
	}
	return managedVMRuntimeIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func validateManagedVMRuntimePolicy(policy managedVMRuntimePolicy) error {
	if !filepath.IsAbs(policy.rootPath) || filepath.Clean(policy.rootPath) != policy.rootPath ||
		strings.ContainsRune(policy.rootPath, '\x00') || policy.trustedUID == math.MaxUint32 ||
		policy.trustedGID == math.MaxUint32 {
		return instanceAtomicError("managed VM runtime policy is invalid", nil)
	}
	return nil
}

func validateManagedVMRuntimeDeps(deps managedVMRuntimeDeps) error {
	if deps.open == nil || deps.openAt2 == nil || deps.mkdirAt == nil || deps.fstat == nil || deps.fchown == nil ||
		deps.fchmod == nil || deps.fsync == nil || deps.unlinkAt == nil || deps.close == nil {
		return instanceAtomicError("managed VM runtime dependencies are incomplete", nil)
	}
	return nil
}

func managedVMRuntimeUIDName(uid uint32) string {
	return strconv.FormatUint(uint64(uid), 10)
}

func closeRejectedManagedVMRuntimeFD(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	fd int,
	primary error,
) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return appendInstanceOperationError(primary, "close rejected managed VM runtime descriptor", err)
	}
	return primary
}

func closeManagedVMRuntimeFDs(ctx context.Context, deps managedVMRuntimeDeps, fds []int) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func realManagedVMRuntimeDeps() managedVMRuntimeDeps {
	return managedVMRuntimeDeps{
		open: func(_ context.Context, name string, flags int, mode uint32) (int, error) {
			return unix.Open(name, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		fstat:  func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
	}
}
