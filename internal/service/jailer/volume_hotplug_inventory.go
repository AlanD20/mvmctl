package jailer

import (
	"context"
	"errors"
	"sort"

	"golang.org/x/sys/unix"
)

func (authority *volumeHotplugAuthority) openVolumeTransaction(
	ctx context.Context,
	volumeID string,
) (_ *volumeHotplugTransaction, _ []volumeHotplugRecord, _ vmLaunchDeviceAuthority, returnErr error) {
	instance := authority.locked.record
	inventory, err := readVMLaunchDeviceAuthority(ctx, instance, authority.root)
	if err != nil {
		return nil, nil, vmLaunchDeviceAuthority{}, err
	}
	if err := inventory.admitVolumeCount(0); err != nil {
		return nil, nil, vmLaunchDeviceAuthority{}, err
	}
	volumes, err := openVolumeHotplugDirectory(ctx, authority.root, "volumes", 0755, true)
	if err != nil {
		return nil, nil, vmLaunchDeviceAuthority{}, err
	}
	journal, err := openVolumeHotplugDirectory(ctx, volumes, volumeHotplugJournalDirectory, 0700, true)
	if err != nil {
		return nil, nil, vmLaunchDeviceAuthority{}, errors.Join(err, unix.Close(volumes))
	}
	transaction := &volumeHotplugTransaction{directory: volumes, journal: journal, source: -1,
		record: volumeHotplugRecord{Volume: VMLaunchVolume{ID: volumeID}}}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, transaction.close())
		}
	}()
	records, err := listVolumeHotplugRecords(ctx, journal, instance)
	if err != nil {
		return nil, nil, vmLaunchDeviceAuthority{}, err
	}
	for _, record := range records {
		if record.Volume.ID == volumeID {
			transaction.record = record
			return transaction, records, inventory, nil
		}
	}
	for _, initial := range inventory.Volumes {
		if initial.Selection.ID != volumeID {
			continue
		}
		record := newVolumeHotplugRecord(instance, initial.Selection)
		record.SourceMajor, record.SourceMinor, record.SourceInode = initial.DeviceMajor, initial.DeviceMinor, initial.Inode
		record.SourceUID, record.SourceGID, record.SourceMode = initial.UID, initial.GID, initial.Mode
		record.Initial, record.State = true, "attached"
		transaction.record = record
		target, mounted, err := transaction.inspectTarget(ctx)
		if err != nil {
			return nil, nil, vmLaunchDeviceAuthority{}, err
		}
		closeErr := unix.Close(target)
		parent, err := volumeHotplugMountIdentity(ctx, volumes)
		if err != nil {
			return nil, nil, vmLaunchDeviceAuthority{}, errors.Join(err, closeErr)
		}
		if !record.matchesSource(mounted) || mounted.Mnt_id == parent.Mnt_id {
			return nil, nil, vmLaunchDeviceAuthority{}, errors.Join(
				instanceStateError(instance.vmID, "initial volume mount differs from root launch inventory"), closeErr)
		}
		if closeErr != nil {
			return nil, nil, vmLaunchDeviceAuthority{}, closeErr
		}
		transaction.record.MountID = mounted.Mnt_id
	}
	return transaction, records, inventory, nil
}

func volumeHotplugDesiredCount(
	inventory vmLaunchDeviceAuthority,
	records []volumeHotplugRecord,
	volumeID string,
	attach bool,
) int {
	present := make(map[string]bool, len(inventory.Volumes)+len(records))
	for _, volume := range inventory.Volumes {
		present[volume.Selection.ID] = true
	}
	for _, record := range records {
		present[record.Volume.ID] = record.State != "detached"
	}
	present[volumeID] = attach
	count := 0
	for _, attached := range present {
		if attached {
			count++
		}
	}
	return count
}

// readCurrentVMVolumeInventory combines the immutable launch inventory with
// completed root-owned hotplug transactions. The caller retains the active VM
// lock and verified root; uncertain transitions cannot authorize a snapshot.
func readCurrentVMVolumeInventory(
	ctx context.Context,
	instance instanceRecord,
	rootFD int,
) (_ []launchDeviceVolume, returnErr error) {
	initial, err := readVMLaunchDeviceAuthority(ctx, instance, rootFD)
	if err != nil {
		return nil, err
	}
	volumes, err := openVolumeHotplugDirectory(ctx, rootFD, "volumes", 0755, false)
	if errors.Is(err, unix.ENOENT) {
		return initial.Volumes, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(volumes)) }()
	journal, err := openVolumeHotplugDirectory(ctx, volumes, volumeHotplugJournalDirectory, 0700, false)
	if errors.Is(err, unix.ENOENT) {
		return initial.Volumes, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(journal)) }()
	merged, err := mergeVolumeHotplugDeviceAuthority(ctx, journal, instance, initial)
	if err != nil {
		return nil, err
	}
	return merged.Volumes, nil
}

// mergeVolumeHotplugDeviceAuthority applies only completed, root-owned intents.
// The caller retains the VM lock and exact journal directory; stopped cleanup
// must archive this result before removing either journal or launch metadata.
func mergeVolumeHotplugDeviceAuthority(
	ctx context.Context,
	journalFD int,
	instance instanceRecord,
	initial vmLaunchDeviceAuthority,
) (vmLaunchDeviceAuthority, error) {
	records, err := listVolumeHotplugRecords(ctx, journalFD, instance)
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	byID := make(map[string]launchDeviceVolume, len(initial.Volumes)+len(records))
	for _, volume := range initial.Volumes {
		byID[volume.Selection.ID] = volume
	}
	for _, record := range records {
		switch record.State {
		case "detached":
			delete(byID, record.Volume.ID)
		case "attached":
			byID[record.Volume.ID] = launchDeviceVolume{
				Selection: record.Volume, DeviceMajor: record.SourceMajor, DeviceMinor: record.SourceMinor,
				Inode: record.SourceInode, UID: record.SourceUID, GID: record.SourceGID, Mode: record.SourceMode,
			}
		default:
			return vmLaunchDeviceAuthority{}, instanceStateError(instance.vmID, "volume inventory has an incomplete hotplug transition")
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]launchDeviceVolume, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	initial.Volumes = result
	return initial, nil
}
