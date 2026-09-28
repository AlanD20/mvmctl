package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const volumeHotplugJournalDirectory = ".hotplug"

type volumeHotplugRecord struct {
	Schema           uint32         `json:"schema"`
	VMID             string         `json:"vm_id"`
	OwnerUID         uint32         `json:"owner_uid"`
	BootID           string         `json:"boot_id"`
	LaunchGeneration uint64         `json:"launch_generation"`
	Volume           VMLaunchVolume `json:"volume"`
	GuestDriveID     string         `json:"guest_drive_id"`
	SourceMajor      uint32         `json:"source_major"`
	SourceMinor      uint32         `json:"source_minor"`
	SourceInode      uint64         `json:"source_inode"`
	SourceUID        uint32         `json:"source_uid"`
	SourceGID        uint32         `json:"source_gid"`
	SourceMode       uint16         `json:"source_mode"`
	Initial          bool           `json:"initial"`
	State            string         `json:"state"`
	TargetDevice     uint64         `json:"target_device"`
	TargetInode      uint64         `json:"target_inode"`
	MountID          uint64         `json:"mount_id"`
}

type volumeHotplugTransaction struct {
	directory int
	journal   int
	source    int
	attach    bool
	record    volumeHotplugRecord
}

func openVolumeHotplugDirectory(ctx context.Context, parent int, name string, mode uint32, create bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if create {
		if err := unix.Mkdirat(parent, name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && (stat.Mode != unix.S_IFDIR|mode || stat.Uid != 0 || stat.Gid != 0) {
		err = instanceAtomicError("volume journal directory metadata is not admitted", nil)
	}
	if err != nil {
		return -1, errors.Join(err, unix.Close(fd))
	}
	if create {
		if err := unix.Fsync(parent); err != nil {
			return -1, errors.Join(err, unix.Close(fd))
		}
	}
	return fd, nil
}

func readVolumeHotplugRecord(
	ctx context.Context,
	directory int,
	name string,
) (_ volumeHotplugRecord, _ bool, returnErr error) {
	fd, err := unix.Openat2(directory, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if errors.Is(err, unix.ENOENT) {
		return volumeHotplugRecord{}, false, nil
	}
	if err != nil {
		return volumeHotplugRecord{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return volumeHotplugRecord{}, false, err
	}
	if before.Mode != unix.S_IFREG|0600 || before.Uid != 0 || before.Gid != 0 || before.Nlink != 1 ||
		before.Size <= 0 ||
		before.Size > 16*1024 {
		return volumeHotplugRecord{}, false, instanceAtomicError("volume journal record metadata is not admitted", nil)
	}
	raw, err := readBoundedProcessDescriptor(ctx, fd, 16*1024)
	if err != nil {
		return volumeHotplugRecord{}, false, err
	}
	var record volumeHotplugRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return volumeHotplugRecord{}, false, err
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return volumeHotplugRecord{}, false, instanceAtomicError("volume journal is not exact canonical schema", err)
	}
	if record.Schema != 2 || !isCanonicalManagedResourceID(record.GuestDriveID, 64) || !vmIDPattern.MatchString(record.VMID) || record.OwnerUID == 0 ||
		!hostBootIDPattern.MatchString(record.BootID) || record.LaunchGeneration == 0 ||
		record.SourceInode == 0 {
		return volumeHotplugRecord{}, false, instanceAtomicError("volume journal identity is invalid", nil)
	}
	if err := record.Volume.Validate(); err != nil {
		return volumeHotplugRecord{}, false, err
	}
	switch record.State {
	case "mounting", "attached", "detaching", "detached":
	default:
		return volumeHotplugRecord{}, false, instanceAtomicError("volume journal state is invalid", nil)
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(directory, name, &entry, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT); err != nil {
		return volumeHotplugRecord{}, false, err
	}
	if entry.Dev != before.Dev || entry.Ino != before.Ino || entry.Mode != before.Mode || entry.Uid != before.Uid ||
		entry.Gid != before.Gid || entry.Nlink != before.Nlink || entry.Size != before.Size || entry.Mtim != before.Mtim || entry.Ctim != before.Ctim {
		return volumeHotplugRecord{}, false, instanceAtomicError("volume journal changed while reading", nil)
	}
	return record, true, nil
}

func (transaction *volumeHotplugTransaction) write(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(transaction.record)
	if err != nil {
		return err
	}
	name := transaction.record.Volume.ID + ".json"
	temporary := transaction.record.Volume.ID + ".pending"
	if err := removeVolumeHotplugPending(ctx, transaction.journal, temporary); err != nil {
		return err
	}
	fd, err := unix.Openat(transaction.journal, temporary,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := unix.Fchmod(fd, 0600); err != nil {
		return err
	}
	if err := writeAllInstanceRecord(ctx, realInstanceAuthorityDeps(), fd, raw); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if err := unix.Renameat(transaction.journal, temporary, transaction.journal, name); err != nil {
		return err
	}
	if err := unix.Fsync(transaction.journal); err != nil {
		return volumeHotplugUnknown(transaction.record.VMID, err)
	}
	return nil
}

func removeVolumeHotplugPending(ctx context.Context, directory int, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(directory, name, &stat, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size > 16*1024 {
		return instanceAtomicError("volume journal temporary record metadata is unsafe", nil)
	}
	return unix.Unlinkat(directory, name, 0)
}

func listVolumeHotplugRecords(
	ctx context.Context,
	directory int,
	instance instanceRecord,
) (_ []volumeHotplugRecord, returnErr error) {
	return scanVolumeHotplugRecords(ctx, directory, instance, true)
}

// readVolumeHotplugRecords never repairs interrupted journal publication.
func readVolumeHotplugRecords(
	ctx context.Context,
	directory int,
	instance instanceRecord,
) ([]volumeHotplugRecord, error) {
	return scanVolumeHotplugRecords(ctx, directory, instance, false)
}

func scanVolumeHotplugRecords(
	ctx context.Context,
	directory int,
	instance instanceRecord,
	repair bool,
) (_ []volumeHotplugRecord, returnErr error) {
	fd, err := unix.Openat(directory, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "volume-hotplug-journal")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var records []volumeHotplugRecord
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, readErr := file.Readdirnames(32)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		for _, name := range names {
			if id, pending := strings.CutSuffix(name, ".pending"); pending && isCanonicalManagedResourceID(id, 64) {
				if !repair {
					return nil, instanceStateError(
						instance.vmID,
						"volume journal publication is incomplete; stop the VM before recovery",
					)
				}
				if err := removeVolumeHotplugPending(ctx, directory, name); err != nil {
					return nil, err
				}
				continue
			}
			id, valid := strings.CutSuffix(name, ".json")
			if !valid || !isCanonicalManagedResourceID(id, 64) {
				return nil, instanceAtomicError("volume journal contains an unknown entry", nil)
			}
			record, present, err := readVolumeHotplugRecord(ctx, directory, name)
			if err != nil {
				return nil, err
			}
			if !present || record.Volume.ID != id || record.VMID != instance.vmID ||
				record.OwnerUID != instance.ownerUID ||
				record.LaunchGeneration != instance.launchGeneration ||
				record.BootID != instance.launch.hostBootID {
				return nil, instanceAtomicError("volume journal differs from the retained VM generation", nil)
			}
			records = append(records, record)
			if len(records) > 128 {
				return nil, instanceAtomicError("volume journal exceeds its bounded inventory", nil)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return records, nil
		}
	}
}
