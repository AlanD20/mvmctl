package jailer

import (
	"context"
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

// Permanent receipt authority lives in the external volume record; raw capture stays on cache FS.
type initialVolumeRemovalReceipt struct {
	Schema               int    `json:"schema"`
	VolumeID             string `json:"volume_id"`
	DeviceMajor          uint32 `json:"device_major"`
	DeviceMinor          uint32 `json:"device_minor"`
	Inode                uint64 `json:"inode"`
	MountID              uint64 `json:"mount_id"`
	UID                  uint32 `json:"uid"`
	GID                  uint32 `json:"gid"`
	Mode                 uint16 `json:"mode"`
	Size                 uint64 `json:"size"`
	DirectoryDeviceMajor uint32 `json:"directory_device_major"`
	DirectoryDeviceMinor uint32 `json:"directory_device_minor"`
	DirectoryInode       uint64 `json:"directory_inode"`
	DirectoryMountID     uint64 `json:"directory_mount_id"`
}

type initialVolumeRemovalProgress struct {
	Phase        string                       `json:"phase"`
	Quarantine   protectedBackingIdentity     `json:"quarantine"`
	Receipt      *initialVolumeRemovalReceipt `json:"receipt,omitempty"`
	SourceAbsent bool                         `json:"source_absent"`
}

func validateInitialVolumeRemoval(record protectedBackingRecord) error {
	progress := record.InitialRemoval
	if progress == nil {
		if record.Phase == "initial-removing" {
			return managedVMResourceValidationError("initial volume removal authority is missing")
		}
		return nil
	}
	if record.Kind != protectedBackingVolume || record.SnapshotSource ||
		(record.Phase != "initial-removing" && record.Phase != "removed") ||
		len(record.References) != 0 || len(record.NextReferences) != 0 {
		return managedVMResourceValidationError("initial volume removal authority conflicts with backing")
	}
	switch progress.Phase {
	case "preparing":
		if progress.Receipt != nil || progress.SourceAbsent {
			return managedVMResourceValidationError("initial volume preparation authority is invalid")
		}
	case "prepared", "captured":
		if progress.Quarantine.Inode == 0 || progress.Receipt == nil || progress.SourceAbsent {
			return managedVMResourceValidationError("initial volume capture authority is incomplete")
		}
	case "removed":
		if record.Phase != "removed" || progress.Quarantine.Inode == 0 ||
			(progress.Receipt == nil) != progress.SourceAbsent {
			return managedVMResourceValidationError("initial volume terminal authority is incomplete")
		}
	default:
		return managedVMResourceValidationError("initial volume removal phase is invalid")
	}
	if record.Phase == "removed" && progress.Phase != "removed" {
		return managedVMResourceValidationError("initial volume backing completed before source removal")
	}
	if receipt := progress.Receipt; receipt != nil {
		if receipt.Schema != 1 || receipt.VolumeID != record.ID || receipt.UID != record.Owner ||
			receipt.Inode == 0 || receipt.MountID == 0 ||
			receipt.DirectoryInode == 0 || receipt.DirectoryMountID == 0 || receipt.Size == 0 ||
			receipt.Size > math.MaxInt64 || receipt.Mode&unix.S_IFMT != unix.S_IFREG ||
			receipt.Mode&0600 != 0600 || receipt.Mode&07133 != 0 {
			return managedVMResourceValidationError("initial volume removal receipt is invalid")
		}
	}
	return nil
}

// Changed/replaced staging remains caller-owned evidence rather than deletion authority.
func (storage *protectedVolumeStorage) protectedSourcePresent(
	ctx context.Context,
	id string,
) (_ bool, returnErr error) {
	record := storage.backing.record
	if record.Source.Inode == 0 || record.SnapshotSource {
		return false, managedVMResourceValidationError("protected volume has no managed raw source")
	}
	directory, err := openManagedVMResourceDirectory(
		ctx,
		storage.cache.deps,
		storage.cache.cacheFD,
		"volumes",
		"volume store",
		storage.cache.ownerUID,
	)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	var named unix.Stat_t
	if err := unix.Fstatat(directory, id+".raw", &named, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return false, syncInitialVolumeDirectory(ctx, directory)
	} else if err != nil {
		return false, err
	}
	raw, err := storage.openInitialRaw(ctx, id, false)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, raw.Release()) }()
	if err := requireProtectedSourceIdentity(record, raw); err != nil {
		return false, err
	}
	if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
		return false, err
	}
	return true, nil
}

func requireProtectedSourceIdentity(record protectedBackingRecord, raw *initialRawVolume) error {
	if record.SourceCTimeSec == 0 || record.Source.Size <= 0 ||
		unix.Mkdev(raw.identity.deviceMajor, raw.identity.deviceMinor) != record.Source.Device ||
		raw.identity.inode != record.Source.Inode || raw.identity.size != uint64(record.Source.Size) ||
		raw.identity.ctime.Sec != record.SourceCTimeSec || int64(raw.identity.ctime.Nsec) != record.SourceCTimeNsec {
		return managedVMResourceValidationError("managed raw volume source changed after protection")
	}
	return nil
}

func (storage *protectedVolumeStorage) removeInitialRaw(ctx context.Context, id string) (returnErr error) {
	if err := validateInitialVolumeRemoval(storage.backing.record); err != nil {
		return err
	}
	if progress := storage.backing.record.InitialRemoval; progress != nil && progress.Phase == "removed" {
		// Seal a visible terminal publication; never inspect/adopt a replacement caller source.
		return syncProtectedCacheDirectory(ctx, storage.backing.metadataFD)
	}
	store, err := openProtectedStoreBranch(ctx, storage.cache, "volume-removals", true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	return storage.removeInitialRawFromStore(ctx, id, store)
}

// The production coordinator consumes the selected cache's already admitted root branch lease.
func (storage *protectedVolumeStorage) removeInitialRawFromStore(ctx context.Context, id string,
	store *protectedSnapshotStoreLease) (returnErr error) {
	if err := validateInitialVolumeRemoval(storage.backing.record); err != nil {
		return err
	}
	if progress := storage.backing.record.InitialRemoval; progress != nil && progress.Phase == "removed" {
		return syncProtectedCacheDirectory(ctx, storage.backing.metadataFD)
	}
	if storage.backing.record.InitialRemoval == nil {
		if store == nil || store.fd < 0 || store.released || store.branch != "volume-removals" ||
			store.cacheIdentity != storage.cache.identity || store.ownerUID != storage.cache.ownerUID {
			return managedVMResourceValidationError("initial volume removal branch authority conflicts")
		}
		if err := protectedCacheNameAbsent(ctx, store.fd, id); err != nil {
			return err
		}
	}
	if err := storage.backing.BeginInitialVolumeRemoval(ctx); err != nil {
		return err
	}
	// Seal a prior visible record rename before any resumed nonterminal effect.
	if err := syncProtectedCacheDirectory(ctx, storage.backing.metadataFD); err != nil {
		return err
	}
	quarantine, err := storage.backing.openInitialVolumeQuarantine(ctx, storage.cache, store)
	if err != nil {
		return err
	}
	entries, err := initialVolumeRemovalEntries(ctx, quarantine)
	if err != nil {
		return err
	}
	progress := storage.backing.record.InitialRemoval
	if progress.Phase == "preparing" {
		if len(entries) != 0 {
			return managedVMResourceValidationError("initial volume preparation retains unrecorded data")
		}
		receipt, absent, err := storage.prepareInitialVolumeReceipt(ctx, id)
		if err != nil {
			return err
		}
		if absent {
			if err := unix.Fsync(quarantine); err != nil {
				return err
			}
			return storage.backing.CommitInitialVolumeRemoval(ctx, true)
		}
		progress.Receipt, progress.Phase = &receipt, "prepared"
		if err := writeProtectedRecord(ctx, storage.backing.metadataFD, "record.json", storage.backing.record); err != nil {
			return err
		}
	}
	receipt := *progress.Receipt
	directory, err := storage.openInitialRemovalSourceDirectory(ctx, receipt)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	if progress.Phase == "prepared" {
		if !entries["data.raw"] {
			if storage.backing.record.Source.Inode != 0 {
				raw, err := storage.openInitialRaw(ctx, id, false)
				if err != nil {
					return err
				}
				err = errors.Join(
					requireProtectedSourceIdentity(storage.backing.record, raw),
					raw.requireCurrentName(ctx, storage.cache),
					raw.Release(),
				)
				if err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Renameat2(directory, id+".raw", quarantine, "data.raw", unix.RENAME_NOREPLACE); err != nil {
				return managedVMResourceAtomicError("quarantine initial raw volume", err)
			}
		}
		if err := matchInitialVolumeQuarantine(ctx, quarantine, receipt); err != nil {
			return err
		}
		if err := errors.Join(syncInitialVolumeDirectory(ctx, directory), unix.Fsync(quarantine)); err != nil {
			return err
		}
		progress.Phase = "captured"
		if err := writeProtectedRecord(ctx, storage.backing.metadataFD, "record.json", storage.backing.record); err != nil {
			return err
		}
	}
	if err := matchInitialVolumeQuarantine(ctx, quarantine, receipt); err == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Unlinkat(quarantine, "data.raw", 0); err != nil {
			return err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Fsync(quarantine); err != nil {
		return err
	}
	return storage.backing.CommitInitialVolumeRemoval(ctx, false)
}

func (storage *protectedVolumeStorage) prepareInitialVolumeReceipt(
	ctx context.Context,
	id string,
) (_ initialVolumeRemovalReceipt, _ bool, returnErr error) {
	directory, err := openManagedVMResourceDirectory(
		ctx,
		storage.cache.deps,
		storage.cache.cacheFD,
		"volumes",
		"volume store",
		storage.cache.ownerUID,
	)
	if err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	var named unix.Stat_t
	if err := unix.Fstatat(directory, id+".raw", &named, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return initialVolumeRemovalReceipt{}, true, syncInitialVolumeDirectory(ctx, directory)
	} else if err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	raw, err := storage.openInitialRaw(ctx, id, false)
	if err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, raw.Release()) }()
	if storage.backing.record.Source.Inode != 0 {
		if err := requireProtectedSourceIdentity(storage.backing.record, raw); err != nil {
			return initialVolumeRemovalReceipt{}, false, err
		}
	}
	if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	stat, err := stableManagedVMConfigStat(ctx, storage.cache.configDeps, raw.directory)
	if err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	mount, err := initialVolumeMountID(raw.directory)
	if err != nil {
		return initialVolumeRemovalReceipt{}, false, err
	}
	return initialVolumeRemovalReceipt{Schema: 1, VolumeID: id, DeviceMajor: raw.identity.deviceMajor,
		DeviceMinor: raw.identity.deviceMinor, Inode: raw.identity.inode, MountID: raw.mountID,
		UID: raw.identity.uid, GID: raw.identity.gid, Mode: raw.identity.mode, Size: raw.identity.size,
		DirectoryDeviceMajor: stat.deviceMajor, DirectoryDeviceMinor: stat.deviceMinor,
		DirectoryInode: stat.inode, DirectoryMountID: mount}, false, nil
}

func (storage *protectedVolumeStorage) openInitialRemovalSourceDirectory(
	ctx context.Context,
	receipt initialVolumeRemovalReceipt,
) (int, error) {
	directory, err := openManagedVMResourceDirectory(
		ctx,
		storage.cache.deps,
		storage.cache.cacheFD,
		"volumes",
		"volume store",
		storage.cache.ownerUID,
	)
	if err != nil {
		return -1, err
	}
	stat, err := stableManagedVMConfigStat(ctx, storage.cache.configDeps, directory)
	if err != nil {
		return -1, errors.Join(err, unix.Close(directory))
	}
	mount, err := initialVolumeMountID(directory)
	if err != nil || stat.deviceMajor != receipt.DirectoryDeviceMajor ||
		stat.deviceMinor != receipt.DirectoryDeviceMinor ||
		stat.inode != receipt.DirectoryInode ||
		mount != receipt.DirectoryMountID {
		return -1, errors.Join(
			managedVMResourceAtomicError("initial volume source directory changed during removal", err),
			unix.Close(directory),
		)
	}
	return directory, nil
}

func initialVolumeRemovalEntries(ctx context.Context, directory int) (map[string]bool, error) {
	names, err := protectedCleanupNames(ctx, directory)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]bool)
	for _, name := range names {
		if name != "data.raw" {
			return nil, managedVMResourceValidationError("unknown or old-layout initial volume quarantine entry")
		}
		entries[name] = true
	}
	return entries, nil
}

func matchInitialVolumeQuarantine(
	ctx context.Context,
	directory int,
	receipt initialVolumeRemovalReceipt,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(directory, "data.raw", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	identity, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return err
	}
	mount, err := initialVolumeMountID(fd)
	if err != nil {
		return err
	}
	if identity.deviceMajor != receipt.DeviceMajor ||
		identity.deviceMinor != receipt.DeviceMinor ||
		identity.inode != receipt.Inode ||
		identity.uid != receipt.UID ||
		identity.gid != receipt.GID ||
		identity.mode != receipt.Mode ||
		identity.size != receipt.Size ||
		identity.nlink != 1 ||
		mount != receipt.MountID {
		return managedVMResourceValidationError("quarantined volume differs from removal receipt; contents retained")
	}
	return nil
}

func syncInitialVolumeDirectory(ctx context.Context, directory int) error {
	return syncProtectedCacheDirectory(ctx, directory)
}
