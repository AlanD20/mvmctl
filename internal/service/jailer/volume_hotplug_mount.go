package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

func volumeHotplugMountIdentity(ctx context.Context, fd int) (unix.Statx_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Statx_t{}, err
	}
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_BASIC_STATS|unix.STATX_MNT_ID_UNIQUE, &stat); err != nil {
		return unix.Statx_t{}, err
	}
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO |
		unix.STATX_SIZE | unix.STATX_NLINK | unix.STATX_MNT_ID_UNIQUE)
	if stat.Mask&required != required || stat.Mnt_id == 0 || stat.Ino == 0 {
		return unix.Statx_t{}, instanceAtomicError(
			"volume hotplug requires complete metadata and unique mount IDs",
			nil,
		)
	}
	return stat, nil
}

func (record volumeHotplugRecord) matchesSource(stat unix.Statx_t) bool {
	return stat.Dev_major == record.SourceMajor && stat.Dev_minor == record.SourceMinor &&
		stat.Ino == record.SourceInode &&
		stat.Uid == record.SourceUID && stat.Gid == record.SourceGID && stat.Mode == record.SourceMode &&
		stat.Nlink == 1 &&
		stat.Mode&unix.S_IFMT == unix.S_IFREG &&
		stat.Size == record.Volume.ExpectedBytes
}

func (transaction *volumeHotplugTransaction) inspectTarget(
	ctx context.Context,
) (_ int, _ unix.Statx_t, returnErr error) {
	fd, err := unix.Openat2(transaction.directory, transaction.record.Volume.private().basename(), &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return -1, unix.Statx_t{}, err
	}
	stat, err := volumeHotplugMountIdentity(ctx, fd)
	if err != nil {
		return -1, unix.Statx_t{}, errors.Join(err, unix.Close(fd))
	}
	return fd, stat, nil
}

func (transaction *volumeHotplugTransaction) prepareMount(ctx context.Context) (_ int, returnErr error) {
	if !transaction.attach {
		return -1, nil
	}
	if transaction.source < 0 {
		return -1, instanceStateError(transaction.record.VMID, "volume attach has no retained source descriptor")
	}
	source, err := volumeHotplugMountIdentity(ctx, transaction.source)
	if err != nil {
		return -1, err
	}
	if !transaction.record.matchesSource(source) {
		return -1, instanceStateError(transaction.record.VMID, "volume source changed after root descriptor admission")
	}
	mount, err := unix.OpenTree(
		transaction.source, "", uint(unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH),
	)
	if err != nil {
		return -1, instanceAtomicError("clone retained volume source mount", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(mount))
		}
	}()
	attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC)
	if transaction.record.Volume.ReadOnly {
		attributes |= unix.MOUNT_ATTR_RDONLY
	}
	if err := unix.MountSetattr(mount, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: attributes}); err != nil {
		return -1, instanceAtomicError("set retained volume mount attributes", err)
	}
	return mount, nil
}

func (transaction *volumeHotplugTransaction) applyMount(ctx context.Context, mount int) error {
	if transaction.attach {
		return transaction.attachMount(ctx, mount)
	}
	return transaction.detachMount(ctx)
}

func (transaction *volumeHotplugTransaction) attachMount(ctx context.Context, mount int) (returnErr error) {
	record := &transaction.record
	name := record.Volume.private().basename()
	target, stat, err := transaction.inspectTarget(ctx)
	if errors.Is(err, unix.ENOENT) {
		if record.TargetInode != 0 {
			return instanceStateError(record.VMID, "retained volume placeholder disappeared")
		}
		target, err = unix.Openat(transaction.directory, name,
			unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		if err := unix.Fchmod(target, 0600); err != nil {
			return errors.Join(err, unix.Close(target))
		}
		stat, err = volumeHotplugMountIdentity(ctx, target)
		if err != nil {
			return errors.Join(err, unix.Close(target))
		}
		record.TargetDevice = unix.Mkdev(stat.Dev_major, stat.Dev_minor)
		record.TargetInode = stat.Ino
		if err := unix.Fsync(transaction.directory); err != nil {
			return errors.Join(err, unix.Close(target))
		}
		if err := transaction.write(ctx); err != nil {
			return errors.Join(err, unix.Close(target))
		}
	} else if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(target)) }()
	if record.MountID != 0 && stat.Mnt_id == record.MountID && record.matchesSource(stat) {
		return nil
	}
	if !record.matchesPlaceholder(stat) {
		return instanceStateError(record.VMID, "volume target is not the retained exclusive placeholder")
	}
	if mount < 0 {
		return instanceStateError(record.VMID, "volume attach has no prepared source mount")
	}
	identity, err := volumeHotplugMountIdentity(ctx, mount)
	if err != nil {
		return err
	}
	if !record.matchesSource(identity) {
		return instanceStateError(record.VMID, "prepared volume mount differs from retained source identity")
	}
	record.MountID = identity.Mnt_id
	if err := transaction.write(ctx); err != nil {
		return err
	}
	if err := unix.MoveMount(
		mount,
		"",
		target,
		"",
		unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH,
	); err != nil {
		return err
	}
	current, mounted, err := transaction.inspectTarget(ctx)
	if err != nil {
		return err
	}
	closeErr := unix.Close(current)
	if mounted.Mnt_id != record.MountID || !record.matchesSource(mounted) {
		return errors.Join(
			instanceStateError(record.VMID, "published volume mount differs from retained intent"),
			closeErr,
		)
	}
	return closeErr
}

func (record volumeHotplugRecord) matchesPlaceholder(stat unix.Statx_t) bool {
	return record.TargetInode != 0 && stat.Ino == record.TargetInode &&
		unix.Mkdev(stat.Dev_major, stat.Dev_minor) == record.TargetDevice && stat.Mode == unix.S_IFREG|0600 &&
		stat.Uid == 0 && stat.Gid == 0 && stat.Nlink == 1 && stat.Size == 0
}

func (transaction *volumeHotplugTransaction) detachMount(ctx context.Context) (returnErr error) {
	record := &transaction.record
	target, stat, err := transaction.inspectTarget(ctx)
	if errors.Is(err, unix.ENOENT) {
		if record.State != "detaching" && record.State != "detached" {
			return instanceStateError(record.VMID, "volume mount disappeared before detach intent")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(target)) }()
	if stat.Mnt_id == record.MountID && record.matchesSource(stat) {
		// DELETE/readback already proved no API drive. Detach the exact pinned
		// mount. The Jailer namespace has no /proc, so resolve the canonical
		// basename relative to its pinned root-owned volume directory.
		if err := unix.Fchdir(transaction.directory); err != nil {
			return err
		}
		if err := unix.Unmount("./"+record.Volume.private().basename(), unix.MNT_DETACH); err != nil {
			return err
		}
		current, placeholder, err := transaction.inspectTarget(ctx)
		if err != nil {
			return err
		}
		closeErr := unix.Close(current)
		if closeErr != nil {
			return closeErr
		}
		if record.Initial && record.TargetInode == 0 && placeholder.Mode == unix.S_IFREG|0600 &&
			placeholder.Uid == 0 && placeholder.Gid == 0 && placeholder.Nlink == 1 && placeholder.Size == 0 {
			record.TargetDevice = unix.Mkdev(placeholder.Dev_major, placeholder.Dev_minor)
			record.TargetInode = placeholder.Ino
			if err := transaction.write(ctx); err != nil {
				return err
			}
		}
		stat = placeholder
	}
	if !record.matchesPlaceholder(stat) {
		return instanceStateError(record.VMID, "detached volume target does not match retained placeholder")
	}
	if err := unix.Unlinkat(transaction.directory, record.Volume.private().basename(), 0); err != nil {
		return err
	}
	return unix.Fsync(transaction.directory)
}
