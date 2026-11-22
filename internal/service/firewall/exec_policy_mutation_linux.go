package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// EnsureVMExecPolicy consumes exact endpoint identities already retained by the
// privileged caller under protected VM backing ownership.
func EnsureVMExecPolicy(
	ctx context.Context, ownerUID uint32, source, destination VMPolicyEndpoint, targetUser, claim, expected string,
) (ExecPolicyIntent, PolicyReceipt, error) {
	policy := ExecPolicyIntent{
		OwnerUID: ownerUID, Source: source, Destination: destination, TargetUser: targetUser,
		ClaimID: claim, Incarnation: crypto.UUIDV4(),
	}
	if ownerUID == 0 || !validPolicyOwnership(claim, policy.Incarnation) ||
		!validPolicyOwnership("", expected) || (expected != "" && claim == "") {
		return ExecPolicyIntent{}, PolicyReceipt{}, invalidGeneration("invalid exec policy request ownership")
	}
	var err error
	policy.ID, err = policy.identity()
	if err != nil {
		return ExecPolicyIntent{}, PolicyReceipt{}, err
	}
	err = withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		if err := mutation.recoverExecPolicyIndex(ctx, ownerUID); err != nil {
			return err
		}
		for _, existing := range mutation.before.ExecPolicies {
			if existing.ID != policy.ID {
				continue
			}
			if existing.OwnerUID != ownerUID || (expected != "" && existing.Incarnation != expected) {
				return policyOwnershipConflict()
			}
			policy = existing
			return verifyExactCommittedGeneration(ctx, mutation.kernel, mutation.compiled)
		}
		if expected != "" {
			return policyOwnershipConflict()
		}
		target := cloneIntent(mutation.before)
		if target.VMLabels == nil {
			target.VMLabels = make(vmLabelRecords)
		}
		for _, endpoint := range []VMPolicyEndpoint{source, destination} {
			if endpoint.VMID == "" {
				continue
			}
			if _, found := target.VMLabels[endpoint.VMID]; !found {
				if _, err := target.VMLabels.enroll(ownerUID, endpoint.VMID, model.VMLabels{}); err != nil {
					return err
				}
			}
		}
		if err := policy.validate(target.VMLabels); err != nil {
			return err
		}
		target.ExecPolicies = append(target.ExecPolicies, policy)
		return mutation.publish(ctx, target, ownerUID)
	})
	if err != nil {
		return ExecPolicyIntent{}, PolicyReceipt{}, err
	}
	return policy, PolicyReceipt{
		Incarnation: policy.Incarnation, ClaimID: policy.ClaimID,
		WasCreated: claim != "" && claim == policy.ClaimID,
	}, nil
}

// RemoveVMExecPolicy revokes the selected root grant and publishes its successor index.
func RemoveVMExecPolicy(ctx context.Context, ownerUID uint32, id, claim, expected string) error {
	if ownerUID == 0 || validateGenerationID(id) != nil || !validPolicyOwnership("", expected) ||
		(claim != "" && !policyOwnershipPattern.MatchString(claim)) {
		return invalidGeneration("invalid exec policy removal ownership")
	}
	return withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		if err := mutation.recoverExecPolicyIndex(ctx, ownerUID); err != nil {
			return err
		}
		for index, policy := range mutation.before.ExecPolicies {
			if policy.ID != id || policy.OwnerUID != ownerUID {
				continue
			}
			if (claim != "" && policy.ClaimID != claim) || (expected != "" && policy.Incarnation != expected) {
				return policyOwnershipConflict()
			}
			target := cloneIntent(mutation.before)
			target.ExecPolicies = slices.Delete(target.ExecPolicies, index, index+1)
			return mutation.publish(ctx, target, ownerUID)
		}
		return nil
	})
}

// ListVMExecPolicies returns only the authenticated owner's committed policies.
func ListVMExecPolicies(ctx context.Context, ownerUID uint32) ([]ExecPolicyIntent, error) {
	if ownerUID == 0 {
		return nil, invalidGeneration("exec policy listing requires an authenticated owner")
	}
	policies := make([]ExecPolicyIntent, 0)
	err := withCommittedLabelIntent(ctx, func(intent Intent) error {
		for _, policy := range intent.ExecPolicies {
			if policy.OwnerUID == ownerUID {
				policies = append(policies, policy)
			}
		}
		return nil
	})
	return sortedCopy(policies, func(policy ExecPolicyIntent) string { return policy.ID }), err
}
