package authorityfs

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type priorBootCgroupIdentity struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mountID     uint64
	mountKind   uint32
}

type priorBootCgroupObservation struct {
	rootFD   int
	parentFD int
	root     priorBootCgroupIdentity
	parent   priorBootCgroupIdentity
}

func requirePriorBootCgroupAbsent(ctx context.Context, key VMKey) (returnErr error) {
	first, err := observePriorBootCgroupAbsence(ctx, key)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, first.close()) }()
	second, err := observePriorBootCgroupAbsence(ctx, key)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, second.close()) }()
	if first.root != second.root || first.parent != second.parent || (first.parentFD < 0) != (second.parentFD < 0) {
		return fmt.Errorf("authorityfs: prior-boot cgroup hierarchy changed during absence proof")
	}
	return nil
}

func observePriorBootCgroupAbsence(
	ctx context.Context,
	key VMKey,
) (_ *priorBootCgroupObservation, returnErr error) {
	observation := &priorBootCgroupObservation{rootFD: -1, parentFD: -1}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, observation.close())
		}
	}()
	fd, err := openPriorBootDirectory(ctx, []string{"sys", "fs", "cgroup"}, 2, 0755)
	if err != nil {
		return nil, err
	}
	observation.rootFD = fd
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil {
		return nil, err
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, fmt.Errorf("authorityfs: prior-boot cgroup root is not cgroup v2")
	}
	observation.root, err = inspectPriorBootCgroup(fd)
	if err != nil {
		return nil, err
	}
	parent, err := unix.Openat2(fd, "mvmctl", &unix.OpenHow{Flags: directoryFlags, Resolve: resolveManaged})
	if errors.Is(err, unix.ENOENT) {
		return observation, nil
	}
	if err != nil {
		return nil, err
	}
	observation.parentFD = parent
	observation.parent, err = inspectPriorBootCgroup(parent)
	if err != nil {
		return nil, err
	}
	if observation.root.deviceMajor != observation.parent.deviceMajor ||
		observation.root.deviceMinor != observation.parent.deviceMinor ||
		observation.root.mountID != observation.parent.mountID || observation.root.mountKind != observation.parent.mountKind {
		return nil, fmt.Errorf("authorityfs: prior-boot cgroup parent is outside its fixed mount")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	leaf, err := unix.Openat2(parent, key.value, &unix.OpenHow{Flags: directoryFlags, Resolve: resolveManaged})
	if errors.Is(err, unix.ENOENT) {
		return observation, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, errors.Join(fmt.Errorf("authorityfs: prior-boot VM cgroup is present"), unix.Close(leaf))
}

func inspectPriorBootCgroup(fd int) (priorBootCgroupIdentity, error) {
	var stat unix.Statx_t
	mask := unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO |
		unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW,
		mask, &stat); err != nil {
		return priorBootCgroupIdentity{}, err
	}
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO)
	if stat.Mask&required != required || stat.Mode != unix.S_IFDIR|0755 || stat.Uid != 0 || stat.Gid != 0 ||
		stat.Ino == 0 {
		return priorBootCgroupIdentity{}, fmt.Errorf("authorityfs: prior-boot cgroup metadata is not admitted")
	}
	kind := stat.Mask & unix.STATX_MNT_ID_UNIQUE
	if kind == 0 {
		kind = stat.Mask & unix.STATX_MNT_ID
	}
	if kind == 0 || stat.Mnt_id == 0 {
		return priorBootCgroupIdentity{}, fmt.Errorf("authorityfs: prior-boot cgroup mount identity is unavailable")
	}
	return priorBootCgroupIdentity{
		deviceMajor: stat.Dev_major, deviceMinor: stat.Dev_minor, inode: stat.Ino,
		mountID: stat.Mnt_id, mountKind: kind,
	}, nil
}

func (observation *priorBootCgroupObservation) close() error {
	var result error
	if observation.parentFD >= 0 {
		result = unix.Close(observation.parentFD)
		observation.parentFD = -1
	}
	if observation.rootFD >= 0 {
		result = errors.Join(result, unix.Close(observation.rootFD))
		observation.rootFD = -1
	}
	return result
}
