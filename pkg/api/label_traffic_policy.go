package api

import (
	"context"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

type LabelTrafficPolicyAPI interface {
	LabelTrafficPolicyCreate(context.Context, inputs.LabelTrafficPolicyCreateInput) (results.LabelTrafficPolicy, error)
	LabelTrafficPolicyList(context.Context) ([]results.LabelTrafficPolicy, error)
	LabelTrafficPolicyRemove(context.Context, inputs.LabelTrafficPolicyInput) error
}

func (op *Operation) LabelTrafficPolicyCreate(
	ctx context.Context, input inputs.LabelTrafficPolicyCreateInput,
) (results.LabelTrafficPolicy, error) {
	request, err := op.resolveLabelTrafficPolicy(ctx, input)
	if err != nil {
		return results.LabelTrafficPolicy{}, err
	}
	reply, err := network.NewTrafficPolicyClient().ApplyLabelTrafficPolicy(ctx, request)
	if err != nil {
		return results.LabelTrafficPolicy{}, err
	}
	return labelTrafficPolicyResult(reply.PolicyID, request), nil
}

func (op *Operation) resolveLabelTrafficPolicy(
	ctx context.Context, input inputs.LabelTrafficPolicyCreateInput,
) (network.LabelTrafficPolicyRequest, error) {
	transport, first, last, err := input.ResolveTraffic()
	if err != nil {
		return network.LabelTrafficPolicyRequest{}, err
	}
	source, err := op.resolveVMPolicySelector(ctx, input.Source)
	if err != nil {
		return network.LabelTrafficPolicyRequest{}, err
	}
	destination, err := op.resolveVMPolicySelector(ctx, input.Destination)
	if err != nil {
		return network.LabelTrafficPolicyRequest{}, err
	}
	return network.LabelTrafficPolicyRequest{
		Source: source, Destination: destination, Protocol: transport, PortStart: first, PortEnd: last,
	}, nil
}

func (op *Operation) resolveVMPolicySelector(
	ctx context.Context, input inputs.VMPolicySelectorInput,
) (firewall.VMPolicyEndpoint, error) {
	if input.VM == "" {
		return firewall.VMPolicyEndpoint{Labels: input.Labels}, nil
	}
	vm, err := (inputs.VMLabelInput{VM: input.VM}).Resolve(ctx, op.Repos.VM)
	if err != nil {
		return firewall.VMPolicyEndpoint{}, err
	}
	return firewall.VMPolicyEndpoint{VMID: vm.ID}, nil
}

func (op *Operation) LabelTrafficPolicyList(ctx context.Context) ([]results.LabelTrafficPolicy, error) {
	policies, err := network.NewTrafficPolicyClient().ListLabelTrafficPolicies(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]results.LabelTrafficPolicy, 0, len(policies))
	for _, policy := range policies {
		result = append(result, labelTrafficPolicyResult(policy.ID, labelTrafficPolicyRequest(policy)))
	}
	return result, nil
}

func (op *Operation) LabelTrafficPolicyRemove(ctx context.Context, input inputs.LabelTrafficPolicyInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	client := network.NewTrafficPolicyClient()
	policies, err := client.ListLabelTrafficPolicies(ctx)
	if err != nil {
		return err
	}
	resolved := make(map[string]firewall.LabelTrafficPolicy, len(input.Identifiers))
	for _, identifier := range input.Identifiers {
		var match firewall.LabelTrafficPolicy
		for _, policy := range policies {
			if !strings.HasPrefix(policy.ID, identifier) {
				continue
			}
			if match.ID != "" {
				return errs.New(errs.CodePolicyAmbiguous, "ambiguous label traffic policy", errs.WithEntity(identifier))
			}
			match = policy
		}
		if match.ID == "" {
			return errs.NotFound(errs.CodePolicyNotFound, "label traffic policy not found", errs.WithEntity(identifier))
		}
		resolved[match.ID] = match
	}
	for _, policy := range policies {
		if _, found := resolved[policy.ID]; !found {
			continue
		}
		request := labelTrafficPolicyRequest(policy)
		request.Incarnation = policy.Incarnation
		request.ClaimID = policy.ClaimID
		if _, err := client.RemoveLabelTrafficPolicy(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func labelTrafficPolicyRequest(policy firewall.LabelTrafficPolicy) network.LabelTrafficPolicyRequest {
	transport := model.TrafficProtocolTCP
	if policy.Protocol == firewall.UDP {
		transport = model.TrafficProtocolUDP
	}
	return network.LabelTrafficPolicyRequest{
		Source: policy.Source, Destination: policy.Destination, Protocol: transport,
		PortStart: policy.Ports.First, PortEnd: policy.Ports.Last,
	}
}

func labelTrafficPolicyResult(id string, request network.LabelTrafficPolicyRequest) results.LabelTrafficPolicy {
	return results.LabelTrafficPolicy{
		ID: id, Protocol: request.Protocol, DestinationPortStart: request.PortStart, DestinationPortEnd: request.PortEnd,
		Source:      results.VMPolicySelector{VMID: request.Source.VMID, Labels: request.Source.Labels},
		Destination: results.VMPolicySelector{VMID: request.Destination.VMID, Labels: request.Destination.Labels},
	}
}
