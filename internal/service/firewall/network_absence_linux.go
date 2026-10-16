package firewall

import "context"

// VerifyNetworkAbsent proves the retained owner-bound network has no current firewall references.
// CRITICAL: Absence is acknowledged only after fresh inspection of the complete committed generation.
// This method never removes intent or kernel objects, including objects belonging to other networks.
func (lease *MutationLease) VerifyNetworkAbsent(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall network absence lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	// CRITICAL: ready may recover an uncertain transaction. Require the caller to finish that
	// recovery through acquisition before entering this strictly read-only absence checkpoint.
	if state.uncertain {
		return generationAuthorityError("firewall network absence requires reconciled generation authority", nil, nil)
	}
	if err := state.ready(ctx); err != nil {
		return err
	}
	networkID := state.binding.NetworkID
	for _, network := range state.intent.Networks {
		if network.NetworkID == networkID {
			return generationAuthorityError("firewall network intent is still present", nil, nil)
		}
	}
	for _, activeID := range state.intent.ActiveNetworks {
		if activeID == networkID {
			return generationAuthorityError("firewall network projection is still active", nil, nil)
		}
	}
	for _, endpoint := range state.intent.Endpoints {
		if endpoint.NetworkID == networkID {
			return generationAuthorityError("firewall network still has an active VM endpoint", nil, nil)
		}
	}
	for _, reservation := range state.intent.Reservations {
		if reservation.NetworkID == networkID {
			return generationAuthorityError("firewall network still has a durable VM reservation", nil, nil)
		}
	}
	for _, grant := range state.intent.NetworkGrants {
		if grant.SourceNetworkID == networkID {
			return generationAuthorityError("firewall network still has a traffic grant", nil, nil)
		}
	}
	for _, service := range state.intent.HostServices {
		if service.SourceNetworkID == networkID {
			return generationAuthorityError("firewall network still has a host service grant", nil, nil)
		}
	}
	return verifyExactCommittedGeneration(ctx, state.network.kernel, state.compiled)
}
