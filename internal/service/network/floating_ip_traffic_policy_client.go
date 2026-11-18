package network

import "context"

// ApplyVMFloatingIPTrafficPolicy installs an exact VM-to-floating-IP grant before local projection.
func (client *TrafficPolicyClient) ApplyVMFloatingIPTrafficPolicy(
	ctx context.Context, request VMFloatingIPTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMFloatingIPTrafficPolicyApplyAction, request, request.PolicyID())
}

// RemoveVMFloatingIPTrafficPolicy revokes an exact VM-to-floating-IP grant before local deletion.
func (client *TrafficPolicyClient) RemoveVMFloatingIPTrafficPolicy(
	ctx context.Context, request VMFloatingIPTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMFloatingIPTrafficPolicyRemoveAction, request, request.PolicyID())
}

// ApplyNetworkFloatingIPTrafficPolicy installs an owned-network floating-IP grant before local projection.
func (client *TrafficPolicyClient) ApplyNetworkFloatingIPTrafficPolicy(
	ctx context.Context, request NetworkFloatingIPTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkFloatingIPTrafficPolicyApplyAction, request, request.PolicyID())
}

// RemoveNetworkFloatingIPTrafficPolicy revokes an owned-network floating-IP grant before local deletion.
func (client *TrafficPolicyClient) RemoveNetworkFloatingIPTrafficPolicy(
	ctx context.Context, request NetworkFloatingIPTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkFloatingIPTrafficPolicyRemoveAction, request, request.PolicyID())
}
