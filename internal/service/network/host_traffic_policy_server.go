package network

import (
	"context"

	"mvmctl/internal/service/firewall"
)

// ApplyVMHostTrafficPolicy installs access from one admitted VM to its own managed gateway.
// CRITICAL: The dispatcher authenticates ownerUID; topology and firewall authority derive the destination.
func (server *TrafficPolicyServer) ApplyVMHostTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMHostTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.SourceVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, authority.topologies[request.SourceVMID].NetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	receipt, err := authority.firewall.EnsureVMHostTrafficGrant(ctx, firewall.VMHostTrafficGrant{
		ID: request.PolicyID(), SourceVMID: request.SourceVMID, Protocol: trafficFirewallProtocol(request.Protocol),
		Ports: firewall.PortInterval{First: request.PortStart, Last: request.PortEnd},
	}, request.ClaimID, request.Incarnation)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{
		PolicyID: request.PolicyID(), ClaimID: receipt.ClaimID,
		Incarnation: receipt.Incarnation, WasCreated: receipt.WasCreated,
	}, nil
}

// RemoveVMHostTrafficPolicy revokes only the exact selected VM-to-host grant before local deletion.
// CRITICAL: Revocation requires the same admitted owner/source graph even when the grant is already absent.
func (server *TrafficPolicyServer) RemoveVMHostTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMHostTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.SourceVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, authority.topologies[request.SourceVMID].NetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveOwnedVMHostTrafficGrant(ctx, request.PolicyID(),
		request.ClaimID, request.Incarnation); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// ApplyNetworkHostTrafficPolicy permits selected ports on the authenticated source network's gateway.
// CRITICAL: A request cannot specify an address, raw firewall expression or foreign network authority.
func (server *TrafficPolicyServer) ApplyNetworkHostTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkHostTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, nil)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, request.SourceNetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	receipt, err := authority.firewall.EnsureNetworkHostTrafficGrant(ctx, firewall.NetworkHostTrafficGrant{
		ID: request.PolicyID(), SourceNetworkID: request.SourceNetworkID,
		Protocol: trafficFirewallProtocol(request.Protocol),
		Ports:    firewall.PortInterval{First: request.PortStart, Last: request.PortEnd},
	}, request.ClaimID, request.Incarnation)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{
		PolicyID: request.PolicyID(), ClaimID: receipt.ClaimID,
		Incarnation: receipt.Incarnation, WasCreated: receipt.WasCreated,
	}, nil
}

// RemoveNetworkHostTrafficPolicy revokes the exact owned network-to-host policy incarnation.
func (server *TrafficPolicyServer) RemoveNetworkHostTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkHostTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, nil)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, request.SourceNetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveOwnedNetworkHostTrafficGrant(ctx, request.PolicyID(),
		request.ClaimID, request.Incarnation); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}
