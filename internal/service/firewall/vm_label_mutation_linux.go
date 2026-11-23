package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/model"
)

// UpdateVMLabels publishes one already-admitted VM's metadata under the firewall lock.
// Its caller retains protected backing authority through the returned journal revision.
// Any change to active membership is published as a verified kernel generation.
func UpdateVMLabels(
	ctx context.Context, ownerUID uint32, vmID string, expected uint64, labels model.VMLabels,
) (uint64, error) {
	if labels == nil {
		return 0, invalidGeneration("VM label update requires an explicit map")
	}
	if err := labels.Validate(); err != nil {
		return 0, err
	}
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return 0, err
	}
	var revision uint64
	err := withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		if err := mutation.recoverExecPolicyIndex(ctx, ownerUID); err != nil {
			return err
		}
		var err error
		revision, err = mutation.publishVMLabels(ctx, ownerUID, vmID, expected, labels)
		return err
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// ForgetVMLabels removes labels and exact exec grants after durable VM retirement.
// CRITICAL: The lifecycle caller retains the cleaned VM lock after retiring its
// protected backing and execution identity, which prevent re-enrollment.
func ForgetVMLabels(ctx context.Context, ownerUID uint32, vmID string) error {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return err
	}
	return withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		if err := mutation.recoverExecPolicyIndex(ctx, ownerUID); err != nil {
			return err
		}
		return mutation.forgetVMLabels(ctx, ownerUID, vmID)
	})
}

func (mutation *logicalPolicyMutation) publishVMLabels(
	ctx context.Context, ownerUID uint32, vmID string, expected uint64, labels model.VMLabels,
) (uint64, error) {
	target := cloneIntent(mutation.before)
	if target.VMLabels == nil {
		target.VMLabels = make(vmLabelRecords)
	}
	var next vmLabelRecord
	var err error
	if expected == 0 {
		next, err = target.VMLabels.enroll(ownerUID, vmID, labels)
	} else {
		next, err = target.VMLabels.replace(ownerUID, vmID, expected, labels)
	}
	if err != nil {
		return 0, err
	}
	if err := mutation.publish(ctx, target, ownerUID); err != nil {
		return 0, err
	}
	return next.Revision, nil
}

func (mutation *logicalPolicyMutation) forgetVMLabels(ctx context.Context, ownerUID uint32, vmID string) error {
	record, found := mutation.before.VMLabels[vmID]
	if !found {
		// Committed exact exec grants require a VM label record, even for an empty map.
		return nil
	}
	if record.OwnerUID != ownerUID {
		return invalidGeneration("VM label retirement owner differs")
	}
	if slices.ContainsFunc(mutation.before.Reservations, func(reservation EndpointReservation) bool {
		return reservation.VMID == vmID
	}) {
		return invalidGeneration("VM label retirement requires endpoint reservation removal")
	}
	target := cloneIntent(mutation.before)
	delete(target.VMLabels, vmID)
	target.ExecPolicies = slices.DeleteFunc(target.ExecPolicies, func(policy ExecPolicyIntent) bool {
		return policy.OwnerUID == ownerUID && (policy.Source.VMID == vmID || policy.Destination.VMID == vmID)
	})
	return mutation.publish(ctx, target, ownerUID)
}
