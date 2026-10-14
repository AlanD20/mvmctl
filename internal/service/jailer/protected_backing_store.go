package jailer

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func protectBacking(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool,
) (_ *protectedBackingLease, returnErr error) {
	return protectBackingWithKernelImport(ctx, cache, kind, id, expectedBytes, create, -1, false)
}

func protectBackingFromSource(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool, snapshotFD int,
) (_ *protectedBackingLease, returnErr error) {
	return protectBackingWithKernelImport(ctx, cache, kind, id, expectedBytes, create, snapshotFD, false)
}

func protectBackingWithKernelImport(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool,
	snapshotFD int, explicitKernelImport bool,
) (_ *protectedBackingLease, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	length := protectedKindIDLength(kind)
	if length == 0 {
		return nil, managedVMResourceValidationError("protected backing kind is invalid")
	}
	readOnlyArtifact := kind == protectedBackingKernel || kind == protectedBackingCloudInit
	if !isCanonicalManagedResourceID(id, length) || (expectedBytes == 0 && create && !readOnlyArtifact) ||
		expectedBytes > math.MaxInt64 {
		return nil, managedVMResourceValidationError("protected backing identity or length is invalid")
	}
	anchorFD, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, create)
	if err != nil {
		return nil, err
	}
	return protectBackingInAnchor(
		ctx,
		cache,
		kind,
		id,
		expectedBytes,
		create,
		snapshotFD,
		explicitKernelImport,
		anchorFD,
		retained,
	)
}

// The root anchor and owner descriptors are already admitted. Reject existing store layouts
// before publishing a previously unseen permanent global logical binding.
func protectBackingInAnchor(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64, create bool,
	snapshotFD int, explicitKernelImport bool, anchorFD int, retained []int,
) (_ *protectedBackingLease, returnErr error) {
	admission, _, err := tryAdmitProtectedBackingInAnchor(ctx, cache, kind, id, create, anchorFD, retained)
	if err != nil {
		return nil, err
	}
	return admission.protect(ctx, expectedBytes, create, snapshotFD, explicitKernelImport)
}

// protectedBackingAdmission owns only admitted store, logical and object authority.
// No data object is created or copied until the complete restore set is held.
type protectedBackingAdmission struct {
	// mu serializes consuming admission and cached descriptor release.
	mu                                     sync.Mutex
	cache                                  *managedCacheLease
	kind, id                               string
	logicalFD, storeFD, metadataFD, lockFD int
	ownerFD                                int
	retained                               []int
	released                               bool
	releaseErr                             error
}

func tryAdmitProtectedBackingInAnchor(
	ctx context.Context, cache *managedCacheLease, kind, id string, create bool, anchorFD int, retained []int,
) (_ *protectedBackingAdmission, contended bool, returnErr error) {
	ownerFD := retained[len(retained)-2]
	defer func() {
		if returnErr != nil {
			closeErr := closeProtectedFDs(retained)
			contended = contended && closeErr == nil
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	use, err := acquireProtectedStoreUse(ctx, anchorFD, false)
	if err != nil {
		return nil, restoreProtectedLockContention(err), err
	}
	retained = append(retained, use)
	cacheLock, err := acquireProtectedLock(ctx, anchorFD)
	if err != nil {
		return nil, restoreProtectedLockContention(err), err
	}
	admissionErr := admitProtectedStoreBeforeLogical(ctx, cache, anchorFD, create)
	if err := errors.Join(admissionErr, unix.Close(cacheLock)); err != nil {
		return nil, false, err
	}
	// Never take the logical lock while holding the cache publication lock. The retained
	// shared store-use authority prevents retirement between admission and publication.
	logicalFD, logical, busy, err := tryLockProtectedLogicalInAnchor(
		ctx, cache.ownerUID, cache.identity, kind, id, create, ownerFD,
	)
	if err != nil {
		return nil, busy, err
	}
	retained = append(retained, logical...)
	cacheLock, err = acquireProtectedLock(ctx, anchorFD)
	if err != nil {
		return nil, restoreProtectedLockContention(err), err
	}
	storeFD, storeErr := pinProtectedStore(ctx, cache, anchorFD, create)
	lockErr := unix.Close(cacheLock)
	if storeErr != nil || lockErr != nil {
		if storeFD >= 0 {
			retained = append(retained, storeFD)
		}
		return nil, false, errors.Join(storeErr, lockErr)
	}
	retained = append(retained, storeFD)
	metadataKind, err := openProtectedDirectory(ctx, anchorFD, kind, create, true)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, metadataKind)
	metadata, err := openProtectedDirectory(ctx, metadataKind, id, create, true)
	if err != nil {
		return nil, false, err
	}
	retained = append(retained, metadata)
	lock, err := acquireProtectedLock(ctx, metadata)
	if err != nil {
		return nil, restoreProtectedLockContention(err), err
	}
	retained = append(retained, lock)
	return &protectedBackingAdmission{cache: cache, kind: kind, id: id, logicalFD: logicalFD,
		storeFD: storeFD, metadataFD: metadata, lockFD: lock, ownerFD: ownerFD, retained: retained}, false, nil
}

func (admission *protectedBackingAdmission) Release(ctx context.Context) error {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if !admission.released {
		admission.releaseErr = closeProtectedFDs(admission.retained)
		admission.retained = nil
		admission.released = true
	}
	return admission.releaseErr
}

func (admission *protectedBackingAdmission) protect(
	ctx context.Context, expectedBytes uint64, create bool, snapshotFD int, explicitKernelImport bool,
) (_ *protectedBackingLease, returnErr error) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.released {
		return nil, managedVMResourceValidationError("protected backing admission is already consumed")
	}
	cache, kind, id := admission.cache, admission.kind, admission.id
	logicalFD, storeFD, metadata, lock := admission.logicalFD, admission.storeFD, admission.metadataFD, admission.lockFD
	retained := admission.retained
	var kernelLease *protectedBackingLease
	admission.retained = nil
	admission.released = true
	defer func() {
		if returnErr != nil {
			if kernelLease != nil {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				returnErr = errors.Join(returnErr, kernelLease.abortKernelImport(cleanupCtx))
				if explicitKernelImport {
					sealErr := syncProtectedCacheDirectory(cleanupCtx, logicalFD)
					logical, readErr := readProtectedKernelLogical(cleanupCtx, logicalFD, cache.ownerUID, id)
					unknown := sealErr != nil || readErr != nil || logical.Kernel.Pending != nil
					imported := !unknown && logical.Kernel.Current != nil &&
						logical.Kernel.Current.Phase == "ready" &&
						logical.Kernel.Current.Generation == kernelLease.kernelGeneration
					returnErr = errs.WrapMsg(
						errs.CodeKernelImportFailed,
						"protected kernel import failed",
						errors.Join(returnErr, sealErr, readErr),
						errs.WithEntity(id),
						errs.WithDetails(
							map[string]any{"outcome_unknown": unknown, "protected_kernel_imported": imported},
						),
					)
				}
				cancel()
			}
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	var record protectedBackingRecord
	recordErr := readProtectedRecord(ctx, metadata, "record.json", &record)
	newObject := errors.Is(recordErr, unix.ENOENT)
	var kernelLogical protectedLogicalRecord
	kernelProducer := explicitKernelImport
	var err error
	if kind == protectedBackingKernel {
		if recordErr != nil && !newObject {
			return nil, recordErr
		}
		var existing *protectedBackingRecord
		if !newObject {
			existing = &record
		}
		if snapshotFD >= 0 {
			kernelLogical, kernelProducer, err = prepareProtectedKernelSnapshot(ctx, cache, admission.ownerFD,
				logicalFD, storeFD, id, existing, snapshotFD, expectedBytes)
			if err != nil {
				return nil, err
			}
		} else if explicitKernelImport {
			var previous []int
			kernelLogical, previous, err = prepareProtectedKernelImport(ctx, cache, admission.ownerFD,
				logicalFD, storeFD, id, existing, nil)
			if err != nil {
				return nil, err
			}
			retained = append(retained, previous...)
		} else {
			kernelLogical, err = requireProtectedKernelSelection(ctx, logicalFD, cache.ownerUID, id,
				protectedCacheIdentity(cache.identity), true)
			if err != nil {
				return nil, err
			}
		}
	}
	if newObject {
		var logical protectedLogicalRecord
		if err := readProtectedRecord(ctx, logicalFD, "logical.json", &logical); err != nil {
			return nil, err
		}
		if logical.Initialized && (kind != protectedBackingKernel || !kernelProducer ||
			kernelLogical.Kernel.Pending == nil || kernelLogical.Kernel.Pending.Phase == "aborting") {
			return nil, managedVMResourceValidationError("initialized protected backing record is missing")
		}
	}
	if recordErr != nil && (!newObject || !create) {
		return nil, recordErr
	}
	if !newObject {
		if err := validateInitialVolumeRemoval(record); err != nil {
			return nil, err
		}
		if record.Version != 1 || record.Kind != kind || record.ID != id || record.Owner != cache.ownerUID ||
			record.Cache != protectedCacheIdentity(
				cache.identity,
			) {
			return nil, managedVMResourceValidationError("protected backing record conflicts with requested identity")
		}
		kernelRecovery := record.Phase == "kernel-reimport-copying" || record.Phase == "kernel-reimport-copied"
		if record.Phase == "initial-removing" ||
			((record.Phase == "removed" || record.Phase == "kernel-unprotected-removed" || kernelRecovery) &&
				(!explicitKernelImport || kind != protectedBackingKernel)) {
			return nil, managedVMResourceValidationError("protected backing requires explicit kernel import")
		}
		if record.Phase == "kernel-unprotected-removed" {
			if record.KernelDigest != "" || record.Backing.Inode != 0 || record.Generation != 0 {
				return nil, managedVMResourceValidationError("never-protected kernel retirement record conflicts")
			}
			newObject = true
		}
	}
	newKernelObject := kind == protectedBackingKernel && kernelProducer && kernelLogical.Kernel.Pending != nil &&
		(kernelLogical.Kernel.Pending.Phase == "preparing" ||
			(kernelLogical.Kernel.Pending.Phase == "copying" && (newObject ||
				(record.Phase == "removed" || record.Phase == "kernel-aborted") &&
					record.Generation != kernelLogical.Kernel.Pending.Generation)))
	dataKind, err := openProtectedDirectory(ctx, storeFD, kind, newObject || newKernelObject, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, dataKind)
	if newObject && (!newKernelObject || kernelLogical.Kernel.Pending.Directory.Inode == 0) {
		// Exclusive creation prevents an unrecorded former directory from becoming a new import.
		if err := unix.Mkdirat(dataKind, id, 0700); err != nil &&
			(!newKernelObject || !errors.Is(err, unix.EEXIST)) {
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
	if newKernelObject {
		pending := kernelLogical.Kernel.Pending
		if pending.Directory.Inode != 0 && !sameProtectedNode(pending.Directory, directoryIdentity) {
			return nil, managedVMResourceValidationError("kernel prepared directory identity changed")
		}
		leaves, err := protectedCleanupNames(ctx, directory)
		if err != nil {
			return nil, err
		}
		if len(leaves) != 0 {
			return nil, managedVMResourceValidationError("kernel preparing directory retains unexpected data")
		}
		pending.Directory = directoryIdentity
		pending.Phase = "copying"
		if err := writeProtectedRecord(ctx, logicalFD, "logical.json", kernelLogical); err != nil {
			return nil, err
		}
		// Recorded preparation authorizes a fresh record, not replacement of any retained inode.
		newObject = true
	}
	if newObject {
		if err := markProtectedLogicalInitialized(ctx, logicalFD); err != nil {
			return nil, err
		}
		record = protectedBackingRecord{
			Version: 1, Kind: kind, ID: id, Owner: cache.ownerUID,
			Cache: protectedCacheIdentity(cache.identity), Directory: directoryIdentity, Phase: "copying",
			References:     []protectedBackingReference{},
			SnapshotSource: snapshotFD >= 0,
		}
		if kind == protectedBackingKernel {
			pending := kernelLogical.Kernel.Pending
			if pending == nil {
				return nil, managedVMResourceValidationError("new kernel backing has no pending import")
			}
			record.Generation = pending.Generation
			if current := kernelLogical.Kernel.Current; current != nil {
				record.KernelDigest = current.Digest
				record.Phase = "kernel-reimport-copying"
			}
		}
		if err := writeProtectedRecord(ctx, metadata, "record.json", record); err != nil {
			return nil, err
		}
	} else if !sameProtectedNode(record.Directory, directoryIdentity) {
		return nil, managedVMResourceValidationError("protected backing directory identity changed")
	}
	lease := &protectedBackingLease{
		fd: -1, identity: managedVMConfigStat{}, ownerUID: cache.ownerUID, cacheIdentity: cache.identity,
		metadataFD: metadata, logicalFD: logicalFD, directoryFD: directory, lockFD: lock, record: record,
	}
	if kind == protectedBackingKernel {
		if pending := kernelLogical.Kernel.Pending; pending != nil {
			lease.kernelGeneration = pending.Generation
		} else if current := kernelLogical.Kernel.Current; current != nil {
			lease.kernelGeneration = current.Generation
		}
		if lease.record.Generation != lease.kernelGeneration {
			return nil, managedVMResourceValidationError("kernel object generation differs from logical authority")
		}
		kernelLease = lease
		if kernelLogical.Kernel.Pending != nil && kernelLogical.Kernel.Pending.Phase == "aborting" {
			if err := lease.abortKernelImport(ctx); err != nil {
				return nil, err
			}
			return nil, managedVMResourceValidationError("kernel interrupted abort completed; retry explicit import")
		}
	}
	if explicitKernelImport && kind == protectedBackingKernel && lease.record.Phase == "removed" {
		if err := lease.beginKernelReimport(ctx); err != nil {
			return nil, err
		}
	}
	if record.Phase == "removing" {
		if err := lease.finishRemoval(ctx); err != nil {
			return nil, err
		}
		lease.retained = retained
		retained = nil
		return lease, nil
	}
	if lease.record.Phase == "copying" || lease.record.Phase == "copied" ||
		lease.record.Phase == "kernel-reimport-copying" || lease.record.Phase == "kernel-reimport-copied" {
		if snapshotFD >= 0 && !record.SnapshotSource {
			return nil, managedVMResourceValidationError("snapshot restore cannot resume an operator-source import")
		}
		if !create {
			return nil, managedVMResourceValidationError("protected backing initial import is incomplete")
		}
		if lease.record.SnapshotSource {
			if snapshotFD < 0 {
				return nil, managedVMResourceValidationError(
					"snapshot backing import requires its original trusted source",
				)
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
	if err := lease.commitKernelGeneration(ctx); err != nil {
		return nil, err
	}
	lease.retained = retained
	retained = nil
	return lease, nil
}

func admitProtectedStoreBeforeLogical(ctx context.Context, cache *managedCacheLease, anchor int, create bool) error {
	var record protectedBackingAnchor
	err := readProtectedRecord(ctx, anchor, "store.json", &record)
	if errors.Is(err, unix.ENOENT) && create {
		// Only a genuinely fresh store may proceed to initial logical publication.
		if err := protectedCacheNameAbsent(ctx, cache.cacheFD, protectedBackingDirectory); err != nil {
			return err
		}
		return protectedCacheNameAbsent(ctx, anchor, protectedSnapshotLockDirectory)
	}
	if err != nil {
		return err
	}
	store, err := pinProtectedStore(ctx, cache, anchor, false)
	if err != nil {
		return err
	}
	return unix.Close(store)
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
			// Exclusive fresh creation refuses an unrecorded prior authority directory.
			err = unix.Mkdirat(metadata, protectedSnapshotLockDirectory, 0700)
		}
		var locks protectedBackingIdentity
		if err == nil {
			var authority int
			authority, err = openProtectedDirectory(ctx, metadata, protectedSnapshotLockDirectory, false, true)
			if err == nil {
				locks, err = protectedIdentity(authority)
				err = errors.Join(err, unix.Close(authority))
			}
		}
		if err == nil {
			anchor = protectedBackingAnchor{
				Version: protectedStoreLayoutVersion, Owner: cache.ownerUID, OwnerGID: cache.ownerGID,
				Cache: protectedCacheIdentity(cache.identity), Locator: protectedLocator(cache), Store: identity,
				SnapshotLocks: locks,
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
	if anchor.Retirement != nil {
		return -1, managedVMResourceValidationError("protected store has explicit retirement authority")
	}
	if err := validateProtectedStoreLayout(ctx, metadata, anchor); err != nil {
		return -1, err
	}
	if anchor.Owner != cache.ownerUID ||
		anchor.Cache != protectedCacheIdentity(cache.identity) ||
		anchor.OwnerGID == 0 ||
		anchor.Locator == "" {
		return -1, managedVMResourceValidationError("protected backing anchor conflicts with admitted cache")
	}
	store, err := openProtectedDirectory(ctx, cache.cacheFD, protectedBackingDirectory, false, true)
	if err != nil {
		return -1, err
	}
	identity, err := protectedIdentity(store)
	if err != nil || !sameProtectedNode(anchor.Store, identity) {
		return -1, errors.Join(
			managedVMResourceAtomicError("protected backing store identity changed", err),
			unix.Close(store),
		)
	}
	// Seal visible fresh-layout publication and the cache entry before admitting consumers.
	if err := errors.Join(
		syncProtectedCacheDirectory(ctx, metadata),
		syncProtectedCacheDirectory(ctx, cache.cacheFD),
	); err != nil {
		return -1, errors.Join(err, unix.Close(store))
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
