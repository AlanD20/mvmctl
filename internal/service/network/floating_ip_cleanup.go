package network

import (
	"context"
	"errors"
)

// recoverFloatingIPOwnership is root-authorized reconciliation, never an inspection side effect.
// It fences every possibly installed historical target; it never resumes timed installation.
func recoverFloatingIPOwnership(
	ctx context.Context, tx *managedNetworkTransaction, store *floatingIPStore, record floatingIPRecord,
) (floatingIPRecord, error) {
	if record.Retired {
		return record, networkAuthorityValidationError("retired floating IP cannot recover ownership")
	}
	if record.Phase == floatingIPUnassociated {
		if err := tx.firewall.FenceFloatingIP(ctx, floatingIPFirewallBinding(record, floatingIPOwner{})); err != nil {
			return record, err
		}
		return record, tx.invalidateFloatingIPNetworkState(ctx, record)
	}
	var err error
	record, err = beginFloatingIPRecovery(ctx, store, record)
	if err != nil {
		return record, err
	}
	return fenceFloatingIPOwnership(ctx, tx, store, record)
}

// cleanupFloatingIPVM fences only references to this exact root-admitted incarnation before endpoint removal.
// It borrows the already retained network/global authority and never takes an index or another VM lock.
func cleanupFloatingIPVM(
	ctx context.Context, tx *managedNetworkTransaction, vmID, bootID string, launch uint64, executionUID uint32,
) (returnErr error) {
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store != nil {
		records, err := store.list(ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Retired || record.NetworkID != string(tx.key.networkID) {
				continue
			}
			matches := func(owner floatingIPOwner) bool {
				return owner.VMID == vmID && owner.BootID == bootID && owner.LaunchGeneration == launch &&
					owner.ExecutionUID == executionUID
			}
			if matches(record.Owner) || matches(record.BeforeOwner) || matches(record.TargetOwner) {
				if _, err := recoverFloatingIPOwnership(ctx, tx, store, record); err != nil {
					return err
				}
			}
		}
	}
	return retireFloatingIPVMDelegations(ctx, tx, vmID, bootID, launch, executionUID)
}

func retireFloatingIPVMDelegations(
	ctx context.Context, tx *managedNetworkTransaction, vmID, bootID string, launch uint64, executionUID uint32,
) (returnErr error) {
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return nil
	}
	grants, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.NetworkID == string(tx.key.networkID) && grant.VMID == vmID && grant.BootID == bootID &&
			grant.LaunchGeneration == launch && grant.ExecutionUID == executionUID &&
			grant.State != floatingIPDelegationRevoked {
			if err := revokeFloatingIPDelegation(ctx, tx, store, grant); err != nil {
				return err
			}
		}
	}
	return nil
}

// revokeFloatingIPDelegation first publishes non-admission, then fences only exact sustaining provenance.
func revokeFloatingIPDelegation(
	ctx context.Context, tx *managedNetworkTransaction, store *floatingIPDelegationStore, grant floatingIPDelegationRecord,
) (returnErr error) {
	if grant.State == floatingIPDelegationRevoked {
		return nil
	}
	if grant.State == floatingIPDelegationActive {
		next := grant
		next.Revision++
		next.State = floatingIPDelegationRevoking
		if err := store.persist(ctx, next, &grant); err != nil {
			return err
		}
		grant = next
	}
	resources, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, resources.release(context.WithoutCancel(ctx))) }()
	if resources == nil {
		return networkAuthorityCorruptError("delegation resource inventory is missing", nil)
	}
	resource, _, _, found, err := resources.read(ctx, grant.ResourceID)
	if err != nil {
		return err
	}
	if !found || resource.OwnerUID != grant.OwnerUID || resource.NetworkID != grant.NetworkID {
		return networkAuthorityCorruptError("delegation resource binding is missing or changed", nil)
	}
	if !resource.Retired && floatingIPRecordSustainedByDelegation(resource, grant) {
		if _, err := recoverFloatingIPOwnership(ctx, tx, resources, resource); err != nil {
			return err
		}
	}
	next := grant
	next.Revision++
	next.State = floatingIPDelegationRevoked
	return store.persist(ctx, next, &grant)
}

func retireFloatingIPResourceDelegations(
	ctx context.Context, tx *managedNetworkTransaction, resource floatingIPRecord,
) (returnErr error) {
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return nil
	}
	grants, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.ResourceID == resource.ID && grant.NetworkID == resource.NetworkID && grant.OwnerUID == resource.OwnerUID &&
			grant.State != floatingIPDelegationRevoked {
			if err := revokeFloatingIPDelegation(ctx, tx, store, grant); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireRetiredFloatingIPDelegations(ctx context.Context, owner *networkStoreUID) (returnErr error) {
	store, err := openFloatingIPDelegationStore(ctx, owner, false)
	if err != nil {
		return err
	}
	if store == nil {
		return networkAuthorityCorruptError("reset delegation inventory disappeared", nil)
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	grants, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.State != floatingIPDelegationRevoked {
			return networkAuthorityCorruptError("host reset requires revoked delegations", nil)
		}
	}
	return nil
}

func beginFloatingIPRecovery(
	ctx context.Context, store *floatingIPStore, record floatingIPRecord,
) (floatingIPRecord, error) {
	if record.Operation != floatingIPRecover || (record.Phase != floatingIPFencing && record.Phase != floatingIPFenced) {
		next := record
		next.Revision++
		next.Phase = floatingIPFencing
		next.Operation = floatingIPRecover
		if record.Phase == floatingIPOwned {
			next.Generation++
			next.BeforeOwner = record.Owner
			next.Owner = floatingIPOwner{}
		} else if record.Phase == floatingIPRenewing {
			next.Generation++
		}
		if err := store.persist(ctx, next, &record); err != nil {
			return record, err
		}
		record = next
	}
	return record, nil
}
func completeFloatingIPRecovery(
	ctx context.Context, store *floatingIPStore, record floatingIPRecord,
) (floatingIPRecord, error) {
	fenced := record
	if record.Phase != floatingIPFenced {
		var err error
		fenced, err = advanceFloatingIPPhase(ctx, store, record, floatingIPFenced)
		if err != nil {
			return record, err
		}
	}
	next := fenced
	next.Revision++
	next.Phase = floatingIPUnassociated
	next.Operation = ""
	next.Owner = floatingIPOwner{}
	next.BeforeOwner = floatingIPOwner{}
	next.TargetOwner = floatingIPOwner{}
	if err := store.persist(ctx, next, &fenced); err != nil {
		return fenced, err
	}
	return next, nil
}

func fenceFloatingIPOwnership(
	ctx context.Context, tx *managedNetworkTransaction, store *floatingIPStore, record floatingIPRecord,
) (floatingIPRecord, error) {
	if err := tx.firewall.FenceFloatingIP(ctx, floatingIPFirewallBinding(record, floatingIPOwner{})); err != nil {
		return record, err
	}
	if err := tx.invalidateFloatingIPNetworkState(ctx, record); err != nil {
		return record, err
	}
	return completeFloatingIPRecovery(ctx, store, record)
}
