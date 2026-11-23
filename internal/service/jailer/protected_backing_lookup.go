package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

func (cache *managedCacheLease) openProtectedVMRootfs(
	ctx context.Context, vmID vmResourceID,
) (*protectedBackingLease, bool, error) {
	return lookupProtectedBacking(ctx, cache, protectedBackingRootfs, string(vmID))
}

func (cache *managedCacheLease) openProtectedVMVolume(
	ctx context.Context, volumeID vmVolumeResourceID,
) (*protectedBackingLease, bool, error) {
	return lookupProtectedBacking(ctx, cache, protectedBackingVolume, string(volumeID))
}

// lookupProtectedBacking retains the same import lock even for authoritative absence. Normal
// initial-file operations may then run without racing the first copy into protected storage.
func lookupProtectedBacking(
	ctx context.Context, cache *managedCacheLease, kind, id string,
) (_ *protectedBackingLease, _ bool, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, false, err
	}
	length := protectedKindIDLength(kind)
	if length == 0 {
		return nil, false, managedVMResourceValidationError("protected backing lookup kind is invalid")
	}
	if !isCanonicalManagedResourceID(id, length) {
		return nil, false, managedVMResourceValidationError("protected backing lookup identity is invalid")
	}
	anchor, retained, err := openProtectedLogical(ctx, cache.ownerUID, cache.identity, kind, id, true)
	if err != nil {
		return nil, false, err
	}
	logicalFD := retained[len(retained)-2]
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	use, err := acquireProtectedStoreUse(ctx, anchor, false)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, use)
	var kernelLogical protectedLogicalRecord
	if kind == protectedBackingKernel {
		kernelLogical, err = requireProtectedKernelSelection(ctx, logicalFD, cache.ownerUID, id,
			protectedCacheIdentity(cache.identity), false)
		if err != nil {
			return nil, false, err
		}
	}
	store, err := pinProtectedStore(ctx, cache, anchor, false)
	if errors.Is(err, unix.ENOENT) {
		var anchorRecord protectedBackingAnchor
		anchorErr := readProtectedRecord(ctx, anchor, "store.json", &anchorRecord)
		if anchorErr == nil || !errors.Is(anchorErr, unix.ENOENT) {
			return nil, false, managedVMResourceAtomicError("recorded protected store is missing", err)
		}
		var stat unix.Stat_t
		if statErr := unix.Fstatat(
			cache.cacheFD,
			protectedBackingDirectory,
			&stat,
			unix.AT_SYMLINK_NOFOLLOW,
		); !errors.Is(
			statErr,
			unix.ENOENT,
		) {
			return nil, false, managedVMResourceAtomicError(
				"unanchored protected store prevents initial-file fallback",
				statErr,
			)
		}
		store = -1
	} else if err != nil {
		return nil, false, err
	}
	if store >= 0 {
		retained = append(retained, store)
	}
	metadataKind, err := openProtectedDirectory(ctx, anchor, kind, true, true)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, metadataKind)
	metadata, err := openProtectedDirectory(ctx, metadataKind, id, true, true)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, metadata)
	lock, err := acquireProtectedLock(ctx, metadata)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, lock)
	var record protectedBackingRecord
	err = readProtectedRecord(ctx, metadata, "record.json", &record)
	if errors.Is(err, unix.ENOENT) {
		var logical protectedLogicalRecord
		if err := readProtectedRecord(ctx, logicalFD, "logical.json", &logical); err != nil {
			return nil, true, err
		}
		if logical.Initialized {
			return nil, true, managedVMResourceValidationError("initialized protected backing record is missing")
		}
		if store >= 0 {
			kindFD, openErr := openProtectedDirectory(ctx, store, kind, false, true)
			if openErr == nil {
				var stat unix.Stat_t
				statErr := unix.Fstatat(kindFD, id, &stat, unix.AT_SYMLINK_NOFOLLOW)
				closeErr := unix.Close(kindFD)
				if !errors.Is(statErr, unix.ENOENT) || closeErr != nil {
					return nil, false, managedVMResourceAtomicError(
						"unrecorded protected object prevents source fallback",
						errors.Join(statErr, closeErr),
					)
				}
			} else if !errors.Is(openErr, unix.ENOENT) {
				return nil, false, openErr
			}
		}
		record = protectedBackingRecord{
			Version: 1, Kind: kind, ID: id, Owner: cache.ownerUID, Cache: protectedCacheIdentity(cache.identity),
			Phase: "absent", References: []protectedBackingReference{},
		}
		return &protectedBackingLease{
			fd: -1, ownerUID: cache.ownerUID, cacheIdentity: cache.identity, metadataFD: metadata, logicalFD: logicalFD,
			directoryFD: -1, lockFD: lock, record: record, retained: retained,
		}, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if record.Version != 1 || record.Kind != kind || record.ID != id || record.Owner != cache.ownerUID ||
		record.Cache != protectedCacheIdentity(cache.identity) {
		return nil, true, managedVMResourceValidationError("protected lookup record identity conflicts")
	}
	if err := validateInitialVolumeRemoval(record); err != nil {
		return nil, true, err
	}
	if record.Phase == "removed" || record.Phase == "initial-removing" || record.Phase == "kernel-unprotected-removed" {
		if len(record.References) != 0 || len(record.NextReferences) != 0 {
			return nil, true, managedVMResourceValidationError("protected removal tombstone retains references")
		}
		lease := &protectedBackingLease{
			fd: -1, ownerUID: cache.ownerUID, cacheIdentity: cache.identity, metadataFD: metadata, logicalFD: logicalFD,
			directoryFD: -1, lockFD: lock, record: record, retained: retained,
		}
		if kind == protectedBackingKernel && kernelLogical.Kernel.Current != nil {
			lease.kernelGeneration = kernelLogical.Kernel.Current.Generation
		}
		return lease, true, nil
	}
	if record.Phase == "copying" || record.Phase == "copied" || store < 0 {
		return nil, true, managedVMResourceValidationError(
			"protected import is incomplete; initial-source fallback is forbidden",
		)
	}
	if err := closeProtectedFDs(retained); err != nil {
		retained = nil
		return nil, true, err
	}
	retained = nil
	lease, err := protectBacking(ctx, cache, kind, id, 0, false)
	return lease, true, err
}

func (lease *protectedBackingLease) IsRemoved(ctx context.Context) (bool, error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if lease.released || lease.lockFD < 0 {
		return false, managedVMResourceValidationError("protected lookup lease is closed")
	}
	return lease.record.Phase == "removed" || lease.record.Phase == "kernel-unprotected-removed", nil
}

// CommitInitialVolumeRemoval follows the consumer's exact initial raw-file unlink and directory
// sync while this same import lock remains held. The permanent tombstone prevents resurrection.
func (lease *protectedBackingLease) CommitInitialVolumeRemoval(ctx context.Context, sourceAbsent bool) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.released || lease.lockFD < 0 || lease.record.Kind != protectedBackingVolume ||
		lease.record.Phase != "initial-removing" || lease.record.InitialRemoval == nil ||
		lease.record.InitialRemoval.Quarantine.Inode == 0 ||
		(!sourceAbsent && lease.record.InitialRemoval.Phase != "captured") ||
		(sourceAbsent && lease.record.InitialRemoval.Receipt != nil) {
		return managedVMResourceValidationError("initial volume removal does not hold an absent protected import lease")
	}
	lease.record.Phase = "removed"
	lease.record.InitialRemoval.Phase = "removed"
	lease.record.InitialRemoval.SourceAbsent = sourceAbsent
	if err := markProtectedLogicalInitialized(ctx, lease.logicalFD); err != nil {
		return err
	}
	return writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record)
}

func (lease *protectedBackingLease) BeginInitialVolumeRemoval(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	protectedSource := lease.record.Phase == "removed" && lease.record.Source.Inode != 0 &&
		!lease.record.SnapshotSource
	if lease.released || lease.lockFD < 0 || lease.record.Kind != protectedBackingVolume ||
		(lease.record.Phase != "absent" && lease.record.Phase != "initial-removing" && !protectedSource) {
		return managedVMResourceValidationError("initial volume removal does not hold an admitted source lease")
	}
	if lease.record.Phase == "initial-removing" {
		return validateInitialVolumeRemoval(lease.record)
	}
	if lease.record.InitialRemoval != nil {
		return managedVMResourceValidationError("initial volume removal cannot restart recorded authority")
	}
	lease.record.Phase = "initial-removing"
	lease.record.InitialRemoval = &initialVolumeRemovalProgress{Phase: "preparing"}
	if err := markProtectedLogicalInitialized(ctx, lease.logicalFD); err != nil {
		return err
	}
	return writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record)
}

func (lease *protectedBackingLease) IsInitialVolumeRemovalPending(ctx context.Context) (bool, error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if lease.released || lease.lockFD < 0 {
		return false, managedVMResourceValidationError("initial removal lookup lease is closed")
	}
	return lease.record.Phase == "initial-removing", nil
}
