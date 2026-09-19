package firewall

import (
	"context"
	"slices"

	"mvmctl/internal/lib/authorityfs"
)

// PolicyBinding is root-observed network authority for a closed traffic-policy mutation.
type PolicyBinding struct {
	OwnerUID      uint32
	NetworkID     string
	BootID        string
	HostNamespace NamespaceIdentity
}

// BeginPolicyChange acquires the firewall suffix after the owned source-network mutation lock.
// CRITICAL: The root network service resolves this binding; unprivileged requests contain only IDs.
func BeginPolicyChange(
	ctx context.Context, mutation *authorityfs.NetworkMutation, binding PolicyBinding,
) (*MutationLease, error) {
	if ctx == nil || !generationBootIDPattern.MatchString(binding.BootID) || binding.HostNamespace.Device == 0 ||
		binding.HostNamespace.Inode == 0 {
		return nil, generationAuthorityError("firewall policy authority binding is incomplete", nil, nil)
	}
	return beginMutation(ctx, mutation, EndpointBinding{
		OwnerUID:      binding.OwnerUID,
		NetworkID:     binding.NetworkID,
		BootID:        binding.BootID,
		HostNamespace: binding.HostNamespace,
	})
}

// ApplyVMTrafficGrant installs one exact directional owned-VM grant and freshly verifies the full generation.
func (lease *MutationLease) ApplyVMTrafficGrant(ctx context.Context, grant VMTrafficGrant) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	source, err := state.ownedEndpoint(grant.SourceVMID)
	if err != nil {
		return err
	}
	if _, err := state.ownedEndpoint(grant.DestinationVMID); err != nil {
		return err
	}
	if source.NetworkID != state.binding.NetworkID {
		return generationAuthorityError("VM policy source differs from retained network lock", nil, nil)
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.VMGrants {
		if existing.ID == grant.ID {
			if existing != grant {
				return generationAuthorityError("existing VM policy intent differs", nil, nil)
			}
			return state.applyIntent(ctx, target)
		}
	}
	target.VMGrants = append(target.VMGrants, grant)
	return state.applyIntent(ctx, target)
}

// ApplyNetworkTrafficGrant installs one owned-network to owned-VM grant, including same-network traffic.
func (lease *MutationLease) ApplyNetworkTrafficGrant(ctx context.Context, grant NetworkTrafficGrant) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if err := state.ownedNetwork(grant.SourceNetworkID); err != nil {
		return err
	}
	if _, err := state.ownedEndpoint(grant.DestinationVMID); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.NetworkGrants {
		if existing.ID == grant.ID {
			if existing != grant {
				return generationAuthorityError("existing network policy intent differs", nil, nil)
			}
			return state.applyIntent(ctx, target)
		}
	}
	target.NetworkGrants = append(target.NetworkGrants, grant)
	return state.applyIntent(ctx, target)
}

// RemoveVMTrafficGrant revokes a root-stored owned VM grant before desired-state deletion.
func (lease *MutationLease) RemoveVMTrafficGrant(ctx context.Context, policyID string) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.VMGrants {
		if existing.ID != policyID {
			continue
		}
		source, err := state.ownedEndpoint(existing.SourceVMID)
		if err != nil {
			return err
		}
		if _, err := state.ownedEndpoint(existing.DestinationVMID); err != nil {
			return err
		}
		if source.NetworkID != state.binding.NetworkID {
			return generationAuthorityError("VM revocation source differs from retained network lock", nil, nil)
		}
	}
	target.VMGrants = slices.DeleteFunc(
		target.VMGrants,
		func(grant VMTrafficGrant) bool { return grant.ID == policyID },
	)
	return state.applyIntent(ctx, target)
}

// RemoveNetworkTrafficGrant revokes a root-stored owned network grant before desired-state deletion.
func (lease *MutationLease) RemoveNetworkTrafficGrant(ctx context.Context, policyID string) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.NetworkGrants {
		if existing.ID != policyID {
			continue
		}
		if err := state.ownedNetwork(existing.SourceNetworkID); err != nil {
			return err
		}
		if _, err := state.ownedEndpoint(existing.DestinationVMID); err != nil {
			return err
		}
	}
	target.NetworkGrants = slices.DeleteFunc(
		target.NetworkGrants,
		func(grant NetworkTrafficGrant) bool { return grant.ID == policyID },
	)
	return state.applyIntent(ctx, target)
}

// ApplyHostService grants a fixed root service on the retained managed network.
func (lease *MutationLease) ApplyHostService(ctx context.Context, service HostServiceIntent) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if err := state.ownedNetwork(service.SourceNetworkID); err != nil {
		return err
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.HostServices {
		if existing.ID == service.ID {
			if existing != service {
				return generationAuthorityError("existing host service intent differs", nil, nil)
			}
			return state.applyIntent(ctx, target)
		}
	}
	target.HostServices = append(target.HostServices, service)
	return state.applyIntent(ctx, target)
}

// RemoveNetwork removes exact network firewall intent only after every VM endpoint has been removed.
func (lease *MutationLease) RemoveNetwork(ctx context.Context, network NetworkIntent) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall network lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if network.OwnerUID != state.binding.OwnerUID || network.NetworkID != state.binding.NetworkID {
		return generationAuthorityError("firewall network removal binding differs", nil, nil)
	}
	target := cloneIntent(state.intent)
	for _, endpoint := range target.Endpoints {
		if endpoint.NetworkID == network.NetworkID {
			return generationAuthorityError("firewall network still has VM endpoints", nil, nil)
		}
	}
	for _, reservation := range target.Reservations {
		if reservation.NetworkID == network.NetworkID {
			return generationAuthorityError("firewall network still has durable VM reservations", nil, nil)
		}
	}
	for _, existing := range target.Networks {
		if existing.NetworkID == network.NetworkID && existing != network {
			return generationAuthorityError("firewall network intent was replaced", nil, nil)
		}
	}
	target.Networks = slices.DeleteFunc(
		target.Networks,
		func(existing NetworkIntent) bool { return existing.NetworkID == network.NetworkID },
	)
	target.NetworkGrants = slices.DeleteFunc(
		target.NetworkGrants,
		func(grant NetworkTrafficGrant) bool { return grant.SourceNetworkID == network.NetworkID },
	)
	target.HostServices = slices.DeleteFunc(
		target.HostServices,
		func(service HostServiceIntent) bool { return service.SourceNetworkID == network.NetworkID },
	)
	return state.applyIntent(ctx, target)
}

// CALLER MUST HOLD: state.mu.
func (state *mutationLeaseState) ownedEndpoint(vmID string) (EndpointReservation, error) {
	for _, endpoint := range state.intent.Reservations {
		if endpoint.VMID != vmID {
			continue
		}
		if endpoint.OwnerUID != state.binding.OwnerUID {
			return EndpointReservation{}, generationAuthorityError(
				"traffic policy endpoint authority differs",
				nil,
				nil,
			)
		}
		return endpoint, nil
	}
	return EndpointReservation{}, generationAuthorityError(
		"traffic policy endpoint has no root-owned durable reservation",
		nil,
		nil,
	)
}

// CALLER MUST HOLD: state.mu.
func (state *mutationLeaseState) ownedNetwork(networkID string) error {
	if networkID != state.binding.NetworkID {
		return generationAuthorityError("traffic policy source differs from retained network lock", nil, nil)
	}
	for _, network := range state.intent.Networks {
		if network.NetworkID == networkID && network.OwnerUID == state.binding.OwnerUID {
			return nil
		}
	}
	return generationAuthorityError("traffic policy network is not registered in the root generation", nil, nil)
}
