package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

func (server *VMLifecycleServer) AttachVolume(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMVolumeAttachRequest,
) (_ VMVolumeResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMVolumeResult{}, err
	}
	caller := instanceCaller{uid: ownerUID, gid: ownerGID}
	authority, err := server.instances.lockVolumeHotplug(ctx, caller, request.VMID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release volume attach authority", authority.release(ctx))
	}()
	locator, err := parseManagedCacheLocator(request.CacheRoot)
	if err != nil {
		return VMVolumeResult{}, err
	}
	cache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, locator)
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	if cache.identity != authority.locked.record.launch.cacheIdentity {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"volume cache differs from committed launch authority",
		)
	}
	transaction, records, inventory, err := authority.openVolumeTransaction(ctx, request.Volume.ID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, transaction.close()) }()
	if err := inventory.admitVolumeCount(
		volumeHotplugDesiredCount(inventory, records, request.Volume.ID, true),
	); err != nil {
		return VMVolumeResult{}, err
	}
	if transaction.record.Schema != 0 && transaction.record.Volume != request.Volume {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"volume retry differs from root-owned attachment intent",
		)
	}
	if transaction.record.State == "detaching" {
		return VMVolumeResult{}, instanceStateError(request.VMID, "pending volume detach must finish before attach")
	}
	backing, err := cache.protectVMVolume(ctx, request.Volume.private())
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, backing.Release(context.WithoutCancel(ctx))) }()
	if transaction.record.Schema == 0 || transaction.record.State == "detached" {
		guestDriveID := transaction.record.GuestDriveID
		transaction.record = newVolumeHotplugRecord(authority.locked.record, request.Volume)
		for _, initial := range inventory.Volumes {
			if initial.Selection.ID == request.Volume.ID {
				transaction.record.Initial = true
				transaction.record.GuestDriveID = initial.GuestDriveID
			}
		}
		if guestDriveID != "" {
			transaction.record.GuestDriveID = guestDriveID
		}
		transaction.record.State = "mounting"
	}
	source, err := volumeHotplugMountIdentity(ctx, backing.fd)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if transaction.record.SourceInode != 0 && (transaction.record.SourceMajor != source.Dev_major ||
		transaction.record.SourceMinor != source.Dev_minor || transaction.record.SourceInode != source.Ino ||
		transaction.record.Volume.ExpectedBytes != source.Size) {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"protected backing differs from retained attachment identity",
		)
	}
	transaction.record.SourceMajor, transaction.record.SourceMinor = source.Dev_major, source.Dev_minor
	transaction.record.SourceInode = source.Ino
	transaction.record.SourceUID, transaction.record.SourceGID, transaction.record.SourceMode = source.Uid, source.Gid, source.Mode
	transaction.source, transaction.attach = backing.fd, true
	if err := transaction.write(ctx); err != nil {
		return VMVolumeResult{}, err
	}
	// The durable VM journal precedes the ACL claim, so stopped cleanup can
	// recover a crash at any claim/mount/API boundary without guessing a source.
	if err := backing.Grant(
		ctx,
		request.VMID,
		authority.locked.record.active.userIDs.effective,
		request.Volume.private().access,
	); err != nil {
		return VMVolumeResult{}, err
	}
	source, err = volumeHotplugMountIdentity(ctx, backing.fd)
	if err != nil {
		return VMVolumeResult{}, err
	}
	transaction.record.SourceUID, transaction.record.SourceGID, transaction.record.SourceMode = source.Uid, source.Gid, source.Mode
	if err := transaction.write(ctx); err != nil {
		return VMVolumeResult{}, err
	}
	observed, err := authority.observeVolumeDrive(ctx, transaction.record.GuestDriveID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if observed != nil && !volumeRecordDriveMatches(*observed, transaction.record) {
		return VMVolumeResult{}, instanceStateError(request.VMID, "volume API drive differs from root-owned intent")
	}
	if err := authority.runVolumeHotplugMounts(ctx, transaction); err != nil {
		return VMVolumeResult{}, err
	}
	if observed == nil {
		selection := transaction.record.Volume
		selection.ID = transaction.record.GuestDriveID
		body, err := json.Marshal(volumeDrive(selection))
		if err != nil {
			return VMVolumeResult{}, err
		}
		if _, err := authority.volumeHotplugAPI(
			ctx,
			http.MethodPut,
			"/drives/"+transaction.record.GuestDriveID,
			body,
		); err != nil {
			return VMVolumeResult{}, err
		}
	}
	observed, err = authority.observeVolumeDrive(ctx, transaction.record.GuestDriveID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if observed == nil || !volumeRecordDriveMatches(*observed, transaction.record) {
		return VMVolumeResult{}, volumeHotplugUnknown(request.VMID, nil)
	}
	transaction.record.State = "attached"
	if err := transaction.write(ctx); err != nil {
		return VMVolumeResult{}, err
	}
	return transaction.result(true), nil
}

func (server *VMLifecycleServer) DetachVolume(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMVolumeDetachRequest,
) (_ VMVolumeResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMVolumeResult{}, err
	}
	authority, err := server.instances.lockVolumeHotplug(
		ctx,
		instanceCaller{uid: ownerUID, gid: ownerGID},
		request.VMID,
	)
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, authority.release(ctx)) }()
	if request.Observed.LaunchGeneration != authority.locked.record.launchGeneration {
		return VMVolumeResult{}, instanceStateError(request.VMID, "volume launch generation changed; retry observation")
	}
	fresh, err := authority.observeSelectedVolume(
		ctx,
		VMVolumeObserveRequest{VMID: request.VMID, VolumeID: request.VolumeID},
	)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if fresh != request.Observed {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"volume attachment identity changed; retry observation",
		)
	}
	if !fresh.Attached {
		return VMVolumeResult{
			VMID:             request.VMID,
			VolumeID:         request.VolumeID,
			LaunchGeneration: fresh.LaunchGeneration,
			Attached:         false,
		}, nil
	}
	transaction, _, _, err := authority.openVolumeTransaction(ctx, request.VolumeID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, transaction.close()) }()
	observed, err := authority.observeVolumeDrive(ctx, transaction.record.GuestDriveID)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if transaction.record.Schema != 2 ||
		transaction.record.State != "attached" && transaction.record.State != "detaching" {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"selected attachment differs from fresh root observation",
		)
	}
	backing, err := reopenProtectedBacking(ctx, transaction.record.backingBinding(authority.locked.record))
	if err != nil {
		return VMVolumeResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, backing.Release(context.WithoutCancel(ctx))) }()
	identity, err := volumeHotplugMountIdentity(ctx, backing.fd)
	if err != nil {
		return VMVolumeResult{}, err
	}
	if identity.Dev_major != transaction.record.SourceMajor || identity.Dev_minor != transaction.record.SourceMinor ||
		identity.Ino != transaction.record.SourceInode || identity.Size != transaction.record.Volume.ExpectedBytes {
		return VMVolumeResult{}, instanceStateError(
			request.VMID,
			"detach backing differs from root-owned attachment identity",
		)
	}
	if observed != nil && !volumeRecordDriveMatches(*observed, transaction.record) {
		return VMVolumeResult{}, instanceStateError(request.VMID, "detach API drive differs from root-owned intent")
	}
	transaction.record.State = "detaching"
	if err := transaction.write(ctx); err != nil {
		return VMVolumeResult{}, err
	}
	if observed != nil {
		if _, err := authority.volumeHotplugAPI(
			ctx,
			http.MethodDelete,
			"/drives/"+transaction.record.GuestDriveID,
			nil,
		); err != nil {
			return VMVolumeResult{}, err
		}
	}
	if observed, err = authority.observeVolumeDrive(ctx, transaction.record.GuestDriveID); err != nil {
		return VMVolumeResult{}, err
	} else if observed != nil {
		return VMVolumeResult{}, volumeHotplugUnknown(request.VMID, nil)
	}
	transaction.attach = false
	if err := authority.runVolumeHotplugMounts(ctx, transaction); err != nil {
		return VMVolumeResult{}, err
	}
	if err := backing.Revoke(ctx, request.VMID, authority.locked.record.active.userIDs.effective); err != nil {
		return VMVolumeResult{}, err
	}
	transaction.record.State = "detached"
	if err := transaction.write(ctx); err != nil {
		return VMVolumeResult{}, err
	}
	// Initial receipts retain captured guest IDs for restored-volume retry. A live
	// addition uses its logical ID as the guest ID, so complete absence is sufficient.
	if !transaction.record.Initial {
		if err := unix.Unlinkat(transaction.journal, request.VolumeID+".json", 0); err != nil {
			return VMVolumeResult{}, err
		}
		if err := unix.Fsync(transaction.journal); err != nil {
			return VMVolumeResult{}, err
		}
	}
	return transaction.result(false), nil
}

func newVolumeHotplugRecord(instance instanceRecord, selection VMLaunchVolume) volumeHotplugRecord {
	return volumeHotplugRecord{Schema: 2, GuestDriveID: selection.ID, VMID: instance.vmID, OwnerUID: instance.ownerUID,
		BootID: instance.launch.hostBootID, LaunchGeneration: instance.launchGeneration, Volume: selection}
}

func volumeDrive(volume VMLaunchVolume) model.DriveConfig {
	return model.DriveConfig{DriveID: volume.ID, PathOnHost: volume.private().jailPath(),
		IsReadOnly: volume.ReadOnly, CacheType: volume.CacheType, IOEngine: "Sync"}
}

func volumeDriveMatches(observed model.DriveConfig, selection VMLaunchVolume) bool {
	want := volumeDrive(selection)
	return observed.DriveID == want.DriveID && observed.PathOnHost == want.PathOnHost &&
		observed.IsRootDevice == want.IsRootDevice && observed.IsReadOnly == want.IsReadOnly &&
		observed.CacheType == want.CacheType && observed.IOEngine == want.IOEngine && observed.Partuuid == nil &&
		observed.RateLimiter == nil && observed.Socket == nil
}

func volumeRecordDriveMatches(observed model.DriveConfig, record volumeHotplugRecord) bool {
	selection := record.Volume
	selection.ID = record.GuestDriveID
	return volumeDriveMatches(observed, selection)
}

func (record volumeHotplugRecord) basename() string {
	return record.GuestDriveID + ".raw"
}

func (transaction *volumeHotplugTransaction) result(attached bool) VMVolumeResult {
	return VMVolumeResult{VMID: transaction.record.VMID, VolumeID: transaction.record.Volume.ID,
		LaunchGeneration: transaction.record.LaunchGeneration, Attached: attached}
}

func (transaction *volumeHotplugTransaction) close() error {
	return errors.Join(unix.Close(transaction.journal), unix.Close(transaction.directory))
}

func (record volumeHotplugRecord) backingBinding(instance instanceRecord) protectedBackingBinding {
	return protectedBackingBinding{
		kind: "volume", logicalID: record.Volume.ID, ownerUID: instance.ownerUID,
		cacheIdentity: instance.launch.cacheIdentity, bytes: record.Volume.ExpectedBytes,
		identity: managedVMConfigStat{
			deviceMajor: record.SourceMajor,
			deviceMinor: record.SourceMinor,
			inode:       record.SourceInode,
			uid:         record.SourceUID,
			gid:         record.SourceGID,
			mode:        record.SourceMode,
			nlink:       1,
			size:        record.Volume.ExpectedBytes,
		},
	}
}
