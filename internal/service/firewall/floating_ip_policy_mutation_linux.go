package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/crypto"
)

// VerifyFloatingIPPolicyResource compares root reservation authority with the current firewall projection.
// It neither assigns an owner nor creates or renews a timed element.
func (lease *MutationLease) VerifyFloatingIPPolicyResource(ctx context.Context, binding FloatingIPBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP policy mutation is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	resource, err := state.ownedFloatingIPPolicyResource(binding.ID)
	if err != nil {
		return err
	}
	if resource != binding {
		return generationAuthorityError("floating-IP policy root binding differs", nil, nil)
	}
	return nil
}

func (state *mutationLeaseState) ownedFloatingIPPolicyResource(id string) (FloatingIPBinding, error) {
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID != id {
			continue
		}
		if resource.OwnerUID != state.binding.OwnerUID {
			break
		}
		for _, network := range state.intent.Networks {
			if network.NetworkID == resource.NetworkID && network.OwnerUID == state.binding.OwnerUID {
				return resource, nil
			}
		}
		break
	}
	return FloatingIPBinding{}, generationAuthorityError("floating-IP policy destination is not owned", nil, nil)
}

func (lease *MutationLease) EnsureVMFloatingIPTrafficGrant(
	ctx context.Context,
	grant VMFloatingIPTrafficGrant,
	claim, expected string,
) (PolicyReceipt, error) {
	if lease == nil || lease.state == nil {
		return PolicyReceipt{}, generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return PolicyReceipt{}, err
	}
	target, receipt, err := state.vmFloatingIPTrafficGrantIntent(grant, claim, expected)
	if err != nil {
		return PolicyReceipt{}, err
	}
	if err := state.applyIntent(ctx, target); err != nil {
		return PolicyReceipt{}, err
	}
	return receipt, nil
}

// vmFloatingIPTrafficGrantIntent prepares immutable destination intent without authorizing timed activation.
func (state *mutationLeaseState) vmFloatingIPTrafficGrantIntent(
	grant VMFloatingIPTrafficGrant,
	claim,
	expected string,
) (Intent, PolicyReceipt, error) {
	source, err := state.ownedEndpoint(grant.SourceVMID)
	if err != nil {
		return Intent{}, PolicyReceipt{}, err
	}
	if _, err := state.ownedFloatingIPPolicyResource(grant.DestinationFloatingIPID); err != nil {
		return Intent{}, PolicyReceipt{}, err
	}
	if source.NetworkID != state.binding.NetworkID {
		return Intent{}, PolicyReceipt{}, generationAuthorityError(
			"VM policy source differs from retained network lock",
			nil,
			nil,
		)
	}
	target := cloneIntent(state.intent)
	for index, existing := range target.VMFloatingIPGrants {
		if existing.ID == grant.ID {
			grant.ClaimID, grant.Incarnation = existing.ClaimID, existing.Incarnation
			if existing != grant {
				return Intent{}, PolicyReceipt{}, generationAuthorityError(
					"existing VM policy intent differs",
					nil,
					nil,
				)
			}
			if expected != "" && existing.Incarnation != expected {
				return Intent{}, PolicyReceipt{}, policyOwnershipConflict()
			}
			if existing.Incarnation == "" {
				existing.Incarnation = crypto.UUIDV4()
				target.VMFloatingIPGrants[index] = existing
			}
			return target, PolicyReceipt{
				Incarnation: existing.Incarnation,
				ClaimID:     existing.ClaimID,
				WasCreated:  claim != "" && existing.ClaimID == claim,
			}, nil
		}
	}
	if expected != "" {
		return Intent{}, PolicyReceipt{}, policyOwnershipConflict()
	}
	grant.Incarnation, grant.ClaimID = crypto.UUIDV4(), claim
	target.VMFloatingIPGrants = append(target.VMFloatingIPGrants, grant)
	return target, PolicyReceipt{Incarnation: grant.Incarnation, ClaimID: claim, WasCreated: claim != ""}, nil
}

func (lease *MutationLease) EnsureNetworkFloatingIPTrafficGrant(
	ctx context.Context,
	grant NetworkFloatingIPTrafficGrant,
	claim, expected string,
) (PolicyReceipt, error) {
	if lease == nil || lease.state == nil {
		return PolicyReceipt{}, generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return PolicyReceipt{}, err
	}
	target, receipt, err := state.networkFloatingIPTrafficGrantIntent(grant, claim, expected)
	if err != nil {
		return PolicyReceipt{}, err
	}
	if err := state.applyIntent(ctx, target); err != nil {
		return PolicyReceipt{}, err
	}
	return receipt, nil
}

// networkFloatingIPTrafficGrantIntent prepares immutable destination intent without authorizing timed activation.
func (state *mutationLeaseState) networkFloatingIPTrafficGrantIntent(
	grant NetworkFloatingIPTrafficGrant,
	claim,
	expected string,
) (Intent, PolicyReceipt, error) {
	if err := state.ownedNetwork(grant.SourceNetworkID); err != nil {
		return Intent{}, PolicyReceipt{}, err
	}
	if _, err := state.ownedFloatingIPPolicyResource(grant.DestinationFloatingIPID); err != nil {
		return Intent{}, PolicyReceipt{}, err
	}
	target := cloneIntent(state.intent)
	for index, existing := range target.NetworkFloatingIPGrants {
		if existing.ID == grant.ID {
			grant.ClaimID, grant.Incarnation = existing.ClaimID, existing.Incarnation
			if existing != grant {
				return Intent{}, PolicyReceipt{}, generationAuthorityError(
					"existing network policy intent differs",
					nil,
					nil,
				)
			}
			if expected != "" && existing.Incarnation != expected {
				return Intent{}, PolicyReceipt{}, policyOwnershipConflict()
			}
			if existing.Incarnation == "" {
				existing.Incarnation = crypto.UUIDV4()
				target.NetworkFloatingIPGrants[index] = existing
			}
			return target, PolicyReceipt{
				Incarnation: existing.Incarnation,
				ClaimID:     existing.ClaimID,
				WasCreated:  claim != "" && existing.ClaimID == claim,
			}, nil
		}
	}
	if expected != "" {
		return Intent{}, PolicyReceipt{}, policyOwnershipConflict()
	}
	grant.Incarnation, grant.ClaimID = crypto.UUIDV4(), claim
	target.NetworkFloatingIPGrants = append(target.NetworkFloatingIPGrants, grant)
	return target, PolicyReceipt{Incarnation: grant.Incarnation, ClaimID: claim, WasCreated: claim != ""}, nil
}

func (lease *MutationLease) RemoveOwnedVMFloatingIPTrafficGrant(
	ctx context.Context,
	policyID,
	claim,
	expected string,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target, err := state.removeOwnedVMFloatingIPTrafficGrantIntent(policyID, claim, expected)
	if err != nil {
		return err
	}
	return state.applyIntent(ctx, target)
}
func (state *mutationLeaseState) removeOwnedVMFloatingIPTrafficGrantIntent(
	policyID,
	claim,
	expected string,
) (Intent, error) {
	target := cloneIntent(state.intent)
	for _, existing := range target.VMFloatingIPGrants {
		if existing.ID != policyID {
			continue
		}
		if (claim != "" && existing.ClaimID != claim) || (expected != "" && existing.Incarnation != expected) {
			return Intent{}, policyOwnershipConflict()
		}
		source, err := state.ownedEndpoint(existing.SourceVMID)
		if err != nil {
			return Intent{}, err
		}
		if _, err := state.ownedFloatingIPPolicyResource(existing.DestinationFloatingIPID); err != nil {
			return Intent{}, err
		}
		if source.NetworkID != state.binding.NetworkID {
			return Intent{}, generationAuthorityError(
				"VM revocation source differs from retained network lock",
				nil,
				nil,
			)
		}
	}
	target.VMFloatingIPGrants = slices.DeleteFunc(
		target.VMFloatingIPGrants,
		func(grant VMFloatingIPTrafficGrant) bool { return grant.ID == policyID },
	)
	return target, nil
}

func (lease *MutationLease) RemoveOwnedNetworkFloatingIPTrafficGrant(
	ctx context.Context,
	policyID, claim, expected string,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target, err := state.removeOwnedNetworkFloatingIPTrafficGrantIntent(policyID, claim, expected)
	if err != nil {
		return err
	}
	return state.applyIntent(ctx, target)
}
func (state *mutationLeaseState) removeOwnedNetworkFloatingIPTrafficGrantIntent(
	policyID,
	claim,
	expected string,
) (Intent, error) {
	target := cloneIntent(state.intent)
	for _, existing := range target.NetworkFloatingIPGrants {
		if existing.ID != policyID {
			continue
		}
		if (claim != "" && existing.ClaimID != claim) || (expected != "" && existing.Incarnation != expected) {
			return Intent{}, policyOwnershipConflict()
		}
		if err := state.ownedNetwork(existing.SourceNetworkID); err != nil {
			return Intent{}, err
		}
		if _, err := state.ownedFloatingIPPolicyResource(existing.DestinationFloatingIPID); err != nil {
			return Intent{}, err
		}
	}
	target.NetworkFloatingIPGrants = slices.DeleteFunc(
		target.NetworkFloatingIPGrants,
		func(grant NetworkFloatingIPTrafficGrant) bool { return grant.ID == policyID },
	)
	return target, nil
}

// VerifyRetiredFloatingIPPolicyResource permits projection cleanup only after exact root retirement
// and absence of all destination intent in the admitted generation. It never changes packet authority.
func (lease *MutationLease) VerifyRetiredFloatingIPPolicyResource(ctx context.Context, id string) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID == id {
			return generationAuthorityError("retired floating-IP still has firewall intent", nil, nil)
		}
	}
	for _, grant := range state.intent.VMFloatingIPGrants {
		if grant.DestinationFloatingIPID == id {
			return generationAuthorityError("retired floating-IP still has VM policy intent", nil, nil)
		}
	}
	for _, grant := range state.intent.NetworkFloatingIPGrants {
		if grant.DestinationFloatingIPID == id {
			return generationAuthorityError("retired floating-IP still has network policy intent", nil, nil)
		}
	}
	return nil
}
