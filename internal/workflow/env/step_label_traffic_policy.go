package env

import (
	"context"
	"maps"

	"mvmctl/pkg/api/inputs"
)

func (output *TrafficPolicyState) usesLabels() bool {
	return output.SourceLabels != nil || output.DestinationLabels != nil
}

func (output *TrafficPolicyState) labelInput() inputs.LabelTrafficPolicyCreateInput {
	return inputs.LabelTrafficPolicyCreateInput{
		Source:      inputs.VMPolicySelectorInput{VM: output.SourceVMID, Labels: output.SourceLabels},
		Destination: inputs.VMPolicySelectorInput{VM: output.DestinationVMID, Labels: output.DestinationLabels},
		Protocol:    output.Protocol, DestinationPort: output.Port,
	}
}

func (s *TrafficPolicyStep) inspectExistingLabelPolicy(ctx context.Context, output *TrafficPolicyState) error {
	transport, first, last, err := output.labelInput().ResolveTraffic()
	if err != nil {
		return err
	}
	policies, err := s.op.LabelTrafficPolicyList(ctx)
	if err != nil {
		return err
	}
	for _, item := range policies {
		if item.ID == s.input.Existing && item.Protocol == transport &&
			item.DestinationPortStart == first && item.DestinationPortEnd == last &&
			item.Source.VMID == output.SourceVMID && maps.Equal(item.Source.Labels, output.SourceLabels) &&
			item.Destination.VMID == output.DestinationVMID && maps.Equal(item.Destination.Labels, output.DestinationLabels) {
			return nil
		}
	}
	return resourceIdentityMismatch(s.Name())
}
