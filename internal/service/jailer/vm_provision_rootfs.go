package jailer

import (
	"context"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// VMProvisionRootfsLease holds VM inactivity and the authoritative backing selection until provisioning ends.
// CRITICAL: Even an absent backing retains its import lock; absence never authorizes a concurrent import.
type VMProvisionRootfsLease struct {
	*vmProvisionRootfsLeaseState
}

type vmProvisionRootfsLeaseState struct {
	// mu and close-once state are shared by copied capability handles.
	mu          sync.Mutex
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	vmLock      *vmAuthorityLease
	indexLock   *authorityLock
	cache       *managedCacheLease
	backing     *protectedBackingLease
	protected   bool
	released    bool
	releaseErr  error
}

// AcquireVMProvisionRootfs selects only this caller's inactive VM rootfs. No descriptor or resource path is accepted.
func AcquireVMProvisionRootfs(
	ctx context.Context, uid, gid uint32, vmID, cacheLocator string,
) (_ *VMProvisionRootfsLease, returnErr error) {
	caller := instanceCaller{uid: uid, gid: gid}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("invalid provisioning VM ID")
	}
	locator, err := parseManagedCacheLocator(cacheLocator)
	if err != nil {
		return nil, err
	}
	lease := &VMProvisionRootfsLease{vmProvisionRootfsLeaseState: &vmProvisionRootfsLeaseState{}}
	defer func() {
		if returnErr != nil {
			returnErr = appendInstanceOperationError(returnErr, "release rejected provisioning authority", lease.Release(ctx))
		}
	}()
	authority := newInstanceAuthority()
	lease.roots, err = openInstanceAuthorityRoots(ctx, authority.deps, authority.policy, authority.authorityTree)
	if err != nil {
		return nil, err
	}
	lease.indexLock, err = lease.roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	location, err := lease.roots.findInstance(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if location != nil && location.uid != uid {
		return nil, appendInstanceOperationError(unauthorizedInstanceError(vmID), "close foreign instance directory",
			lease.roots.deps.close(context.WithoutCancel(ctx), location.stateFD))
	}
	if location == nil {
		lease.directories, err = lease.roots.openUIDDirectories(ctx, uid)
	} else {
		lease.directories, err = lease.roots.openAuthorityForLocation(ctx, location)
	}
	if err != nil {
		return nil, err
	}
	lease.vmLock, err = lease.directories.acquireVMLock(ctx, vmID)
	if err != nil {
		return nil, err
	}
	record, found, err := lease.directories.readRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if location != nil && !found {
		return nil, instanceAtomicError("provisioning instance record disappeared", nil)
	}
	if found {
		if err := authorizeInstanceRecord(caller, record); err != nil {
			return nil, err
		}
		if record.lifecycle != instanceLifecycleCleaned {
			return nil, instanceStateError(vmID, "provisioning requires a cleaned, inactive VM")
		}
		if err := lease.indexLock.Release(context.WithoutCancel(ctx)); err != nil {
			return nil, err
		}
		lease.indexLock = nil
	}
	// A genuinely new VM keeps the global index lock: no durable record yet reserves its ID across owners.
	lease.cache, err = pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, locator)
	if err != nil {
		return nil, err
	}
	lease.backing, lease.protected, err = lease.cache.openProtectedVMRootfs(ctx, vmResourceID(vmID))
	if err != nil {
		return nil, err
	}
	if lease.backing == nil {
		return nil, instanceAtomicError("provisioning backing selection lease is missing", nil)
	}
	return lease, nil
}

// CheckCacheIdentity binds LoopMount's separately retained namespace to the authoritative backing selector.
func (lease *VMProvisionRootfsLease) CheckCacheIdentity(
	ctx context.Context, deviceMajor, deviceMinor uint32, inode, mountID uint64, uniqueMountID bool,
) error {
	if lease == nil || lease.vmProvisionRootfsLeaseState == nil {
		return instanceAtomicError("provisioning authority is missing", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("check provisioning cache", err)
	}
	if lease.released || lease.cache == nil {
		return instanceAtomicError("provisioning authority is inactive", nil)
	}
	identity := lease.cache.identity
	kind := managedCacheMountIdentityUnavailable
	if mountID != 0 {
		kind = managedCacheMountIdentityLegacy
		if uniqueMountID {
			kind = managedCacheMountIdentityUnique
		}
	}
	if identity.deviceMajor != deviceMajor || identity.deviceMinor != deviceMinor || identity.inode != inode ||
		identity.mountID != mountID || identity.mountIDKind != kind {
		return instanceAtomicError("provisioning cache identity changed", nil)
	}
	return nil
}

// DuplicateRootfsForMount returns only the selected protected VM disk, or nil for authoritative first-import absence.
// The caller owns this duplicate and must close it after loop detach, before releasing this authority lease.
func (lease *VMProvisionRootfsLease) DuplicateRootfsForMount(ctx context.Context) (*os.File, error) {
	if lease == nil || lease.vmProvisionRootfsLeaseState == nil {
		return nil, instanceAtomicError("provisioning authority is missing", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("borrow provisioning rootfs", err)
	}
	if lease.released || lease.backing == nil {
		return nil, instanceAtomicError("provisioning authority is inactive", nil)
	}
	if !lease.protected {
		return nil, nil
	}
	lease.backing.mu.Lock()
	defer lease.backing.mu.Unlock()
	if err := lease.backing.active(ctx); err != nil {
		return nil, err
	}
	if len(lease.backing.record.References) != 0 {
		return nil, instanceAtomicError("protected provisioning rootfs retains VM access", nil)
	}
	identity, err := protectedIdentity(lease.backing.fd)
	if err != nil {
		return nil, err
	}
	if identity != lease.backing.record.Backing {
		return nil, instanceAtomicError("protected provisioning rootfs identity differs from root record", nil)
	}
	fd, err := unix.FcntlInt(uintptr(lease.backing.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, instanceAtomicError("duplicate protected provisioning rootfs", err)
	}
	return os.NewFile(uintptr(fd), "protected-vm-provision-rootfs"), nil
}

// GrowRootfs journals physical growth on the root-owned backing; LoopMount owns subsequent filesystem growth.
func (lease *VMProvisionRootfsLease) GrowRootfs(ctx context.Context, target uint64) error {
	if lease == nil || lease.vmProvisionRootfsLeaseState == nil {
		return instanceAtomicError("provisioning authority is missing", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || !lease.protected || lease.backing == nil {
		return instanceAtomicError("protected provisioning authority is inactive", nil)
	}
	return lease.backing.Grow(ctx, target)
}

// Release drops backing selection only after the consumer has detached and closed its mount duplicate.
func (lease *VMProvisionRootfsLease) Release(ctx context.Context) error {
	if lease == nil || lease.vmProvisionRootfsLeaseState == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return lease.releaseErr
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	if lease.backing != nil {
		result = appendInstanceOperationError(result, "release provisioning backing", lease.backing.Release(cleanupCtx))
	}
	if lease.cache != nil {
		result = appendInstanceOperationError(result, "release provisioning cache", lease.cache.Release(cleanupCtx))
	}
	result = releaseInstanceOperationResources(cleanupCtx, result, lease.vmLock, lease.indexLock, nil,
		lease.directories, lease.roots)
	lease.backing, lease.cache, lease.vmLock, lease.indexLock = nil, nil, nil, nil
	lease.directories, lease.roots = nil, nil
	lease.released, lease.releaseErr = true, result
	return result
}
