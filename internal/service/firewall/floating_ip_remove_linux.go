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
		binding.OwnerLaunchGeneration != 0 || binding.Lifetime != "" || binding.LeaseDuration != 0 {
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
		binding.OwnerVMID != "" || binding.OwnerLaunchGeneration != 0 || binding.Lifetime != "" || binding.LeaseDuration != 0 ||
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

// VerifyDormantFloatingIP admits exact historical metadata without reviving it.
// Caller derives history from root journal phases; no current endpoint or address key may exist.
func (lease *MutationLease) VerifyDormantFloatingIP(
	ctx context.Context, history []FloatingIPBinding, fenced FloatingIPBinding,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP dormant authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.uncertain {
		return generationAuthorityError("floating-IP dormant authority requires recovery", nil, nil)
	}
	if err := state.ready(ctx); err != nil {
		return err
	}
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	return state.verifyDormantFloatingIP(history, fenced, observed)
}

// RetirePriorBootFloatingIP replaces admitted dormant history with its exact fence.
// Caller retains genuine prior-boot VM absence proof and the network/global mutation locks.
// This never activates an owner or operates on stale kernel identities.
func (lease *MutationLease) RetirePriorBootFloatingIP(
	ctx context.Context, history []FloatingIPBinding, fenced FloatingIPBinding,
) error {
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
	target, err := state.priorBootFloatingIPRetirementIntent(history, fenced, observed)
	if err != nil {
		return err
	}
	if err := state.applyIntent(ctx, target); err != nil {
		return err
	}
	observed, err = state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	return state.verifyRetiredFloatingIP(fenced, observed)
}

func (state *mutationLeaseState) priorBootFloatingIPRetirementIntent(
	history []FloatingIPBinding, fenced FloatingIPBinding, observed kernelObservation,
) (Intent, error) {
	if err := state.verifyDormantFloatingIP(history, fenced, observed); err != nil {
		return Intent{}, err
	}
	return state.floatingIPOwnershipIntent(fenced, false)
}

func (state *mutationLeaseState) verifyDormantFloatingIP(
	history []FloatingIPBinding, fenced FloatingIPBinding, observed kernelObservation,
) error {
	invalid := func() error {
		return generationAuthorityError("floating-IP dormant history is active, rebound or unavailable", nil, nil)
	}
	if fenced.OwnerUID != state.binding.OwnerUID || fenced.NetworkID != state.binding.NetworkID ||
		fenced.Generation == 0 || fenced.OwnerVMID != "" || fenced.OwnerLaunchGeneration != 0 ||
		fenced.Lifetime != "" || fenced.LeaseDuration != 0 || !validResourceID(fenced.ID) ||
		!fenced.IPv4.Is4() || !fenced.IPv4.IsPrivate() {
		return invalid()
	}
	if err := compareKernelObservation(observed, state.compiled); err != nil {
		return err
	}
	for _, candidate := range history {
		if candidate.OwnerUID != fenced.OwnerUID || candidate.NetworkID != fenced.NetworkID ||
			candidate.ID != fenced.ID || candidate.IPv4 != fenced.IPv4 || candidate.Generation == 0 ||
			candidate.Generation > fenced.Generation {
			return invalid()
		}
		for _, endpoint := range state.intent.Endpoints {
			if candidate.OwnerVMID != "" && endpoint.VMID == candidate.OwnerVMID {
				return invalid()
			}
		}
	}
	if len(history) == 0 {
		return invalid()
	}
	matched := false
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID != fenced.ID {
			if resource.NetworkID == fenced.NetworkID && resource.IPv4 == fenced.IPv4 {
				return invalid()
			}
			continue
		}
		matched = false
		for _, candidate := range history {
			matched = matched || resource == candidate
		}
	}
	if !matched {
		return invalid()
	}
	if _, active := state.compiled.bridge.secondaryOwners[fenced.ID]; active {
		return invalid()
	}
	for _, table := range observed.tables {
		for _, element := range table.secondaryElements {
			_, address, err := decodeSecondaryLeaseKey(element.key[:])
			if err != nil {
				return err
			}
			if address == fenced.IPv4 {
				return invalid()
			}
		}
	}
	return nil
}
