package api

import (
	"context"

	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func (op *Operation) LabelTrafficPolicyEnsure(
	ctx context.Context, input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	request, err := environmentLabelTrafficRequest(input, ownership)
	if err != nil {
		return nil, err
	}
	result, err := network.NewTrafficPolicyClient().ApplyLabelTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	return &results.EnvironmentPolicyReceipt{
		PolicyID: result.PolicyID, Incarnation: result.Incarnation, WasCreated: result.WasCreated,
	}, nil
}

func (op *Operation) LabelTrafficPolicyRemoveOwned(
	ctx context.Context, input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	request, err := environmentLabelTrafficRequest(input, ownership)
	if err != nil {
		return err
	}
	_, err = network.NewTrafficPolicyClient().RemoveLabelTrafficPolicy(ctx, request)
	return err
}

// Workflow state already contains resolved IDs. Root checks the admitted
// reservation, so teardown also works after the VM's user-state row is removed.
func environmentLabelTrafficRequest(
	input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
) (network.LabelTrafficPolicyRequest, error) {
	transport, first, last, err := input.ResolveTraffic()
	if err != nil {
		return network.LabelTrafficPolicyRequest{}, err
	}
	request := network.LabelTrafficPolicyRequest{
		Source:      firewall.VMPolicyEndpoint{VMID: input.Source.VM, Labels: input.Source.Labels},
		Destination: firewall.VMPolicyEndpoint{VMID: input.Destination.VM, Labels: input.Destination.Labels},
		Protocol:    transport, PortStart: first, PortEnd: last,
		ClaimID: ownership.ClaimID, Incarnation: ownership.Incarnation,
	}
	return request, request.Validate()
}
