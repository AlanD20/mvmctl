package env

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

func (output *TrafficPolicyState) networkHostInput() inputs.NetworkToHostTrafficPolicyCreateInput {
	return inputs.NetworkToHostTrafficPolicyCreateInput{
		SourceNetwork: output.SourceNetworkID, Protocol: output.Protocol, DestinationPort: output.Port,
	}
}

func (output *TrafficPolicyState) vmHostInput() inputs.VMToHostTrafficPolicyCreateInput {
	return inputs.VMToHostTrafficPolicyCreateInput{
		SourceVM: output.SourceVMID, Protocol: output.Protocol, DestinationPort: output.Port,
	}
}

func (s *TrafficPolicyStep) inspectExistingHostPolicy(
	ctx context.Context, output *TrafficPolicyState, protocol model.TrafficProtocol, first, last int,
) error {
	if output.SourceNetworkID != "" {
		item, err := s.op.NetworkToHostTrafficPolicyInspect(ctx,
			inputs.NetworkToHostTrafficPolicyInput{Identifiers: []string{s.input.Existing}})
		if err != nil {
			return err
		}
		if item == nil || item.ID != s.input.Existing || item.SourceNetworkID != output.SourceNetworkID ||
			item.Protocol != protocol || item.DestinationPortStart != first || item.DestinationPortEnd != last {
			return resourceIdentityMismatch(s.Name())
		}
		return nil
	}
	item, err := s.op.VMToHostTrafficPolicyInspect(ctx,
		inputs.VMToHostTrafficPolicyInput{Identifiers: []string{s.input.Existing}})
	if err != nil {
		return err
	}
	if item == nil || item.ID != s.input.Existing || item.SourceVMID != output.SourceVMID ||
		item.Protocol != protocol || item.DestinationPortStart != first || item.DestinationPortEnd != last {
		return resourceIdentityMismatch(s.Name())
	}
	return nil
}
