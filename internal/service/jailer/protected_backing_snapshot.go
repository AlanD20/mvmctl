package jailer

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"

	"golang.org/x/sys/unix"
)

type protectedSnapshotStoreLease struct {
	// mu guards descriptor ownership and checked close across copied references.
	mu         sync.Mutex
	fd         int
	retained   []int
	released   bool
	releaseErr error
}

func openProtectedSnapshotStore(
	ctx context.Context, cache *managedCacheLease, create bool,
) (*protectedSnapshotStoreLease, error) {
	return openProtectedStoreBranch(ctx, cache, "snapshots", create)
}

func openProtectedStoreBranch(
	ctx context.Context, cache *managedCacheLease, branch string, create bool,
) (_ *protectedSnapshotStoreLease, returnErr error) {
	if branch != "snapshots" && branch != "volume-removals" {
		return nil, managedVMResourceValidationError("protected store branch is outside closed policy")
	}
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, create)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	use, err := acquireProtectedStoreUse(ctx, anchor, false)
	if err != nil {
		return nil, err
	}
	retained = append(retained, use)
	lock, err := acquireProtectedLock(ctx, anchor)
	if err != nil {
		return nil, err
	}
	store, storeErr := pinProtectedStore(ctx, cache, anchor, create)
	closeErr := unix.Close(lock)
	if store >= 0 {
		retained = append(retained, store)
	}
	if storeErr != nil || closeErr != nil {
		return nil, errors.Join(storeErr, closeErr)
	}
	fd, err := openProtectedDirectory(ctx, store, branch, create, true)
	if err != nil {
		return nil, err
	}
	return &protectedSnapshotStoreLease{fd: fd, retained: append(retained, fd)}, nil
}

func (lease *protectedSnapshotStoreLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return lease.releaseErr
	}
	lease.releaseErr = closeProtectedFDs(lease.retained)
	lease.retained = nil
	lease.fd = -1
	lease.released = true
	return lease.releaseErr
}

func reopenProtectedSnapshotStore(
	ctx context.Context, ownerUID uint32, cacheIdentity managedCacheIdentity,
) (_ *protectedSnapshotStoreLease, returnErr error) {
	cache, err := reopenProtectedCache(ctx, ownerUID, cacheIdentity)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	return openProtectedSnapshotStore(ctx, cache, false)
}

func reopenProtectedCache(
	ctx context.Context, ownerUID uint32, expected managedCacheIdentity,
) (_ *managedCacheLease, returnErr error) {
	anchor, retained, err := openProtectedAnchor(ctx, ownerUID, expected, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	var record protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchor, "store.json", &record); err != nil {
		return nil, err
	}
	if record.Retirement != nil || record.Version != 1 || record.Owner != ownerUID || record.OwnerGID == 0 ||
		record.Cache != protectedCacheIdentity(expected) {
		return nil, managedVMResourceValidationError("protected store anchor differs from recorded ownership")
	}
	locator, err := parseManagedCacheLocator(record.Locator)
	if err != nil {
		return nil, err
	}
	cache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(),
		instanceCaller{uid: ownerUID, gid: record.OwnerGID}, locator)
	if err != nil {
		return nil, err
	}
	if protectedCacheIdentity(cache.identity) != record.Cache {
		return nil, errors.Join(
			managedVMResourceValidationError("protected store cache identity changed"),
			cache.Release(ctx),
		)
	}
	return cache, nil
}

func (lease *protectedBackingLease) openInitialVolumeQuarantine(
	ctx context.Context, cache *managedCacheLease,
) (_ int, returnErr error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.lockFD < 0 || lease.record.Kind != protectedBackingVolume ||
		lease.record.Phase != "initial-removing" || cache.ownerUID != lease.ownerUID ||
		protectedCacheIdentity(cache.identity) != protectedCacheIdentity(lease.cacheIdentity) {
		return -1, managedVMResourceValidationError("initial volume quarantine requires the matching removal lease")
	}
	store, err := openProtectedStoreBranch(ctx, cache, "volume-removals", true)
	if err != nil {
		return -1, err
	}
	fd, err := openProtectedDirectory(ctx, store.fd, lease.record.ID, true, true)
	if err != nil {
		return -1, errors.Join(err, store.Release(context.WithoutCancel(ctx)))
	}
	lease.retained = append(lease.retained, store.retained...)
	lease.retained = append(lease.retained, fd)
	store.retained = nil
	store.fd = -1
	return fd, nil
}

func (cache *managedCacheLease) restoreProtectedVMRootfs(
	ctx context.Context, vmID vmResourceID, sourceFD int, size uint64,
) (*protectedBackingLease, error) {
	if size < managedVMRootfsMinBytes || size > managedVMRootfsMaxBytes {
		return nil, managedVMResourceValidationError("restored rootfs length is outside policy")
	}
	return restoreProtectedBacking(ctx, cache, protectedBackingRootfs, string(vmID), sourceFD, size)
}

func (cache *managedCacheLease) restoreProtectedVMVolume(
	ctx context.Context, spec vmVolumeResourceSpec, sourceFD int,
) (*protectedBackingLease, error) {
	if err := validateVMVolumeResourceSpec(spec); err != nil {
		return nil, err
	}
	return restoreProtectedBacking(ctx, cache, protectedBackingVolume, string(spec.id), sourceFD, spec.expectedBytes)
}

func (cache *managedCacheLease) restoreProtectedVMKernel(
	ctx context.Context, kernelID kernelResourceID, sourceFD int, size uint64,
) (*protectedBackingLease, error) {
	if size < managedVMKernelMinBytes || size > managedVMKernelMaxBytes {
		return nil, managedVMResourceValidationError("restored kernel length is outside policy")
	}
	return restoreProtectedBacking(ctx, cache, protectedBackingKernel, string(kernelID), sourceFD, size)
}

func (cache *managedCacheLease) restoreProtectedVMCloudInit(
	ctx context.Context, vmID vmResourceID, sourceFD int, size uint64,
) (*protectedBackingLease, error) {
	if size < managedVMCloudInitMinBytes || size > managedVMCloudInitMaxBytes {
		return nil, managedVMResourceValidationError("restored cloud-init length is outside policy")
	}
	return restoreProtectedBacking(ctx, cache, protectedBackingCloudInit, string(vmID), sourceFD, size)
}

func restoreProtectedBacking(
	ctx context.Context, cache *managedCacheLease, kind, id string, sourceFD int, size uint64,
) (*protectedBackingLease, error) {
	if size == 0 || size > math.MaxInt64 || sourceFD < 0 {
		return nil, managedVMResourceValidationError("snapshot backing source is incomplete")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(sourceFD, &stat); err != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&07777 != 0400 || stat.Size != int64(size) {
		return nil, managedVMResourceAtomicError("snapshot backing source is not root-owned immutable data", err)
	}
	flags, err := unix.FcntlInt(uintptr(sourceFD), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, managedVMResourceAtomicError("snapshot backing source descriptor is not read-only", err)
	}
	lease, err := protectBackingFromSource(ctx, cache, kind, id, size, true, sourceFD)
	if err != nil {
		return nil, err
	}
	sourceIdentity := protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}
	if lease.record.SnapshotSource && lease.record.Source == sourceIdentity {
		return lease, nil
	}
	if kind == protectedBackingKernel {
		if err := compareProtectedKernel(ctx, sourceFD, lease.fd, int64(size)); err == nil {
			return lease, nil
		} else {
			return nil, errors.Join(err, lease.Release(context.WithoutCancel(ctx)))
		}
	}
	return nil, errors.Join(
		managedVMResourceValidationError("restore cannot replace an unrelated existing protected backing"),
		lease.Release(context.WithoutCancel(ctx)),
	)
}

func compareProtectedKernel(ctx context.Context, source, existing int, size int64) error {
	left, right := make([]byte, 1<<20), make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(int64(len(left)), size-offset)
		n, leftErr := unix.Pread(source, left[:length], offset)
		m, rightErr := unix.Pread(existing, right[:length], offset)
		if leftErr != nil || rightErr != nil || n != int(length) || m != n || !bytes.Equal(left[:n], right[:m]) {
			return managedVMResourceAtomicError(
				"existing protected kernel differs from captured snapshot",
				errors.Join(leftErr, rightErr),
			)
		}
		offset += length
	}
	return nil
}
