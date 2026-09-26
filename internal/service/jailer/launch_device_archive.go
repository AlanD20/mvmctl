package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// A stopped archive is indexed by the retained instance authority, never by a
// caller cache or SQLite row. Unavailable is durable: uncertain hotplug state
// must prevent capture without preventing termination and jail cleanup.
type stoppedVMLaunchDevices struct {
	Schema           uint32                   `json:"schema"`
	OwnerUID         uint32                   `json:"owner_uid"`
	VMID             string                   `json:"vm_id"`
	LaunchGeneration uint64                   `json:"launch_generation"`
	Source           *vmLaunchDeviceAuthority `json:"source"`
}

func openStoppedVMDeviceDirectory(ctx context.Context, ownerUID uint32, create bool) (_ int, returnErr error) {
	if ownerUID == 0 {
		return -1, instanceValidationError("stopped device owner is invalid")
	}
	deps, policy := realInstanceAuthorityDeps(), productionInstanceAuthorityPolicy()
	fd, err := unix.Open("/", instanceDirectoryFlags, 0)
	if err != nil {
		return -1, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	if err := verifyInstanceDirectory(ctx, deps, fd, "root", policy, instanceAncestorDirectory); err != nil {
		return -1, err
	}
	for index, name := range []string{"var", "lib", "mvmctl", "launch-devices", strconv.FormatUint(uint64(ownerUID), 10)} {
		class := instanceAncestorDirectory
		if index == 2 {
			class = instanceStateBaseDirectory
		} else if index > 2 {
			class = instancePrivateDirectory
		}
		next, err := openInstanceDirectory(ctx, deps, fd, name, create && index >= 2, class, policy)
		if err != nil {
			return -1, err
		}
		old := fd
		fd = next
		if err := unix.Close(old); err != nil {
			return -1, err
		}
	}
	return fd, nil
}

func readStoppedVMDeviceRecord(
	ctx context.Context,
	directory int,
	record instanceRecord,
) (_ *stoppedVMLaunchDevices, returnErr error) {
	if !vmIDPattern.MatchString(record.vmID) {
		return nil, instanceValidationError("stopped device VM is invalid")
	}
	fd, err := unix.Openat2(
		directory,
		record.vmID+".json",
		&unix.OpenHow{Flags: instanceReadFlags | unix.O_NONBLOCK, Resolve: managedVMResourceResolveFlags},
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	deps := realManagedFirecrackerConfigDeps()
	before, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	if before.mode != unix.S_IFREG|0600 || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
		before.size == 0 ||
		before.size > uint64(launchDeviceAuthorityMaxBytes+1024) {
		return nil, instanceStateError(record.vmID, "stopped device archive metadata is not admitted")
	}
	raw, err := readLaunchDeviceBytes(ctx, fd, int(before.size))
	if err != nil {
		return nil, err
	}
	after, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, instanceStateError(record.vmID, "stopped device archive changed during read")
	}
	var archive stoppedVMLaunchDevices
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&archive); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(archive)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, canonical) || archive.Schema != 1 || archive.OwnerUID != record.ownerUID ||
		archive.VMID != record.vmID ||
		archive.LaunchGeneration == 0 ||
		archive.LaunchGeneration > record.launchGeneration {
		return nil, instanceStateError(record.vmID, "stopped device archive binding is not admitted")
	}
	if archive.Source != nil {
		if err := archive.Source.validate(); err != nil {
			return nil, err
		}
		if archive.Source.OwnerUID != archive.OwnerUID || archive.Source.VMID != archive.VMID ||
			archive.Source.LaunchGeneration != archive.LaunchGeneration {
			return nil, instanceStateError(record.vmID, "stopped device source differs from archive binding")
		}
	}
	return &archive, nil
}

func readStoppedVMLaunchDeviceAuthority(
	ctx context.Context,
	record instanceRecord,
) (_ vmLaunchDeviceAuthority, returnErr error) {
	if record.lifecycle != instanceLifecycleCleaned {
		return vmLaunchDeviceAuthority{}, instanceStateError(
			record.vmID,
			"stopped capture requires cleaned instance authority",
		)
	}
	fd, err := openStoppedVMDeviceDirectory(ctx, record.ownerUID, false)
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	archive, err := readStoppedVMDeviceRecord(ctx, fd, record)
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	if archive == nil || archive.LaunchGeneration != record.launchGeneration || archive.Source == nil {
		return vmLaunchDeviceAuthority{}, instanceStateError(
			record.vmID,
			"stopped device inventory is unavailable for this launch",
		)
	}
	return *archive.Source, nil
}

// Called with the VM lock and a root descriptor after all VM processes exited,
// before either the hotplug journal or immutable child manifest is removed.
func archiveStoppedVMLaunchDevices(ctx context.Context, record instanceRecord, rootFD int) (returnErr error) {
	if record.lifecycle != instanceLifecycleCleaning {
		return instanceStateError(record.vmID, "device archival requires cleaning authority")
	}
	directory, err := openStoppedVMDeviceDirectory(ctx, record.ownerUID, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	existing, err := readStoppedVMDeviceRecord(ctx, directory, record)
	if err != nil {
		return err
	}
	if existing != nil && existing.LaunchGeneration == record.launchGeneration {
		return nil
	}
	archive := stoppedVMLaunchDevices{
		Schema:           1,
		OwnerUID:         record.ownerUID,
		VMID:             record.vmID,
		LaunchGeneration: record.launchGeneration,
	}
	metadata, sourceErr := readVMLaunchDeviceAuthority(ctx, record, rootFD)
	if sourceErr == nil {
		metadata, sourceErr = mergeStoppedVMDeviceInventory(ctx, record, rootFD, metadata)
	}
	if sourceErr == nil {
		sourceErr = metadata.validate()
	}
	if sourceErr == nil {
		archive.Source = &metadata
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(archive)
	if err != nil {
		return err
	}
	if len(raw) > launchDeviceAuthorityMaxBytes+1024 {
		return instanceValidationError("stopped device archive exceeds its record bound")
	}
	dirs := &instanceUIDDirectories{
		deps:    realInstanceAuthorityDeps(),
		policy:  productionInstanceAuthorityPolicy(),
		uid:     record.ownerUID,
		stateFD: directory,
	}
	name, temp, err := createInstanceRecordTemp(ctx, dirs)
	if err != nil {
		return err
	}
	open, published := true, false
	defer func() {
		if open {
			returnErr = errors.Join(returnErr, unix.Close(temp))
		}
		if !published {
			returnErr = errors.Join(returnErr, unix.Unlinkat(directory, name, 0))
		}
	}()
	if err := writeAllInstanceRecord(ctx, dirs.deps, temp, raw); err != nil {
		return err
	}
	if err := errors.Join(unix.Fchown(temp, 0, 0), unix.Fchmod(temp, 0600), unix.Fsync(temp)); err != nil {
		return err
	}
	open = false
	if err := unix.Close(temp); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := dirs.inspectRecordTarget(ctx, record.vmID); err != nil {
		return err
	}
	if err := unix.Renameat(directory, name, directory, record.vmID+".json"); err != nil {
		return err
	}
	published = true
	return unix.Fsync(directory)
}

func mergeStoppedVMDeviceInventory(
	ctx context.Context,
	record instanceRecord,
	rootFD int,
	initial vmLaunchDeviceAuthority,
) (_ vmLaunchDeviceAuthority, returnErr error) {
	volumes, err := openVolumeHotplugDirectory(ctx, rootFD, "volumes", 0755, false)
	if errors.Is(err, unix.ENOENT) {
		return initial, nil
	}
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(volumes)) }()
	journal, err := openVolumeHotplugDirectory(ctx, volumes, volumeHotplugJournalDirectory, 0700, false)
	if errors.Is(err, unix.ENOENT) {
		return initial, nil
	}
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(journal)) }()
	return mergeVolumeHotplugDeviceAuthority(ctx, journal, record, initial)
}

func removeStoppedVMLaunchDeviceAuthority(ctx context.Context, record instanceRecord) (returnErr error) {
	if record.lifecycle != instanceLifecycleCleaned {
		return instanceStateError(record.vmID, "device archive removal requires cleaned authority")
	}
	fd, err := openStoppedVMDeviceDirectory(ctx, record.ownerUID, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	archive, err := readStoppedVMDeviceRecord(ctx, fd, record)
	if err != nil || archive == nil {
		return err
	}
	if err := unix.Unlinkat(fd, record.vmID+".json", 0); err != nil {
		return err
	}
	return unix.Fsync(fd)
}
