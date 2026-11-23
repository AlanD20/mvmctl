package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/crypto"
)

// EnsureVMHostTrafficGrant installs an exact VM-to-gateway grant under retained source-network authority.
// CRITICAL: The caller supplies only root-resolved source identity; no destination address is accepted.
func (lease *MutationLease) EnsureVMHostTrafficGrant(
	ctx context.Context, grant VMHostTrafficGrant, claim, expected string,
) (PolicyReceipt, error) {
	if lease == nil || lease.state == nil {
		return PolicyReceipt{}, generationAuthorityError("firewall host policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return PolicyReceipt{}, err
	}
	source, err := state.ownedEndpoint(grant.SourceVMID)
	if err != nil {
		return PolicyReceipt{}, err
	}
	if err := state.ownedNetwork(source.NetworkID); err != nil {
		return PolicyReceipt{}, err
	}
	target := cloneIntent(state.intent)
	for index, existing := range target.VMHostGrants {
		if existing.ID != grant.ID {
			continue
		}
		grant.ClaimID, grant.Incarnation = existing.ClaimID, existing.Incarnation
		if existing != grant {
			return PolicyReceipt{}, generationAuthorityError("existing VM-to-host policy intent differs", nil, nil)
		}
		if expected != "" && existing.Incarnation != expected {
			return PolicyReceipt{}, policyOwnershipConflict()
		}
		if existing.Incarnation == "" {
			existing.Incarnation = crypto.UUIDV4()
			target.VMHostGrants[index] = existing
		}
		return PolicyReceipt{
			Incarnation: existing.Incarnation, ClaimID: existing.ClaimID,
			WasCreated: claim != "" && existing.ClaimID == claim,
		}, state.applyIntent(ctx, target)
	}
	if expected != "" {
		return PolicyReceipt{}, policyOwnershipConflict()
	}
	grant.Incarnation, grant.ClaimID = crypto.UUIDV4(), claim
	target.VMHostGrants = append(target.VMHostGrants, grant)
	if err := state.applyIntent(ctx, target); err != nil {
		return PolicyReceipt{}, err
	}
	return PolicyReceipt{Incarnation: grant.Incarnation, ClaimID: claim, WasCreated: claim != ""}, nil
}

// EnsureNetworkHostTrafficGrant installs a network-to-own-gateway grant with conditional workflow ownership.
// CRITICAL: The source must match the retained owned network, not an untrusted caller's network projection.
func (lease *MutationLease) EnsureNetworkHostTrafficGrant(
	ctx context.Context, grant NetworkHostTrafficGrant, claim, expected string,
) (PolicyReceipt, error) {
	if lease == nil || lease.state == nil {
		return PolicyReceipt{}, generationAuthorityError("firewall host policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return PolicyReceipt{}, err
	}
	if err := state.ownedNetwork(grant.SourceNetworkID); err != nil {
		return PolicyReceipt{}, err
	}
	target := cloneIntent(state.intent)
	for index, existing := range target.NetworkHostGrants {
		if existing.ID != grant.ID {
			continue
		}
		grant.ClaimID, grant.Incarnation = existing.ClaimID, existing.Incarnation
		if existing != grant {
			return PolicyReceipt{}, generationAuthorityError("existing network-to-host policy intent differs", nil, nil)
		}
		if expected != "" && existing.Incarnation != expected {
			return PolicyReceipt{}, policyOwnershipConflict()
		}
		if existing.Incarnation == "" {
			existing.Incarnation = crypto.UUIDV4()
			target.NetworkHostGrants[index] = existing
		}
		return PolicyReceipt{
			Incarnation: existing.Incarnation, ClaimID: existing.ClaimID,
			WasCreated: claim != "" && existing.ClaimID == claim,
		}, state.applyIntent(ctx, target)
	}
	if expected != "" {
		return PolicyReceipt{}, policyOwnershipConflict()
	}
	grant.Incarnation, grant.ClaimID = crypto.UUIDV4(), claim
	target.NetworkHostGrants = append(target.NetworkHostGrants, grant)
	if err := state.applyIntent(ctx, target); err != nil {
		return PolicyReceipt{}, err
	}
	return PolicyReceipt{Incarnation: grant.Incarnation, ClaimID: claim, WasCreated: claim != ""}, nil
}

// RemoveOwnedVMHostTrafficGrant revokes only the selected VM-to-host policy incarnation.
func (lease *MutationLease) RemoveOwnedVMHostTrafficGrant(
	ctx context.Context, policyID, claim, expected string,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall host policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.VMHostGrants {
		if existing.ID != policyID {
			continue
		}
		if claim != "" && (existing.ClaimID != claim || (expected != "" && existing.Incarnation != expected)) {
			return policyOwnershipConflict()
		}
		source, err := state.ownedEndpoint(existing.SourceVMID)
		if err != nil {
			return err
		}
		if err := state.ownedNetwork(source.NetworkID); err != nil {
			return err
		}
	}
	target.VMHostGrants = slices.DeleteFunc(target.VMHostGrants,
		func(grant VMHostTrafficGrant) bool { return grant.ID == policyID })
	return state.applyIntent(ctx, target)
}

// RemoveOwnedNetworkHostTrafficGrant revokes only the selected network-to-host policy incarnation.
func (lease *MutationLease) RemoveOwnedNetworkHostTrafficGrant(
	ctx context.Context, policyID, claim, expected string,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall host policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.NetworkHostGrants {
		if existing.ID != policyID {
			continue
		}
		if claim != "" && (existing.ClaimID != claim || (expected != "" && existing.Incarnation != expected)) {
			return policyOwnershipConflict()
		}
		if err := state.ownedNetwork(existing.SourceNetworkID); err != nil {
			return err
		}
	}
	target.NetworkHostGrants = slices.DeleteFunc(target.NetworkHostGrants,
		func(grant NetworkHostTrafficGrant) bool { return grant.ID == policyID })
	return state.applyIntent(ctx, target)
}
