package firewall

import (
	"context"
	"slices"
)

// VerifyExistingVMEndpoint proves an exact root-resolved endpoint is already active in the retained
// graph. It never applies endpoint intent or mints a cleanup capability from an observation.
func (lease *MutationLease) VerifyExistingVMEndpoint(ctx context.Context, binding EndpointBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("existing firewall endpoint authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := validateEndpointBinding(binding, false); err != nil {
		return err
	}
	if err := state.ready(ctx); err != nil {
		return err
	}
	if err := state.requireExistingVMEndpoint(binding); err != nil {
		return err
	}
	return verifyExactCommittedGeneration(ctx, state.network.kernel, state.compiled)
}

// CALLER MUST HOLD: state.mu and a ready mutation lease. Presence is metadata proof only;
// VerifyExistingVMEndpoint also requires fresh complete kernel readback before returning.
func (state *mutationLeaseState) requireExistingVMEndpoint(binding EndpointBinding) error {
	if state.binding != binding || !slices.Contains(state.intent.Endpoints, binding) {
		return generationAuthorityError("existing endpoint differs from retained firewall authority", nil, nil)
	}
	return nil
}
