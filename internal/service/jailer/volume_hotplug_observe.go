package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

// ObserveVolume is read-only: it neither creates directories nor repairs journals.
func (server *VMLifecycleServer) ObserveVolume(
	ctx context.Context,
	uid, gid uint32,
	request VMVolumeObserveRequest,
) (_ VMVolumeObservation, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMVolumeObservation{}, err
	}
	authority, err := server.instances.lockVolumeObservation(ctx, instanceCaller{uid: uid, gid: gid}, request.VMID)
	if err != nil {
		return VMVolumeObservation{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, authority.release(ctx)) }()
	return authority.observeSelectedVolume(ctx, request)
}

func (authority *volumeHotplugAuthority) observeSelectedVolume(
	ctx context.Context,
	request VMVolumeObserveRequest,
) (_ VMVolumeObservation, returnErr error) {
	instance := authority.locked.record
	initial, err := readVMLaunchDeviceAuthority(ctx, instance, authority.root)
	if err != nil {
		return VMVolumeObservation{}, err
	}
	if err := initial.admitVolumeCount(0); err != nil {
		return VMVolumeObservation{}, err
	}
	drives, err := authority.observeVolumeDrives(ctx)
	if err != nil {
		return VMVolumeObservation{}, err
	}
	apiDrives := make(map[string]model.DriveConfig, len(drives))
	for _, drive := range drives {
		apiDrives[drive.DriveID] = drive
	}
	directory, err := openVolumeHotplugDirectory(ctx, authority.root, "volumes", 0755, false)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return VMVolumeObservation{}, err
	}
	if directory >= 0 {
		defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	}
	if directory < 0 && len(initial.Volumes) != 0 {
		return VMVolumeObservation{}, instanceStateError(request.VMID, "launch volume directory is missing")
	}
	journal := -1
	if directory >= 0 {
		journal, err = openVolumeHotplugDirectory(ctx, directory, volumeHotplugJournalDirectory, 0700, false)
	}
	var records []volumeHotplugRecord
	if err == nil {
		defer func() { returnErr = errors.Join(returnErr, unix.Close(journal)) }()
		records, err = readVolumeHotplugRecords(ctx, journal, instance)
	} else if errors.Is(err, unix.ENOENT) {
		err = nil
	}
	if err != nil {
		return VMVolumeObservation{}, err
	}
	byID := make(map[string]volumeHotplugRecord, len(initial.Volumes)+len(records))
	for _, volume := range initial.Volumes {
		record := newVolumeHotplugRecord(instance, volume.Selection)
		record.GuestDriveID = volume.GuestDriveID
		record.Initial, record.State = true, "attached"
		record.SourceMajor, record.SourceMinor, record.SourceInode = volume.DeviceMajor, volume.DeviceMinor, volume.Inode
		record.SourceUID, record.SourceGID, record.SourceMode = volume.UID, volume.GID, volume.Mode
		byID[record.Volume.ID] = record
	}
	for _, record := range records {
		if record.State == "mounting" || record.State == "detaching" && record.Volume.ID != request.VolumeID {
			return VMVolumeObservation{}, instanceStateError(
				request.VMID,
				"volume inventory has an incomplete transition",
			)
		}
		byID[record.Volume.ID] = record
	}
	known := make(map[string]bool, len(byID)+2)
	for _, drive := range initial.Config.Drives {
		if drive.DriveID != "rootfs" && drive.DriveID != "cloud-init" {
			continue
		}
		observed, present := apiDrives[drive.DriveID]
		want, wantErr := json.Marshal(drive)
		got, gotErr := json.Marshal(observed)
		if !present || wantErr != nil || gotErr != nil || !bytes.Equal(want, got) {
			return VMVolumeObservation{}, instanceStateError(
				request.VMID,
				"fixed API disk inventory differs from launch authority",
			)
		}
		known[drive.DriveID] = true
	}
	for _, record := range byID {
		if known[record.GuestDriveID] {
			return VMVolumeObservation{}, instanceStateError(request.VMID, "root guest drive IDs are ambiguous")
		}
		known[record.GuestDriveID] = true
	}
	count := 0
	for _, record := range byID {
		if record.State != "detached" {
			count++
		}
	}
	if err := initial.admitVolumeCount(count); err != nil {
		return VMVolumeObservation{}, err
	}
	for id := range apiDrives {
		if !known[id] {
			return VMVolumeObservation{}, instanceStateError(request.VMID, "API inventory contains an unknown drive")
		}
	}
	serials := make(map[string]bool, len(byID)+2)
	for _, binding := range []protectedBackingBinding{initial.Rootfs} {
		serial, err := observeBackingSerial(ctx, binding)
		if err != nil {
			return VMVolumeObservation{}, err
		}
		serials[serial] = true
	}
	if initial.CloudInitBacking != nil {
		serial, err := observeBackingSerial(ctx, *initial.CloudInitBacking)
		if err != nil {
			return VMVolumeObservation{}, err
		}
		if serials[serial] {
			return VMVolumeObservation{}, instanceStateError(request.VMID, "guest disk serials are ambiguous")
		}
		serials[serial] = true
	}
	result := VMVolumeObservation{
		VMID:             request.VMID,
		VolumeID:         request.VolumeID,
		LaunchGeneration: instance.launchGeneration,
	}
	for id, record := range byID {
		transaction := volumeHotplugTransaction{directory: directory, record: record}
		drive, present := apiDrives[record.GuestDriveID]
		var observed *model.DriveConfig
		if present {
			observed = &drive
		}
		if record.State == "detached" {
			if observed != nil {
				return VMVolumeObservation{}, instanceStateError(request.VMID, "detached volume drive reappeared")
			}
			if err := transaction.proveTargetAbsent(ctx); err != nil {
				return VMVolumeObservation{}, err
			}
			continue
		}
		if observed != nil && !volumeRecordDriveMatches(*observed, record) ||
			observed == nil && record.State != "detaching" {
			return VMVolumeObservation{}, instanceStateError(
				request.VMID,
				"volume API inventory differs from root intent",
			)
		}
		serial, err := observeVolumeBackingSerial(ctx, &transaction, instance, observed == nil)
		if err != nil {
			return VMVolumeObservation{}, err
		}
		if serials[serial] {
			return VMVolumeObservation{}, instanceStateError(request.VMID, "guest disk serials are ambiguous")
		}
		serials[serial] = true
		fd, mounted, err := transaction.inspectTarget(ctx)
		if err == nil {
			closeErr := unix.Close(fd)
			parent, parentErr := volumeHotplugMountIdentity(ctx, directory)
			if parentErr != nil || closeErr != nil {
				return VMVolumeObservation{}, errors.Join(parentErr, closeErr)
			}
			if mounted.Mnt_id == parent.Mnt_id || !record.matchesSource(mounted) ||
				record.MountID != 0 && mounted.Mnt_id != record.MountID {
				// A detaching journal can retain the exact unmounted placeholder on retry.
				if record.State != "detaching" || !record.matchesPlaceholder(mounted) {
					return VMVolumeObservation{}, instanceStateError(
						request.VMID,
						"volume target differs from retained mount",
					)
				}
			} else {
				record.MountID = mounted.Mnt_id
			}
		} else if !errors.Is(err, unix.ENOENT) || record.State != "detaching" {
			return VMVolumeObservation{}, err
		}
		if id == request.VolumeID {
			result.Attached, result.GuestDriveID, result.Serial = true, record.GuestDriveID, serial
			result.ExpectedBytes, result.MountID = record.Volume.ExpectedBytes, record.MountID
			result.SourceMajor, result.SourceMinor, result.SourceInode = record.SourceMajor, record.SourceMinor, record.SourceInode
		}
	}
	if _, found := byID[request.VolumeID]; !found {
		if observed, err := authority.observeVolumeDrive(ctx, request.VolumeID); err != nil {
			return VMVolumeObservation{}, err
		} else if observed != nil {
			return VMVolumeObservation{}, instanceStateError(request.VMID, "unknown selected drive is retained")
		}
		if directory >= 0 {
			transaction := volumeHotplugTransaction{
				directory: directory,
				record:    volumeHotplugRecord{GuestDriveID: request.VolumeID},
			}
			if err := transaction.proveTargetAbsent(ctx); err != nil {
				return VMVolumeObservation{}, err
			}
		}
	}
	if err := authority.verify(ctx); err != nil {
		return VMVolumeObservation{}, err
	}
	return result, result.Validate(request.VMID, request.VolumeID)
}

func (transaction *volumeHotplugTransaction) proveTargetAbsent(ctx context.Context) error {
	fd, _, err := transaction.inspectTarget(ctx)
	if err == nil {
		return errors.Join(
			instanceStateError(transaction.record.VMID, "absent volume target reappeared"),
			unix.Close(fd),
		)
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func observeBackingSerial(ctx context.Context, binding protectedBackingBinding) (_ string, returnErr error) {
	lease, err := openObservedBacking(ctx, binding)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	if lease.record.Phase != "ready" || len(lease.record.NextReferences) != 0 {
		return "", instanceValidationError("observed protected backing is incomplete")
	}
	if err := verifyProtectedACL(ctx, lease.fd, lease.record.References); err != nil {
		return "", err
	}
	return observedBackingSerial(lease), nil
}

// openObservedBacking pins existing authority and retains the logical and object locks.
// It does not repair any phase; each named consumer admits its own exact durable intent.
func openObservedBacking(
	ctx context.Context,
	binding protectedBackingBinding,
) (result *protectedBackingLease, returnErr error) {
	defer func() {
		if returnErr != nil && result != nil {
			returnErr = errors.Join(returnErr, result.Release(context.WithoutCancel(ctx)))
			result = nil
		}
	}()
	anchorFD, retained, err := openProtectedLogical(
		ctx,
		binding.ownerUID,
		binding.cacheIdentity,
		binding.kind,
		binding.logicalID,
		false,
	)
	if err != nil {
		return nil, err
	}
	// openProtectedLogical retains its logical object directory immediately before its lock.
	logicalFD := retained[len(retained)-2]
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	var anchor protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchorFD, "store.json", &anchor); err != nil {
		return nil, err
	}
	if anchor.Owner != binding.ownerUID || anchor.Cache != protectedCacheIdentity(binding.cacheIdentity) {
		return nil, instanceValidationError("observed protected anchor differs from lifecycle authority")
	}
	locator, err := parseManagedCacheLocator(anchor.Locator)
	if err != nil {
		return nil, err
	}
	cache, err := pinManagedCache(
		ctx,
		realManagedCacheDeps(),
		productionManagedCachePolicy(),
		instanceCaller{uid: binding.ownerUID, gid: anchor.OwnerGID},
		locator,
	)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	if cache.identity != binding.cacheIdentity {
		return nil, instanceValidationError("observed protected cache identity changed")
	}
	use, err := acquireProtectedStoreUse(ctx, anchorFD, false)
	if err != nil {
		return nil, err
	}
	retained = append(retained, use)
	store, err := pinProtectedStore(ctx, cache, anchorFD, false)
	if err != nil {
		return nil, err
	}
	retained = append(retained, store)
	metadata := anchorFD
	for _, component := range []string{binding.kind, binding.logicalID} {
		fd, err := openProtectedDirectory(ctx, metadata, component, false, true)
		if err != nil {
			return nil, err
		}
		retained, metadata = append(retained, fd), fd
	}
	lock, err := acquireProtectedLockMode(ctx, metadata, false)
	if err != nil {
		return nil, err
	}
	retained = append(retained, lock)
	var record protectedBackingRecord
	if err := readProtectedRecord(ctx, metadata, "record.json", &record); err != nil {
		return nil, err
	}
	if record.Version != 1 || record.Kind != binding.kind || record.ID != binding.logicalID ||
		record.Owner != binding.ownerUID ||
		record.Cache != anchor.Cache ||
		record.Backing.Size != int64(binding.bytes) {
		return nil, instanceValidationError(
			"observed protected backing is incomplete or differs from lifecycle authority",
		)
	}
	directory := store
	for _, component := range []string{binding.kind, binding.logicalID} {
		fd, err := openProtectedDirectory(ctx, directory, component, false, true)
		if err != nil {
			return nil, err
		}
		retained, directory = append(retained, fd), fd
	}
	directoryIdentity, err := protectedIdentity(directory)
	if err != nil || !sameProtectedNode(directoryIdentity, record.Directory) {
		return nil, managedVMResourceAtomicError("observed protected directory identity changed", err)
	}
	fd, err := openProtectedFile(ctx, directory, "disk", unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	retained = append(retained, fd)
	stat, err := volumeHotplugMountIdentity(ctx, fd)
	if err != nil {
		return nil, err
	}
	identity, err := protectedIdentity(fd)
	if err != nil || !sameProtectedNode(identity, record.Backing) || stat.Dev_major != binding.identity.deviceMajor ||
		stat.Dev_minor != binding.identity.deviceMinor ||
		stat.Ino != binding.identity.inode ||
		stat.Size != binding.bytes {
		return nil, managedVMResourceAtomicError("observed protected backing identity changed", err)
	}
	stable, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return nil, err
	}
	lease := &protectedBackingLease{
		fd:            fd,
		identity:      stable,
		ownerUID:      binding.ownerUID,
		cacheIdentity: binding.cacheIdentity,
		metadataFD:    metadata,
		logicalFD:     logicalFD,
		directoryFD:   directory,
		lockFD:        lock,
		record:        record,
		retained:      retained,
	}
	retained = nil
	return lease, nil
}

func observedBackingSerial(lease *protectedBackingLease) string {
	// Firecracker 1.16.2 virtio/block/device.rs builds the 20-byte disk ID
	// by concatenating decimal st_dev, st_rdev, and st_ino. Raw backing is regular.
	return firecrackerVolumeSerial(lease.record.Backing.Device, 0, lease.record.Backing.Inode)
}

func firecrackerVolumeSerial(device, rdev, inode uint64) string {
	serial := fmt.Sprintf("%d%d%d", device, rdev, inode)
	if len(serial) > 20 {
		serial = serial[:20]
	}
	return serial
}
