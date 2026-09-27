package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// removeStoppedVolumeHotplugJournal runs only from stopped-tree cleanup, after
// all retained processes have exited. The surrounding root identity and NO_XDEV
// traversal prove this is persistent jail metadata, never a surviving mount.
func removeStoppedVolumeHotplugJournal(ctx context.Context, root int, instance instanceRecord) (returnErr error) {
	volumes, err := openVolumeHotplugDirectory(ctx, root, "volumes", 0755, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(volumes)) }()
	journal, err := openVolumeHotplugDirectory(ctx, volumes, volumeHotplugJournalDirectory, 0700, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(journal)) }()
	records, err := listVolumeHotplugRecords(ctx, journal, instance)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if instance.active == nil {
			return instanceStateError(instance.vmID, "hotplug cleanup has no committed execution identity")
		}
		if record.State == "detached" {
			var target unix.Stat_t
			err := unix.Fstatat(volumes, record.Volume.private().basename(), &target,
				unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT)
			if err == nil {
				return instanceStateError(instance.vmID, "detached volume target remains in stopped jail")
			}
			if !errors.Is(err, unix.ENOENT) {
				return err
			}
		} else {
			backing, err := reopenProtectedBacking(ctx, record.backingBinding(instance))
			if err != nil {
				return err
			}
			revokeErr := backing.Revoke(ctx, instance.vmID, instance.active.userIDs.effective)
			if err := errors.Join(revokeErr, backing.Release(context.WithoutCancel(ctx))); err != nil {
				return err
			}
		}
		if err := unix.Unlinkat(journal, record.Volume.ID+".json", 0); err != nil {
			return err
		}
	}
	if err := unix.Fsync(journal); err != nil {
		return err
	}
	var retained, entry unix.Stat_t
	if err := unix.Fstat(journal, &retained); err != nil {
		return err
	}
	if err := unix.Fstatat(volumes, volumeHotplugJournalDirectory, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if entry.Dev != retained.Dev || entry.Ino != retained.Ino || entry.Mode != retained.Mode || entry.Uid != 0 ||
		entry.Gid != 0 {
		return instanceStateError(instance.vmID, "stopped volume journal directory was replaced")
	}
	if err := unix.Unlinkat(volumes, volumeHotplugJournalDirectory, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(volumes)
}
