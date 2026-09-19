package jailer

import (
	"context"
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

func protectBacking(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool,
) (_ *protectedBackingLease, returnErr error) {
	return protectBackingFromSource(ctx, cache, kind, id, expectedBytes, create, -1)
}

func protectBackingFromSource(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool, snapshotFD int,
) (_ *protectedBackingLease, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	length := protectedKindIDLength(kind)
	if length == 0 {
		return nil, managedVMResourceValidationError("protected backing kind is invalid")
	}
	readOnlyArtifact := kind == protectedBackingKernel || kind == protectedBackingCloudInit
	if !isCanonicalManagedResourceID(id, length) || (expectedBytes == 0 && create && !readOnlyArtifact) || expectedBytes > math.MaxInt64 {
		return nil, managedVMResourceValidationError("protected backing identity or length is invalid")
	}
	anchorFD, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, create)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	cacheLock, err := acquireProtectedLock(ctx, anchorFD)
	if err != nil {
		return nil, err
	}
	storeFD, storeErr := pinProtectedStore(ctx, cache, anchorFD, create)
	lockErr := unix.Close(cacheLock)
	if storeErr != nil || lockErr != nil {
		if storeFD >= 0 {
			retained = append(retained, storeFD)
		}
		return nil, errors.Join(storeErr, lockErr)
	}
	retained = append(retained, storeFD)
	metadataKind, err := openProtectedDirectory(ctx, anchorFD, kind, create, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, metadataKind)
	metadata, err := openProtectedDirectory(ctx, metadataKind, id, create, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, metadata)
	lock, err := acquireProtectedLock(ctx, metadata)
	if err != nil {
		return nil, err
	}
	retained = append(retained, lock)
	var record protectedBackingRecord
	recordErr := readProtectedRecord(ctx, metadata, "record.json", &record)
	newObject := errors.Is(recordErr, unix.ENOENT)
	if recordErr != nil && (!newObject || !create) {
		return nil, recordErr
	}
	if !newObject {
		if record.Version != 1 || record.Kind != kind || record.ID != id || record.Owner != cache.ownerUID ||
			record.Cache != protectedCacheIdentity(cache.identity) || record.Phase == "removed" || record.Phase == "initial-removing" {
			return nil, managedVMResourceValidationError("protected backing record conflicts with requested identity")
		}
	}
	dataKind, err := openProtectedDirectory(ctx, storeFD, kind, newObject, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, dataKind)
	if newObject {
		// Exclusive creation prevents an unrecorded former directory from becoming a new import.
		if err := unix.Mkdirat(dataKind, id, 0700); err != nil {
			return nil, managedVMResourceAtomicError("create exclusive protected backing object", err)
		}
		if err := unix.Fsync(dataKind); err != nil {
			return nil, managedVMResourceAtomicError("sync protected backing object parent", err)
		}
	}
	directory, err := openProtectedDirectory(ctx, dataKind, id, false, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, directory)
	directoryIdentity, err := protectedIdentity(directory)
	if err != nil {
		return nil, err
	}
	if newObject {
		record = protectedBackingRecord{
			Version: 1, Kind: kind, ID: id, Owner: cache.ownerUID,
			Cache: protectedCacheIdentity(cache.identity), Directory: directoryIdentity, Phase: "copying",
			References:     []protectedBackingReference{},
			SnapshotSource: snapshotFD >= 0,
		}
		if err := writeProtectedRecord(ctx, metadata, "record.json", record); err != nil {
			return nil, err
		}
	} else if !sameProtectedNode(record.Directory, directoryIdentity) {
		return nil, managedVMResourceValidationError("protected backing directory identity changed")
	}
	lease := &protectedBackingLease{
		fd: -1, identity: managedVMConfigStat{}, ownerUID: cache.ownerUID, cacheIdentity: cache.identity,
		metadataFD: metadata, directoryFD: directory, lockFD: lock, record: record,
	}
	if record.Phase == "removing" {
		if err := lease.finishRemoval(ctx); err != nil {
			return nil, err
		}
		lease.retained = retained
		retained = nil
		return lease, nil
	}
	if record.Phase == "copying" || record.Phase == "copied" {
		if !create {
			return nil, managedVMResourceValidationError("protected backing initial import is incomplete")
		}
		if lease.record.SnapshotSource {
			if snapshotFD < 0 {
				return nil, managedVMResourceValidationError("snapshot backing import requires its original trusted source")
			}
			if lease.record.Phase == "copied" {
				err = lease.publishImport(ctx)
			} else {
				err = lease.copyAndPublish(ctx, snapshotFD, expectedBytes)
			}
		} else {
			err = lease.completeImport(ctx, cache, expectedBytes)
		}
		if err != nil {
			return nil, err
		}
	}
	fd, err := openProtectedFile(ctx, directory, "disk", unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	retained = append(retained, fd)
	lease.fd = fd
	if lease.record.Phase == "acl" {
		if err := lease.finishACL(ctx); err != nil {
			return nil, err
		}
	}
	if err := lease.admitDisk(ctx); err != nil {
		return nil, err
	}
	if lease.record.Phase == "growing" {
		if err := lease.finishGrow(ctx); err != nil {
			return nil, err
		}
	}
	if lease.record.Phase != "ready" || (expectedBytes != 0 && uint64(lease.record.Backing.Size) != expectedBytes) {
		return nil, managedVMResourceValidationError("protected backing size or phase differs from requested state")
	}
	lease.retained = retained
	retained = nil
	return lease, nil
}

func pinProtectedStore(
	ctx context.Context, cache *managedCacheLease, metadata int, create bool,
) (int, error) {
	var anchor protectedBackingAnchor
	err := readProtectedRecord(ctx, metadata, "store.json", &anchor)
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(cache.cacheFD, protectedBackingDirectory, 0700); err != nil {
			return -1, managedVMResourceAtomicError("create exclusive protected backing store", err)
		}
		store, err := openProtectedDirectory(ctx, cache.cacheFD, protectedBackingDirectory, false, true)
		if err != nil {
			return -1, err
		}
		identity, err := protectedIdentity(store)
		if err == nil {
			anchor = protectedBackingAnchor{
				Version: 1, Owner: cache.ownerUID, OwnerGID: cache.ownerGID,
				Cache: protectedCacheIdentity(cache.identity), Locator: protectedLocator(cache), Store: identity,
			}
			err = writeProtectedRecord(ctx, metadata, "store.json", anchor)
		}
		if err == nil {
			// cacheFD is O_PATH; obtain a reader through the retained directory itself for fsync.
			parent, openErr := unix.Openat2(cache.cacheFD, ".", &unix.OpenHow{
				Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: protectedBackingResolve,
			})
			if openErr == nil {
				err = errors.Join(unix.Fsync(parent), unix.Close(parent))
			} else {
				err = openErr
			}
		}
		if err != nil {
			return -1, errors.Join(err, unix.Close(store))
		}
		return store, nil
	}
	if err != nil {
		return -1, err
	}
	if anchor.Version != 1 || anchor.Owner != cache.ownerUID || anchor.Cache != protectedCacheIdentity(cache.identity) ||
		anchor.OwnerGID == 0 || anchor.Locator == "" {
		return -1, managedVMResourceValidationError("protected backing anchor conflicts with admitted cache")
	}
	store, err := openProtectedDirectory(ctx, cache.cacheFD, protectedBackingDirectory, false, true)
	if err != nil {
		return -1, err
	}
	identity, err := protectedIdentity(store)
	if err != nil || !sameProtectedNode(anchor.Store, identity) {
		return -1, errors.Join(managedVMResourceAtomicError("protected backing store identity changed", err), unix.Close(store))
	}
	return store, nil
}

func (lease *protectedBackingLease) admitDisk(ctx context.Context) error {
	identity, err := protectedIdentity(lease.fd)
	if err != nil || !sameProtectedNode(identity, lease.record.Backing) {
		return managedVMResourceAtomicError("protected backing inode changed", err)
	}
	if identity.Size != lease.record.Backing.Size &&
		(lease.record.Phase != "growing" || uint64(identity.Size) != lease.record.GrowBytes) {
		return managedVMResourceValidationError("protected backing length changed outside its grow transaction")
	}
	if err := verifyProtectedACL(ctx, lease.fd, lease.record.References); err != nil {
		return err
	}
	lease.identity, err = stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), lease.fd)
	return err
}
