package jailer

import (
	"context"
	"errors"
	"sync"
)

// VMLabelOwnership retains the protected logical rootfs lock for one VM's label change.
// VM execution holds instance locks. Label changes need only the durable backing identity.
type VMLabelOwnership struct {
	state *vmLabelOwnershipState
}

type vmLabelOwnershipState struct {
	mu       sync.Mutex
	cache    *managedCacheLease
	retained []int
	ownerUID uint32
	vmID     string
	released bool
	closeErr error
}

// AcquireVMLabelOwnership verifies existing root metadata without importing backing or modifying guest files.
func AcquireVMLabelOwnership(
	ctx context.Context, uid, gid uint32, vmID, cacheRoot string,
) (_ *VMLabelOwnership, returnErr error) {
	caller := instanceCaller{uid: uid, gid: gid}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("invalid VM label identity")
	}
	cache, err := pinSnapshotCache(ctx, caller, cacheRoot)
	if err != nil {
		return nil, err
	}
	state := &vmLabelOwnershipState{cache: cache, ownerUID: uid, vmID: vmID}
	lease := &VMLabelOwnership{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	anchor, retained, err := openProtectedLogical(ctx, uid, cache.identity, protectedBackingRootfs, vmID, false)
	if err != nil {
		return nil, err
	}
	state.retained = retained
	logicalFD := retained[len(retained)-2]
	var logical protectedLogicalRecord
	if err := readProtectedRecord(ctx, logicalFD, "logical.json", &logical); err != nil {
		return nil, err
	}
	if !logical.Initialized {
		return nil, instanceValidationError("VM label ownership requires an initialized protected backing")
	}
	kindFD, err := openProtectedDirectory(ctx, anchor, protectedBackingRootfs, false, true)
	if err != nil {
		return nil, err
	}
	state.retained = append(state.retained, kindFD)
	metadataFD, err := openProtectedDirectory(ctx, kindFD, vmID, false, true)
	if err != nil {
		return nil, err
	}
	state.retained = append(state.retained, metadataFD)
	var backing protectedBackingRecord
	if err := readProtectedRecord(ctx, metadataFD, "record.json", &backing); err != nil {
		return nil, err
	}
	if err := validateVMLabelBacking(uid, vmID, protectedCacheIdentity(cache.identity), backing); err != nil {
		return nil, err
	}
	return lease, nil
}

func validateVMLabelBacking(
	uid uint32, vmID string, cache protectedBackingCache, backing protectedBackingRecord,
) error {
	if backing.Version != 1 || backing.Owner != uid || backing.ID != vmID ||
		backing.Kind != protectedBackingRootfs || backing.Cache != cache || backing.Phase != "ready" {
		return instanceValidationError("VM label ownership differs from the ready protected backing")
	}
	return nil
}

// Binding returns the proven identity. The caller retains the lease through publication.
func (lease *VMLabelOwnership) Binding(ctx context.Context) (uint32, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if lease == nil || lease.state == nil {
		return 0, "", instanceValidationError("VM label ownership lease is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return 0, "", instanceValidationError("VM label ownership lease is released")
	}
	return state.ownerUID, state.vmID, nil
}

// Release closes descriptors and releases the logical backing lock.
func (lease *VMLabelOwnership) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.released {
		state.closeErr = errors.Join(closeProtectedFDs(state.retained), state.cache.Release(context.WithoutCancel(ctx)))
		state.released = true
		state.retained = nil
		state.cache = nil
	}
	return state.closeErr
}
