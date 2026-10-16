package network

import (
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

const (
	// NetworkTrafficPolicyApplyAction is the fixed network-source policy installation action.
	NetworkTrafficPolicyApplyAction = "traffic-policy-network-apply"
	// VMTrafficPolicyApplyAction is the fixed exact-VM policy installation action.
	VMTrafficPolicyApplyAction = "traffic-policy-vm-apply"
	// NetworkTrafficPolicyRemoveAction is the fixed network-source policy revocation action.
	NetworkTrafficPolicyRemoveAction = "traffic-policy-network-remove"
	// VMTrafficPolicyRemoveAction is the fixed exact-VM policy revocation action.
	VMTrafficPolicyRemoveAction = "traffic-policy-vm-remove"
)

// NetworkTrafficPolicyRequest contains only a closed grant identity; root resolves ownership and network facts.
type NetworkTrafficPolicyRequest struct {
	SourceNetworkID string                `json:"source_network_id"`
	DestinationVMID string                `json:"destination_vm_id"`
	Protocol        model.TrafficProtocol `json:"protocol"`
	PortStart       uint16                `json:"port_start"`
	PortEnd         uint16                `json:"port_end"`
}

// Validate admits canonical immutable identifiers and a bounded transport grant.
func (request NetworkTrafficPolicyRequest) Validate() error {
	if !networkIDPattern.MatchString(request.SourceNetworkID) || !vmIDPattern.MatchString(request.DestinationVMID) {
		return errs.New(errs.CodeValidationFailed, "traffic policy requires canonical network and VM identifiers")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

// PolicyID derives the identity instead of accepting a caller-selected policy identifier.
func (request NetworkTrafficPolicyRequest) PolicyID() string {
	return crypto.NetworkToVMTrafficPolicyDigest(model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID:      request.SourceNetworkID,
		DestinationVMID:      request.DestinationVMID,
		Protocol:             request.Protocol,
		DestinationPortStart: int(request.PortStart),
		DestinationPortEnd:   int(request.PortEnd),
	})
}

// VMTrafficPolicyRequest grants one exact directional VM pair without any caller-supplied network placement.
type VMTrafficPolicyRequest struct {
	SourceVMID      string                `json:"source_vm_id"`
	DestinationVMID string                `json:"destination_vm_id"`
	Protocol        model.TrafficProtocol `json:"protocol"`
	PortStart       uint16                `json:"port_start"`
	PortEnd         uint16                `json:"port_end"`
}

// Validate admits distinct immutable VM identities and bounded transport intent.
func (request VMTrafficPolicyRequest) Validate() error {
	if !vmIDPattern.MatchString(request.SourceVMID) || !vmIDPattern.MatchString(request.DestinationVMID) ||
		request.SourceVMID == request.DestinationVMID {
		return errs.New(errs.CodeValidationFailed, "traffic policy requires distinct canonical VM identifiers")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

// PolicyID derives the exact directional grant identity.
func (request VMTrafficPolicyRequest) PolicyID() string {
	return crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
		SourceVMID:           request.SourceVMID,
		DestinationVMID:      request.DestinationVMID,
		Protocol:             request.Protocol,
		DestinationPortStart: int(request.PortStart),
		DestinationPortEnd:   int(request.PortEnd),
	})
}

// TrafficPolicyMutationResult is returned only after root verifies the complete kernel generation.
type TrafficPolicyMutationResult struct {
	PolicyID string `json:"policy_id"`
}

// Validate requires the receiver to acknowledge exactly the requested immutable grant.
func (result TrafficPolicyMutationResult) Validate(expectedID string) error {
	if result.PolicyID != expectedID || len(expectedID) != 64 {
		return errs.New(errs.CodePolicySyncFailed, "root traffic policy result does not match the requested grant")
	}
	return nil
}

func validateTrafficPolicyPorts(protocol model.TrafficProtocol, first, last uint16) error {
	if protocol != model.TrafficProtocolTCP && protocol != model.TrafficProtocolUDP {
		return errs.New(errs.CodePolicyProtocolInvalid, "traffic policy protocol must be tcp or udp")
	}
	if first == 0 || last < first {
		return errs.New(errs.CodePolicyPortInvalid, "traffic policy port range must be within 1 through 65535")
	}
	return nil
}
