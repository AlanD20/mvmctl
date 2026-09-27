package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

// The receipt precedes namespace mutation; captured precedes unlink. A crash never permits re-import.
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

// protectedSourcePresent admits only the original mvm-created source inode. A caller
// that replaced or changed it retains that file; root must not remove it by pathname.
func (storage *protectedVolumeStorage) protectedSourcePresent(
	ctx context.Context, id string,
) (_ bool, returnErr error) {
	record := storage.backing.record
	if record.Source.Inode == 0 || record.SnapshotSource {
		return false, managedVMResourceValidationError("protected volume has no managed raw source")
	}
	directory, err := openManagedVMResourceDirectory(ctx, storage.cache.deps, storage.cache.cacheFD,
		"volumes", "volume store", storage.cache.ownerUID)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	var named unix.Stat_t
	if err := unix.Fstatat(directory, id+".raw", &named, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return false, syncInitialVolumeDirectory(directory)
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
		raw.identity.ctime.Sec != record.SourceCTimeSec ||
		int64(raw.identity.ctime.Nsec) != record.SourceCTimeNsec {
		return managedVMResourceValidationError("managed raw volume source changed after protection")
	}
	return nil
}

func (storage *protectedVolumeStorage) removeInitialRaw(ctx context.Context, id string) (returnErr error) {
	if err := storage.backing.BeginInitialVolumeRemoval(ctx); err != nil {
		return err
	}
	quarantine, err := storage.backing.openInitialVolumeQuarantine(ctx, storage.cache)
	if err != nil {
		return err
	}
	entries, err := initialVolumeRemovalEntries(quarantine)
	if err != nil {
		return err
	}
	var receipt initialVolumeRemovalReceipt
	if entries["receipt.json"] {
		receipt, err = readInitialVolumeRemovalReceipt(ctx, quarantine, id, storage.cache.ownerUID)
		if err != nil {
			return err
		}
	} else {
		if entries["data.raw"] || entries["captured"] {
			return managedVMResourceValidationError("initial volume quarantine has no removal receipt")
		}
		if storage.backing.record.Source.Inode != 0 {
			present, err := storage.protectedSourcePresent(ctx, id)
			if err != nil {
				return err
			}
			if !present {
				return storage.backing.CommitInitialVolumeRemoval(ctx)
			}
		}
		raw, err := storage.openInitialRaw(ctx, id, false)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, raw.Release()) }()
		if storage.backing.record.Source.Inode != 0 {
			if err := requireProtectedSourceIdentity(storage.backing.record, raw); err != nil {
				return err
			}
		}
		if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
			return err
		}
		directory, err := stableManagedVMConfigStat(ctx, storage.cache.configDeps, raw.directory)
		if err != nil {
			return err
		}
		directoryMount, err := initialVolumeMountID(raw.directory)
		if err != nil {
			return err
		}
		receipt = initialVolumeRemovalReceipt{
			Schema: 1, VolumeID: id, DeviceMajor: raw.identity.deviceMajor, DeviceMinor: raw.identity.deviceMinor,
			Inode: raw.identity.inode, MountID: raw.mountID, UID: raw.identity.uid, GID: raw.identity.gid,
			Mode: raw.identity.mode, Size: raw.identity.size,
			DirectoryDeviceMajor: directory.deviceMajor, DirectoryDeviceMinor: directory.deviceMinor,
			DirectoryInode: directory.inode, DirectoryMountID: directoryMount,
		}
		payload, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if err := writeInitialVolumeRemovalFile(ctx, quarantine, "receipt.json", payload); err != nil {
			return err
		}
	}

	directory, err := openManagedVMResourceDirectory(ctx, storage.cache.deps, storage.cache.cacheFD,
		"volumes", "volume store", storage.cache.ownerUID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	directoryStat, err := stableManagedVMConfigStat(ctx, storage.cache.configDeps, directory)
	if err != nil {
		return err
	}
	directoryMount, err := initialVolumeMountID(directory)
	if err != nil {
		return err
	}
	if directoryStat.deviceMajor != receipt.DirectoryDeviceMajor ||
		directoryStat.deviceMinor != receipt.DirectoryDeviceMinor ||
		directoryStat.inode != receipt.DirectoryInode ||
		directoryMount != receipt.DirectoryMountID {
		return managedVMResourceValidationError("initial volume source directory changed during removal")
	}
	if !entries["captured"] {
		if !entries["data.raw"] {
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
		if err := syncInitialVolumeDirectory(directory); err != nil {
			return err
		}
		if err := unix.Fsync(quarantine); err != nil {
			return err
		}
		if err := writeInitialVolumeRemovalFile(ctx, quarantine, "captured", nil); err != nil {
			return err
		}
	} else if err := requireInitialVolumeCapturedMarker(quarantine); err != nil {
		return err
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
	return storage.backing.CommitInitialVolumeRemoval(ctx)
}

func initialVolumeRemovalEntries(directory int) (_ map[string]bool, returnErr error) {
	fd, err := unix.Openat(directory, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "initial volume removal quarantine")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	names, err := file.Readdirnames(4)
	if err != nil && err != io.EOF {
		return nil, err
	}
	entries := make(map[string]bool, len(names))
	for _, name := range names {
		switch name {
		case "receipt.json", "data.raw", "captured":
			entries[name] = true
		default:
			return nil, managedVMResourceValidationError("unknown initial volume quarantine entry")
		}
	}
	return entries, nil
}

func readInitialVolumeRemovalReceipt(
	ctx context.Context, directory int, id string, uid uint32,
) (_ initialVolumeRemovalReceipt, returnErr error) {
	if err := ctx.Err(); err != nil {
		return initialVolumeRemovalReceipt{}, err
	}
	fd, err := unix.Openat(directory, "receipt.json", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return initialVolumeRemovalReceipt{}, err
	}
	file := os.NewFile(uintptr(fd), "initial volume removal receipt")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return initialVolumeRemovalReceipt{}, err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size <= 0 ||
		stat.Size > 4096 {
		return initialVolumeRemovalReceipt{}, managedVMResourceValidationError(
			"initial volume removal receipt metadata is invalid",
		)
	}
	payload, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return initialVolumeRemovalReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var receipt initialVolumeRemovalReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return receipt, managedVMResourceValidationError("initial volume removal receipt has trailing content")
	}
	if receipt.Schema != 1 ||
		receipt.VolumeID != id ||
		receipt.UID != uid ||
		receipt.Inode == 0 ||
		receipt.MountID == 0 ||
		receipt.DirectoryInode == 0 ||
		receipt.DirectoryMountID == 0 ||
		receipt.Size == 0 ||
		receipt.Size > math.MaxInt64 ||
		receipt.Mode&unix.S_IFMT != unix.S_IFREG ||
		receipt.Mode&0600 != 0600 ||
		receipt.Mode&07133 != 0 {
		return receipt, managedVMResourceValidationError("initial volume removal receipt is invalid")
	}
	return receipt, nil
}

func matchInitialVolumeQuarantine(
	ctx context.Context,
	directory int,
	receipt initialVolumeRemovalReceipt,
) (returnErr error) {
	fd, err := unix.Openat(directory, "data.raw", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	identity, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return err
	}
	mountID, err := initialVolumeMountID(fd)
	if err != nil {
		return err
	}
	if identity.deviceMajor != receipt.DeviceMajor || identity.deviceMinor != receipt.DeviceMinor ||
		identity.inode != receipt.Inode || identity.uid != receipt.UID || identity.gid != receipt.GID ||
		identity.mode != receipt.Mode || identity.size != receipt.Size || identity.nlink != 1 || mountID != receipt.MountID {
		return managedVMResourceValidationError("quarantined volume differs from removal receipt; contents retained")
	}
	return nil
}

func writeInitialVolumeRemovalFile(ctx context.Context, directory int, name string, payload []byte) (returnErr error) {
	if name != "receipt.json" && name != "captured" {
		return managedVMResourceValidationError("unknown initial volume removal metadata")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(directory, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	for len(payload) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Write(fd, payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	return unix.Fsync(directory)
}

func requireInitialVolumeCapturedMarker(directory int) (returnErr error) {
	fd, err := unix.Openat(directory, "captured", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size != 0 {
		return managedVMResourceValidationError("initial volume captured marker is invalid")
	}
	return nil
}

func syncInitialVolumeDirectory(directory int) (returnErr error) {
	fd, err := unix.Openat(directory, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	return unix.Fsync(fd)
}
