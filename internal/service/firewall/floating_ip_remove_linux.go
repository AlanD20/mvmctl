package firewall

import (
	"context"
	"slices"
)

// RemoveFloatingIP removes only a freshly fenced exact root resource and its destination grants.
// The network owner retains its immutable root retirement record; this method never authorizes timeout replay.
func (lease *MutationLease) RemoveFloatingIP(ctx context.Context, binding FloatingIPBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP removal authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	target, err := state.floatingIPRemovalIntent(binding, observed)
	if err != nil {
		return err
	}
	return state.applyIntent(ctx, target)
}

func (state *mutationLeaseState) floatingIPRemovalIntent(
	binding FloatingIPBinding, observed kernelObservation,
) (Intent, error) {
	if binding.OwnerUID != state.binding.OwnerUID || binding.NetworkID != state.binding.NetworkID ||
		binding.OwnerVMID != "" ||
		binding.OwnerLaunchGeneration != 0 || binding.LeaseDuration != 0 {
		return Intent{}, generationAuthorityError("floating-IP removal requires exact ownerless authority", nil, nil)
	}
	if _, err := state.floatingIPFlowAddress(binding.ID, binding.Generation, observed); err != nil {
		return Intent{}, err
	}
	target := cloneIntent(state.intent)
	found := false
	for _, resource := range target.FloatingIPs {
		if resource.ID == binding.ID {
			if resource != binding {
				return Intent{}, generationAuthorityError("floating-IP removal binding changed", nil, nil)
			}
			found = true
		}
	}
	if !found {
		return Intent{}, generationAuthorityError("floating-IP removal resource is missing", nil, nil)
	}
	target.FloatingIPs = slices.DeleteFunc(target.FloatingIPs, func(resource FloatingIPBinding) bool {
		return resource.ID == binding.ID
	})
	target.VMFloatingIPGrants = slices.DeleteFunc(target.VMFloatingIPGrants, func(grant VMFloatingIPTrafficGrant) bool {
		return grant.DestinationFloatingIPID == binding.ID
	})
	target.NetworkFloatingIPGrants = slices.DeleteFunc(
		target.NetworkFloatingIPGrants, func(grant NetworkFloatingIPTrafficGrant) bool {
			return grant.DestinationFloatingIPID == binding.ID
		},
	)
	return target, nil
}

func requireFloatingIPVMOwnerAbsent(intent Intent, vmID string) error {
	for _, resource := range intent.FloatingIPs {
		if resource.OwnerVMID == vmID {
			return generationAuthorityError("VM endpoint removal requires floating-IP fencing", nil, nil)
		}
	}
	return nil
}

// VerifyRetiredFloatingIP proves current packet and logical owner absence without creating intent.
// Its caller separately retains genuine prior-boot VM absence proof; missing authority is acceptable
// here only for historical retirement, never for guest association or implicit availability.
func (lease *MutationLease) VerifyRetiredFloatingIP(ctx context.Context, binding FloatingIPBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP retirement authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.uncertain {
		return generationAuthorityError("floating-IP retirement requires recovery", nil, nil)
	}
	if err := state.ready(ctx); err != nil {
		return err
	}
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	return state.verifyRetiredFloatingIP(binding, observed)
}
func (state *mutationLeaseState) verifyRetiredFloatingIP(binding FloatingIPBinding, observed kernelObservation) error {
	if binding.OwnerUID != state.binding.OwnerUID || binding.NetworkID != state.binding.NetworkID ||
		binding.Generation == 0 ||
		binding.OwnerVMID != "" || binding.OwnerLaunchGeneration != 0 || binding.LeaseDuration != 0 ||
		!validResourceID(binding.ID) || !binding.IPv4.Is4() || !binding.IPv4.IsPrivate() {
		return generationAuthorityError("floating-IP retirement scope differs", nil, nil)
	}
	if err := compareKernelObservation(observed, state.compiled); err != nil {
		return err
	}
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID == binding.ID && resource != binding {
			return generationAuthorityError("floating-IP retirement resource is owned or rebound", nil, nil)
		}
		if resource.ID != binding.ID && resource.NetworkID == binding.NetworkID && resource.IPv4 == binding.IPv4 {
			return generationAuthorityError("floating-IP retirement address has another reservation", nil, nil)
		}
	}
	for _, table := range observed.tables {
		for _, element := range table.secondaryElements {
			_, address, err := decodeSecondaryLeaseKey(element.key[:])
			if err != nil {
				return err
			}
			if address == binding.IPv4 {
				return generationAuthorityError("floating-IP retirement still has a timed key", nil, nil)
			}
		}
	}
	return nil
}

func requireFloatingIPNetworkAbsent(intent Intent, networkID string) error {
	for _, resource := range intent.FloatingIPs {
		if resource.NetworkID == networkID {
			return generationAuthorityError("network removal requires floating-IP retirement", nil, nil)
		}
	}
	return nil
}
func removeFloatingIPNetworkSourcePolicies(intent *Intent, networkID string) {
	intent.NetworkFloatingIPGrants = slices.DeleteFunc(intent.NetworkFloatingIPGrants,
		func(grant NetworkFloatingIPTrafficGrant) bool { return grant.SourceNetworkID == networkID })
}
