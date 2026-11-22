package network

import (
	"context"
	"os"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/firewall"
)

const (
	LabelTrafficPolicyApplyAction  = "traffic-policy-label-apply"
	LabelTrafficPolicyRemoveAction = "traffic-policy-label-remove"
	LabelTrafficPolicyListAction   = "traffic-policy-label-list"
)

// LabelTrafficPolicyRequest accepts selectors and immutable VM identities.
type LabelTrafficPolicyRequest struct {
	Source      firewall.VMPolicyEndpoint `json:"source"`
	Destination firewall.VMPolicyEndpoint `json:"destination"`
	Protocol    model.TrafficProtocol     `json:"protocol"`
	PortStart   uint16                    `json:"port_start"`
	PortEnd     uint16                    `json:"port_end"`
	ClaimID     string                    `json:"claim_id,omitempty"`
	Incarnation string                    `json:"incarnation,omitempty"`
}

func (request LabelTrafficPolicyRequest) Validate() error {
	if (request.ClaimID != "" && !policyOwnershipToken.MatchString(request.ClaimID)) ||
		(request.Incarnation != "" && !policyOwnershipToken.MatchString(request.Incarnation)) {
		return networkAuthorityValidationError("invalid label traffic policy ownership tokens")
	}
	if request.Source.VMID != "" && request.Destination.VMID != "" {
		return networkAuthorityValidationError("traffic policy requires at least one label selector")
	}
	for _, endpoint := range []firewall.VMPolicyEndpoint{request.Source, request.Destination} {
		if endpoint.VMID == "" {
			if err := endpoint.Labels.Validate(); err != nil {
				return err
			}
		} else if endpoint.Labels != nil || !vmIDPattern.MatchString(endpoint.VMID) {
			return networkAuthorityValidationError("exact traffic endpoint requires a canonical VM ID")
		}
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

func (request LabelTrafficPolicyRequest) policyID(ownerUID uint32) (string, error) {
	return firewall.LabelTrafficPolicyID(
		ownerUID, request.Source, request.Destination, trafficFirewallProtocol(request.Protocol), request.ports(),
	)
}

func (request LabelTrafficPolicyRequest) ports() firewall.PortInterval {
	return firewall.PortInterval{First: request.PortStart, Last: request.PortEnd}
}

func (client *TrafficPolicyClient) ApplyLabelTrafficPolicy(
	ctx context.Context, request LabelTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	id, err := request.policyID(uint32(os.Getuid()))
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, LabelTrafficPolicyApplyAction, request, id)
}

func (client *TrafficPolicyClient) RemoveLabelTrafficPolicy(
	ctx context.Context, request LabelTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	id, err := request.policyID(uint32(os.Getuid()))
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, LabelTrafficPolicyRemoveAction, request, id)
}

func (server *TrafficPolicyServer) ApplyLabelTrafficPolicy(
	ctx context.Context, ownerUID uint32, request LabelTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	policy, receipt, err := firewall.EnsureLabelTrafficPolicy(
		ctx, ownerUID, request.Source, request.Destination, trafficFirewallProtocol(request.Protocol), request.ports(),
		request.ClaimID, request.Incarnation,
	)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{
		PolicyID: policy.ID, ClaimID: receipt.ClaimID, Incarnation: receipt.Incarnation, WasCreated: receipt.WasCreated,
	}, nil
}

func (server *TrafficPolicyServer) RemoveLabelTrafficPolicy(
	ctx context.Context, ownerUID uint32, request LabelTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	id, err := request.policyID(ownerUID)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := firewall.RemoveLabelTrafficPolicy(ctx, ownerUID, id, request.ClaimID, request.Incarnation); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: id}, nil
}

type LabelTrafficPolicyListRequest struct{}

type LabelTrafficPolicyListResult struct {
	Policies []firewall.LabelTrafficPolicy `json:"policies"`
}

func (client *TrafficPolicyClient) ListLabelTrafficPolicies(
	ctx context.Context,
) ([]firewall.LabelTrafficPolicy, error) {
	result, err := exchangeTrafficPolicyFrame[LabelTrafficPolicyListResult](
		ctx, client, LabelTrafficPolicyListAction, LabelTrafficPolicyListRequest{},
	)
	if err != nil {
		return nil, err
	}
	ownerUID := uint32(os.Getuid())
	for _, policy := range result.Policies {
		id, err := firewall.LabelTrafficPolicyID(policy.OwnerUID, policy.Source, policy.Destination,
			policy.Protocol, policy.Ports)
		if err != nil {
			return nil, trafficPolicyResponseError(true, err)
		}
		if policy.OwnerUID != ownerUID || policy.ID != id {
			return nil, trafficPolicyResponseError(true, networkAuthorityValidationError("invalid label policy listing"))
		}
	}
	return result.Policies, nil
}

func (server *TrafficPolicyServer) ListLabelTrafficPolicies(
	ctx context.Context, ownerUID uint32,
) (LabelTrafficPolicyListResult, error) {
	policies, err := firewall.ListLabelTrafficPolicies(ctx, ownerUID)
	return LabelTrafficPolicyListResult{Policies: policies}, err
}
