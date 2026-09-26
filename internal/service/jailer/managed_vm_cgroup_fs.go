package jailer

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

const (
	managedVMCgroupDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedVMCgroupResolveFlags   = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV
	managedVMCgroupRootMode   = uint32(0o555)
	managedVMCgroupParentMode = uint32(0o755)
	managedVMCgroupLeafMode   = uint32(0o700)
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
	write    func(context.Context, int, []byte) (int, error)
	unlinkAt func(context.Context, int, string, int) error
	close    func(context.Context, int) error

	trustedUID uint32
	trustedGID uint32
}

type managedVMCgroupReadDeps struct {
	openRoot func(context.Context) (int, error)
	openAt2  func(context.Context, int, string, *unix.OpenHow) (int, error)
	statx    func(context.Context, int, *unix.Statx_t) error
	statfs   func(context.Context, int, *unix.Statfs_t) error
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
		managedVMCgroupReadDepsFrom(state.deps),
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
	return inspectManagedVMCgroupReadDirectory(
		ctx,
		managedVMCgroupReadDepsFrom(deps),
		fd,
		description,
		wantMode,
	)
}

func inspectManagedVMCgroupReadDirectory(
	ctx context.Context,
	deps managedVMCgroupReadDeps,
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

func inspectManagedVMCgroupRegularFile(
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
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO)
	if stat.Mask&required != required || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint32(stat.Mode)&0o7777 != wantMode || stat.Uid != deps.trustedUID || stat.Gid != deps.trustedGID ||
		stat.Ino == 0 {
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

func managedVMCgroupIdentityFromDirectoryStatx(
	deps managedVMCgroupReadDeps,
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
	mode := uint32(stat.Mode) & 0o7777
	// The kernel-owned cgroup-v2 mount root may be 0555 even on a writable
	// mount. Its managed parent and VM leaves still require exact 0755.
	if mode != wantMode && !(wantMode == managedVMCgroupRootMode && mode == 0o755) {
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

func inspectManagedVMCgroupRoot(
	ctx context.Context,
	deps managedVMCgroupReadDeps,
	rootFD int,
	description string,
) (managedVMCgroupIdentity, error) {
	root, err := inspectManagedVMCgroupReadDirectory(ctx, deps, rootFD, description, managedVMCgroupRootMode)
	if err != nil {
		return managedVMCgroupIdentity{}, err
	}
	var filesystem unix.Statfs_t
	if err := deps.statfs(ctx, rootFD, &filesystem); err != nil {
		return managedVMCgroupIdentity{}, instanceAtomicError("inspect "+description+" filesystem", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" is not a cgroup-v2 mount", nil)
	}
	return root, nil
}

func inspectManagedVMCgroupChild(
	ctx context.Context,
	deps managedVMCgroupReadDeps,
	fd int,
	description string,
	wantMode uint32,
	parent managedVMCgroupIdentity,
) (managedVMCgroupIdentity, error) {
	child, err := inspectManagedVMCgroupReadDirectory(ctx, deps, fd, description, wantMode)
	if err != nil {
		return managedVMCgroupIdentity{}, err
	}
	if child.mount != parent.mount ||
		child.deviceMajor != parent.deviceMajor || child.deviceMinor != parent.deviceMinor {
		return managedVMCgroupIdentity{}, instanceAtomicError(description+" is outside the pinned cgroup-v2 mount", nil)
	}
	return child, nil
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
		deps.fchmod == nil || deps.statx == nil || deps.statfs == nil || deps.pread == nil || deps.write == nil ||
		deps.unlinkAt == nil || deps.close == nil {
		return instanceAtomicError("managed VM cgroup dependencies are incomplete", nil)
	}
	return nil
}

func managedVMCgroupReadDepsFrom(deps managedVMCgroupDeps) managedVMCgroupReadDeps {
	return managedVMCgroupReadDeps{
		openRoot: deps.openRoot,
		openAt2:  deps.openAt2,
		statx:    deps.statx,
		statfs:   deps.statfs,
		close:    deps.close,

		trustedUID: deps.trustedUID,
		trustedGID: deps.trustedGID,
	}
}

func validateManagedVMCgroupReadDeps(deps managedVMCgroupReadDeps) error {
	if deps.openRoot == nil || deps.openAt2 == nil || deps.statx == nil || deps.statfs == nil || deps.close == nil {
		return instanceAtomicError("managed VM cgroup read dependencies are incomplete", nil)
	}
	return nil
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

func closeManagedVMCgroupReadFDs(ctx context.Context, deps managedVMCgroupReadDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(ctx, fds[index]); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
