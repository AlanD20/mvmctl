package env

import (
	"context"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func (s *TrafficPolicyStep) resolveFloatingIPDestination(ctx context.Context, state *workflow.SharedState,
	output *TrafficPolicyState) error {
	network, err := resourceStepReference(state, s.input.DestinationNetwork, "network")
	if err != nil {
		return err
	}
	networkItem, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{network}})
	if err != nil {
		return err
	}
	if networkItem == nil || (strings.HasPrefix(s.input.DestinationNetwork, "@") && networkItem.ID != network) {
		return resourceIdentityMismatch(s.Name())
	}
	resource, err := resourceStepReference(state, s.input.DestinationFloatingIP, "floating_ip")
	if err != nil {
		return err
	}
	item, err := s.op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: networkItem.ID, Identifier: resource})
	if err != nil {
		return err
	}
	if item == nil || item.Retired || item.NetworkID != networkItem.ID ||
		(strings.HasPrefix(s.input.DestinationFloatingIP, "@") && item.ID != resource) {
		return resourceIdentityMismatch(s.Name())
	}
	output.DestinationNetworkID, output.DestinationFloatingIPID = item.NetworkID, item.ID
	return nil
}

func (output *TrafficPolicyState) vmFloatingIPInput() inputs.VMToFloatingIPTrafficPolicyCreateInput {
	return inputs.VMToFloatingIPTrafficPolicyCreateInput{SourceVM: output.SourceVMID,
		DestinationFloatingIP: output.DestinationFloatingIPID, DestinationNetwork: output.DestinationNetworkID,
		Protocol: output.Protocol, DestinationPort: output.Port}
}

func (output *TrafficPolicyState) networkFloatingIPInput() inputs.NetworkToFloatingIPTrafficPolicyCreateInput {
	return inputs.NetworkToFloatingIPTrafficPolicyCreateInput{SourceNetwork: output.SourceNetworkID,
		DestinationFloatingIP: output.DestinationFloatingIPID, DestinationNetwork: output.DestinationNetworkID,
		Protocol: output.Protocol, DestinationPort: output.Port}
}

func validateFloatingIPTrafficDeclaration(input trafficPolicyDeclaration) error {
	if input.SourceNetwork != "" {
		return (&inputs.NetworkToFloatingIPTrafficPolicyCreateInput{SourceNetwork: input.SourceNetwork,
			DestinationFloatingIP: input.DestinationFloatingIP, DestinationNetwork: input.DestinationNetwork,
			Protocol: input.Protocol, DestinationPort: input.Port}).Validate()
	}
	return (&inputs.VMToFloatingIPTrafficPolicyCreateInput{SourceVM: input.SourceVM,
		DestinationFloatingIP: input.DestinationFloatingIP, DestinationNetwork: input.DestinationNetwork,
		Protocol: input.Protocol, DestinationPort: input.Port}).Validate()
}

func (s *TrafficPolicyStep) ensureFloatingIPPolicy(ctx context.Context,
	output *TrafficPolicyState) (*results.EnvironmentPolicyReceipt, error) {
	ownership := inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation}
	if output.SourceNetworkID != "" {
		return s.op.NetworkToFloatingIPTrafficPolicyEnsure(ctx, output.networkFloatingIPInput(), ownership)
	}
	return s.op.VMToFloatingIPTrafficPolicyEnsure(ctx, output.vmFloatingIPInput(), ownership)
}

func (s *TrafficPolicyStep) removeOwnedFloatingIPPolicy(ctx context.Context, output *TrafficPolicyState) error {
	ownership := inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation}
	if output.SourceNetworkID != "" {
		return s.op.NetworkToFloatingIPTrafficPolicyRemoveOwned(ctx, output.networkFloatingIPInput(), ownership)
	}
	return s.op.VMToFloatingIPTrafficPolicyRemoveOwned(ctx, output.vmFloatingIPInput(), ownership)
}

func (s *TrafficPolicyStep) inspectExistingFloatingIPPolicy(ctx context.Context, output *TrafficPolicyState,
	protocol model.TrafficProtocol, first, last int) error {
	if output.SourceNetworkID != "" {
		item, err := s.op.NetworkToFloatingIPTrafficPolicyInspect(ctx,
			inputs.NetworkToFloatingIPTrafficPolicyInput{Identifiers: []string{s.input.Existing}})
		if err != nil {
			return err
		}
		if item == nil || item.ID != s.input.Existing || item.SourceNetworkID != output.SourceNetworkID ||
			item.DestinationFloatingIPID != output.DestinationFloatingIPID || item.DestinationNetworkID != output.DestinationNetworkID ||
			item.Protocol != protocol ||
			item.DestinationPortStart != first || item.DestinationPortEnd != last {
			return resourceIdentityMismatch(s.Name())
		}
		return nil
	}
	item, err := s.op.VMToFloatingIPTrafficPolicyInspect(ctx,
		inputs.VMToFloatingIPTrafficPolicyInput{Identifiers: []string{s.input.Existing}})
	if err != nil {
		return err
	}
	if item == nil || item.ID != s.input.Existing || item.SourceVMID != output.SourceVMID ||
		item.DestinationFloatingIPID != output.DestinationFloatingIPID || item.DestinationNetworkID != output.DestinationNetworkID ||
		item.Protocol != protocol ||
		item.DestinationPortStart != first || item.DestinationPortEnd != last {
		return resourceIdentityMismatch(s.Name())
	}
	return nil
}
