package jailer

import (
	"context"
	"errors"
	"slices"
)

func observeVolumeBackingSerial(
	ctx context.Context, transaction *volumeHotplugTransaction, instance instanceRecord, driveAbsent bool,
) (_ string, returnErr error) {
	lease, err := openObservedBacking(ctx, transaction.record.backingBinding(instance))
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	if err := admitVolumeDetachBacking(ctx, lease, transaction, instance, driveAbsent); err != nil {
		return "", err
	}
	return observedBackingSerial(lease), nil
}

// Only the typed mutating detach boundary completes an interrupted exact revoke.
// Admission and completion share the retained protected-object locks; another VM's
// grant cannot replace the pending intent between its check and ACL recovery.
func reopenVolumeDetachBacking(
	ctx context.Context, transaction *volumeHotplugTransaction, instance instanceRecord, driveAbsent bool,
) (_ *protectedBackingLease, returnErr error) {
	lease, err := openObservedBacking(ctx, transaction.record.backingBinding(instance))
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	if err := recoverVolumeDetachBacking(ctx, lease, transaction, instance, driveAbsent); err != nil {
		return nil, err
	}
	return lease, nil
}

func recoverVolumeDetachBacking(
	ctx context.Context, lease *protectedBackingLease, transaction *volumeHotplugTransaction,
	instance instanceRecord, driveAbsent bool,
) error {
	if err := admitVolumeDetachBacking(ctx, lease, transaction, instance, driveAbsent); err != nil {
		return err
	}
	if lease.record.Phase == "acl" {
		if err := lease.finishACL(ctx); err != nil {
			return err
		}
	}
	return nil
}

func admitVolumeDetachBacking(
	ctx context.Context, lease *protectedBackingLease, transaction *volumeHotplugTransaction,
	instance instanceRecord, driveAbsent bool,
) error {
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("admit selected volume backing", err)
	}
	identity, err := protectedIdentity(lease.fd)
	if err != nil || identity != lease.record.Backing {
		return managedVMResourceAtomicError("selected detach backing identity changed", err)
	}
	if lease.record.Phase == "ready" && len(lease.record.NextReferences) == 0 {
		return verifyProtectedACL(ctx, lease.fd, lease.record.References)
	}
	record := transaction.record
	if lease.record.Phase != "acl" || record.State != "detaching" || !driveAbsent || instance.active == nil {
		return instanceStateError(instance.vmID, "selected backing has an incomplete non-detach transition")
	}
	if err := transaction.proveTargetAbsent(ctx); err != nil {
		return err
	}
	if err := admitPendingVolumeRevoke(lease.record, record, instance.active.userIDs.effective); err != nil {
		return err
	}
	// A crash may precede or follow the ACL write. No other on-disk ACL is admitted.
	if err := verifyProtectedACL(ctx, lease.fd, lease.record.References); err == nil {
		return nil
	}
	return verifyProtectedACL(ctx, lease.fd, lease.record.NextReferences)
}

func admitPendingVolumeRevoke(
	backing protectedBackingRecord,
	attachment volumeHotplugRecord,
	executionUID uint32,
) error {
	if backing.Kind != protectedBackingVolume || backing.ID != attachment.Volume.ID || backing.GrowBytes != 0 {
		return instanceStateError(attachment.VMID, "pending ACL differs from selected volume revoke")
	}
	remaining := make([]protectedBackingReference, 0, len(backing.References))
	found := false
	for _, reference := range backing.References {
		if reference.VMID != attachment.VMID {
			remaining = append(remaining, reference)
			continue
		}
		if found || reference.ExecutionUID != executionUID ||
			reference.Writable != !attachment.Volume.ReadOnly {
			return instanceStateError(attachment.VMID, "pending revoke execution identity or access changed")
		}
		found = true
	}
	if !found || !slices.Equal(remaining, backing.NextReferences) {
		return instanceStateError(attachment.VMID, "pending ACL is not exactly the selected reference removal")
	}
	return nil
}
