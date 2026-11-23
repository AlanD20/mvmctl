package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/crypto"
)

// EnsureLabelTrafficPolicy updates the authenticated operator's policy inventory.
// A selector can only match the operator's admitted endpoints.
func EnsureLabelTrafficPolicy(
	ctx context.Context, ownerUID uint32, source, destination VMPolicyEndpoint,
	transport Protocol, ports PortInterval, claim, expected string,
) (policy LabelTrafficPolicy, receipt PolicyReceipt, err error) {
	policy = LabelTrafficPolicy{
		OwnerUID: ownerUID, Source: source, Destination: destination, Protocol: transport, Ports: ports,
		ClaimID: claim, Incarnation: crypto.UUIDV4(),
	}
	if ownerUID == 0 || !validPolicyOwnership(claim, policy.Incarnation) ||
		!validPolicyOwnership("", expected) || (expected != "" && claim == "") {
		return LabelTrafficPolicy{}, PolicyReceipt{}, invalidGeneration(
			"invalid label traffic policy request ownership",
		)
	}
	policy.ID, err = policy.identity()
	if err != nil {
		return LabelTrafficPolicy{}, PolicyReceipt{}, err
	}
	err = withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		for _, existing := range mutation.before.LabelTrafficPolicies {
			if existing.ID != policy.ID {
				continue
			}
			if existing.OwnerUID != ownerUID ||
				(expected != "" && existing.Incarnation != expected) {
				return policyOwnershipConflict()
			}
			policy = existing
			return verifyExactCommittedGeneration(ctx, mutation.kernel, mutation.compiled)
		}
		if expected != "" {
			return policyOwnershipConflict()
		}
		target := cloneIntent(mutation.before)
		target.LabelTrafficPolicies = append(target.LabelTrafficPolicies, policy)
		return mutation.publish(ctx, target, 0)
	})
	if err != nil {
		return LabelTrafficPolicy{}, PolicyReceipt{}, err
	}
	return policy, PolicyReceipt{
		Incarnation: policy.Incarnation, ClaimID: policy.ClaimID,
		WasCreated: claim != "" && policy.ClaimID == claim,
	}, nil
}

// RemoveLabelTrafficPolicy removes the exact rule instance when a workflow supplies its claim.
func RemoveLabelTrafficPolicy(ctx context.Context, ownerUID uint32, policyID, claim, expected string) error {
	if ownerUID == 0 || validateGenerationID(policyID) != nil || !validPolicyOwnership("", expected) ||
		(claim != "" && !policyOwnershipPattern.MatchString(claim)) {
		return invalidGeneration("invalid label traffic policy removal ownership")
	}
	return withLogicalPolicyMutation(ctx, func(mutation *logicalPolicyMutation) error {
		for index, policy := range mutation.before.LabelTrafficPolicies {
			if policy.ID != policyID || policy.OwnerUID != ownerUID {
				continue
			}
			if (claim != "" && policy.ClaimID != claim) || (expected != "" && policy.Incarnation != expected) {
				return policyOwnershipConflict()
			}
			target := cloneIntent(mutation.before)
			target.LabelTrafficPolicies = slices.Delete(target.LabelTrafficPolicies, index, index+1)
			return mutation.publish(ctx, target, 0)
		}
		return verifyExactCommittedGeneration(ctx, mutation.kernel, mutation.compiled)
	})
}

func (intent *Intent) removeLabelTrafficForVM(ownerUID uint32, vmID string) {
	intent.LabelTrafficPolicies = slices.DeleteFunc(intent.LabelTrafficPolicies, func(policy LabelTrafficPolicy) bool {
		return policy.OwnerUID == ownerUID && (policy.Source.VMID == vmID || policy.Destination.VMID == vmID)
	})
}

// ListLabelTrafficPolicies returns committed desired policies for the authenticated operator.
func ListLabelTrafficPolicies(ctx context.Context, ownerUID uint32) ([]LabelTrafficPolicy, error) {
	if ownerUID == 0 {
		return nil, invalidGeneration("label traffic policy listing requires an authenticated owner")
	}
	policies := make([]LabelTrafficPolicy, 0)
	err := withCommittedLabelIntent(ctx, func(intent Intent) error {
		for _, policy := range intent.LabelTrafficPolicies {
			if policy.OwnerUID == ownerUID {
				policies = append(policies, policy)
			}
		}
		return nil
	})
	return sortedCopy(policies, func(policy LabelTrafficPolicy) string { return policy.ID }), err
}
