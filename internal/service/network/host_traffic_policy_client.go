package network

import "context"

// ApplyVMHostTrafficPolicy installs an exact VM-to-gateway grant before local projection.
func (client *TrafficPolicyClient) ApplyVMHostTrafficPolicy(
	ctx context.Context, request VMHostTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMHostTrafficPolicyApplyAction, request, request.PolicyID())
}

// RemoveVMHostTrafficPolicy revokes an exact VM-to-gateway grant before local deletion.
func (client *TrafficPolicyClient) RemoveVMHostTrafficPolicy(
	ctx context.Context, request VMHostTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMHostTrafficPolicyRemoveAction, request, request.PolicyID())
}

// ApplyNetworkHostTrafficPolicy installs an owned-network gateway grant before local projection.
func (client *TrafficPolicyClient) ApplyNetworkHostTrafficPolicy(
	ctx context.Context, request NetworkHostTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkHostTrafficPolicyApplyAction, request, request.PolicyID())
}

// RemoveNetworkHostTrafficPolicy revokes an owned-network gateway grant before local deletion.
func (client *TrafficPolicyClient) RemoveNetworkHostTrafficPolicy(
	ctx context.Context, request NetworkHostTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkHostTrafficPolicyRemoveAction, request, request.PolicyID())
}
