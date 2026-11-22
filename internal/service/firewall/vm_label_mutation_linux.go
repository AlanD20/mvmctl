package firewall

import (
	"context"
	"mvmctl/internal/lib/model"
	"slices"
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
	return mutateVMLabelMetadata(ctx, ownerUID, vmID, vmLabelMetadataChange{expected: expected, labels: labels})
}

// RetireVMLabels records removal before protected backing is destroyed. Retrying removal
// preserves the same tombstone, including when no labels were ever enrolled.
func RetireVMLabels(ctx context.Context, ownerUID uint32, vmID string) error {
	_, err := mutateVMLabelMetadata(ctx, ownerUID, vmID, vmLabelMetadataChange{retire: true})
	return err
}

type vmLabelMetadataChange struct {
	expected uint64
	labels   model.VMLabels
	retire   bool
}

func mutateVMLabelMetadata(
	ctx context.Context, ownerUID uint32, vmID string, change vmLabelMetadataChange,
) (uint64, error) {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return 0, err
	}
	var revision uint64
	err := withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		if err := mutation.recoverExecPolicyIndex(ctx, ownerUID); err != nil {
			return err
		}
		var err error
		revision, err = mutation.publishVMLabels(ctx, ownerUID, vmID, change)
		return err
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

func (mutation *logicalPolicyMutation) publishVMLabels(
	ctx context.Context, ownerUID uint32, vmID string, change vmLabelMetadataChange,
) (uint64, error) {
	target := cloneIntent(mutation.before)
	if target.VMLabels == nil {
		target.VMLabels = make(vmLabelRecords)
	}
	next, err := change.apply(target.VMLabels, ownerUID, vmID)
	if err != nil {
		return 0, err
	}
	if change.retire {
		target.removeLabelTrafficForVM(ownerUID, vmID)
		target.ExecPolicies = slices.DeleteFunc(target.ExecPolicies, func(policy ExecPolicyIntent) bool {
			return policy.OwnerUID == ownerUID && (policy.Source.VMID == vmID || policy.Destination.VMID == vmID)
		})
	}
	if err := mutation.publish(ctx, target, ownerUID); err != nil {
		return 0, err
	}
	return next.Revision, nil
}

func (change vmLabelMetadataChange) apply(records vmLabelRecords, ownerUID uint32, vmID string) (vmLabelRecord, error) {
	if !change.retire {
		if change.expected == 0 {
			return records.enroll(ownerUID, vmID, change.labels)
		}
		return records.replace(ownerUID, vmID, change.expected, change.labels)
	}
	record, found := records[vmID]
	if !found {
		record = vmLabelRecord{OwnerUID: ownerUID, Revision: 1, Labels: make(model.VMLabels), Retired: true}
		records[vmID] = record
		return record, nil
	}
	if record.OwnerUID != ownerUID {
		return vmLabelRecord{}, invalidGeneration("VM label retirement owner differs")
	}
	if !record.Retired {
		if err := records.retire(ownerUID, vmID, record.Revision); err != nil {
			return vmLabelRecord{}, err
		}
	}
	return records[vmID], nil
}
