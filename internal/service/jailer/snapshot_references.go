package jailer

import (
	"context"
	"encoding/json"
	"errors"

	"golang.org/x/sys/unix"
)

type snapshotRestoreReference struct {
	Schema     uint32 `json:"schema"`
	SnapshotID string `json:"snapshot_id"`
	VMID       string `json:"vm_id"`
	OwnerUID   uint32 `json:"owner_uid"`
}

func (entry *snapshotStoreEntry) openReferences(ctx context.Context) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	fd, err := unix.Openat2(entry.directoryFD, "refs", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && (stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0) {
		err = instanceValidationError("snapshot reference directory is not root protected")
	}
	if err != nil {
		return -1, errors.Join(err, unix.Close(fd))
	}
	return fd, nil
}

func (entry *snapshotStoreEntry) addRestoreReference(
	ctx context.Context,
	prepared *preparedSnapshotRestore,
) (returnErr error) {
	refs, err := entry.openReferences(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(refs)) }()
	name := prepared.targetVMID + ".json"
	fd, err := unix.Openat2(refs, name, &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    0400,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			// No capability has reached launch, so unwind only this exact created inode.
			var pinned, current unix.Stat_t
			pinErr := unix.Fstat(fd, &pinned)
			nameErr := unix.Fstatat(refs, name, &current, unix.AT_SYMLINK_NOFOLLOW)
			if pinErr == nil && nameErr == nil && pinned.Dev == current.Dev && pinned.Ino == current.Ino {
				returnErr = errors.Join(returnErr, unix.Unlinkat(refs, name, 0), unix.Fsync(refs))
			} else {
				returnErr = errors.Join(returnErr, pinErr, nameErr)
			}
		}
		returnErr = errors.Join(returnErr, unix.Close(fd))
	}()
	data, err := json.Marshal(
		snapshotRestoreReference{
			Schema:     1,
			SnapshotID: prepared.snapshotID,
			VMID:       prepared.targetVMID,
			OwnerUID:   prepared.ownerUID,
		},
	)
	if err != nil {
		return err
	}
	if err := writeSnapshotBytes(ctx, fd, data); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	return unix.Fsync(refs)
}

func removePreparedSnapshotReference(ctx context.Context, prepared *preparedSnapshotRestore) error {
	if prepared.registered || !prepared.referenceCreated {
		return instanceValidationError("restore reference was transferred to lifecycle authority")
	}
	return removeSnapshotReference(
		ctx,
		prepared.ownerUID,
		prepared.cacheIdentity,
		prepared.snapshotID,
		prepared.targetVMID,
	)
}

// CALLER MUST HOLD: exact cleaning authority after verified process absence. The durable
// launch snapshot ID proves that an absent reference is an idempotent cleanup suffix.
func releaseRestoredSnapshotReference(ctx context.Context, record instanceRecord) error {
	if record.launch == nil || record.launch.snapshotID == "" {
		return nil
	}
	if record.lifecycle != instanceLifecycleCleaning {
		return instanceStateError(record.vmID, "snapshot reference release requires stopped cleaning authority")
	}
	return removeSnapshotReference(
		ctx,
		record.ownerUID,
		record.launch.cacheIdentity,
		record.launch.snapshotID,
		record.vmID,
	)
}

func removeSnapshotReference(
	ctx context.Context,
	ownerUID uint32,
	cacheIdentity managedCacheIdentity,
	snapshotID, vmID string,
) (returnErr error) {
	if !isCanonicalManagedResourceID(snapshotID, 64) || !vmIDPattern.MatchString(vmID) || ownerUID == 0 {
		return instanceValidationError("snapshot cleanup reference identity is invalid")
	}
	store, err := reopenProtectedSnapshotStore(ctx, ownerUID, cacheIdentity)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	entry, err := lockSnapshotStoreEntry(ctx, store, snapshotID, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx))) }()
	refs, err := entry.openReferences(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(refs)) }()
	name := vmID + ".json"
	fd, err := unix.Openat2(refs, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode != unix.S_IFREG|0400 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size < 1 ||
		stat.Size > 4096 {
		return instanceValidationError("snapshot restore reference metadata is unsafe")
	}
	data, err := readLaunchDeviceBytes(ctx, fd, int(stat.Size))
	if err != nil {
		return err
	}
	var reference snapshotRestoreReference
	if err := json.Unmarshal(data, &reference); err != nil {
		return err
	}
	if reference.Schema != 1 || reference.SnapshotID != snapshotID || reference.VMID != vmID ||
		reference.OwnerUID != ownerUID {
		return instanceValidationError("snapshot restore reference differs from lifecycle authority")
	}
	if err := unix.Unlinkat(refs, name, 0); err != nil {
		return err
	}
	return unix.Fsync(refs)
}
