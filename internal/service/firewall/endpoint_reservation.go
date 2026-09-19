package firewall

import (
	"context"
	"slices"
)

func (intent *Intent) reserveEndpoint(reservation EndpointReservation) error {
	if err := validateEndpointReservation(reservation); err != nil {
		return err
	}
	for _, existing := range intent.Reservations {
		if existing.VMID != reservation.VMID {
			continue
		}
		if existing != reservation {
			return generationAuthorityError("durable firewall VM reservation was replaced", nil, nil)
		}
		return nil
	}
	intent.Reservations = append(intent.Reservations, reservation)
	return nil
}

// RegisterStoppedEndpoint persists a root-cleaned-topology binding without creating an active endpoint.
// CRITICAL: The caller retains the exact VM ownership lock and admits its root-owned cleaned topology.
// Desired SQLite rows and requests are never sufficient authority for this operation.
func (lease *MutationLease) RegisterStoppedEndpoint(ctx context.Context, reservation EndpointReservation) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall policy lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if reservation.OwnerUID != state.binding.OwnerUID {
		return generationAuthorityError("stopped firewall endpoint owner differs", nil, nil)
	}
	for _, endpoint := range state.intent.Endpoints {
		if endpoint.VMID == reservation.VMID {
			return generationAuthorityError("stopped firewall endpoint is still active", nil, nil)
		}
	}
	target := cloneIntent(state.intent)
	if err := target.reserveEndpoint(reservation); err != nil {
		return err
	}
	return state.applyIntent(ctx, target)
}

// ApplyNetwork installs the root-owned managed-network policy before bridge exposure and reads it back.
// CRITICAL: The concrete earlier lock names this network. The network service retains this lease
// through exact bridge activation and supplies only its admitted bridge and root-selected uplink.
func (lease *MutationLease) ApplyNetwork(ctx context.Context, network NetworkIntent) error {
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
		return generationAuthorityError("registered firewall network differs from retained authority", nil, nil)
	}
	target := cloneIntent(state.intent)
	for _, existing := range target.Networks {
		if existing.NetworkID != network.NetworkID {
			continue
		}
		if existing != network {
			return generationAuthorityError("registered firewall network was replaced", nil, nil)
		}
		return state.applyIntent(ctx, target)
	}
	target.Networks = append(target.Networks, network)
	return state.applyIntent(ctx, target)
}

// RegisterNetwork admits the same network projection when recording a stopped-VM policy.
func (lease *MutationLease) RegisterNetwork(ctx context.Context, network NetworkIntent) error {
	return lease.ApplyNetwork(ctx, network)
}

// RemoveVMPolicies invalidates durable endpoint and source/destination grant ownership on final VM deletion.
// CRITICAL: This is not Stop. The caller retains root VM-removal authority after exact topology cleanup.
func (lease *MutationLease) RemoveVMPolicies(ctx context.Context, vmID string) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall VM removal lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if !validResourceID(vmID) {
		return generationAuthorityError("firewall VM removal identity is invalid", nil, nil)
	}
	for _, endpoint := range state.intent.Endpoints {
		if endpoint.VMID == vmID {
			return generationAuthorityError("firewall VM removal requires endpoint cleanup", nil, nil)
		}
	}
	target := cloneIntent(state.intent)
	for _, reservation := range target.Reservations {
		if reservation.VMID != vmID {
			continue
		}
		if reservation.OwnerUID != state.binding.OwnerUID || reservation.NetworkID != state.binding.NetworkID {
			return generationAuthorityError("firewall VM removal reservation ownership differs", nil, nil)
		}
	}
	target.Reservations = slices.DeleteFunc(
		target.Reservations,
		func(reservation EndpointReservation) bool { return reservation.VMID == vmID },
	)
	target.VMGrants = slices.DeleteFunc(
		target.VMGrants,
		func(grant VMTrafficGrant) bool { return grant.SourceVMID == vmID || grant.DestinationVMID == vmID },
	)
	target.NetworkGrants = slices.DeleteFunc(
		target.NetworkGrants,
		func(grant NetworkTrafficGrant) bool { return grant.DestinationVMID == vmID },
	)
	return state.applyIntent(ctx, target)
}
