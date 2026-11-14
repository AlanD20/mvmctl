package network

import (
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

const (
	// VMHostTrafficPolicyApplyAction installs an exact VM-to-gateway traffic grant.
	VMHostTrafficPolicyApplyAction = "traffic-policy-vm-host-apply"
	// VMHostTrafficPolicyRemoveAction revokes an exact VM-to-gateway traffic grant.
	VMHostTrafficPolicyRemoveAction = "traffic-policy-vm-host-remove"
	// NetworkHostTrafficPolicyApplyAction installs a network-to-own-gateway traffic grant.
	NetworkHostTrafficPolicyApplyAction = "traffic-policy-network-host-apply"
	// NetworkHostTrafficPolicyRemoveAction revokes a network-to-own-gateway traffic grant.
	NetworkHostTrafficPolicyRemoveAction = "traffic-policy-network-host-remove"
)

// VMHostTrafficPolicyRequest selects a source and explicit transport ports; root derives its host gateway.
type VMHostTrafficPolicyRequest struct {
	ClaimID     string                `json:"claim_id,omitempty"`
	Incarnation string                `json:"incarnation,omitempty"`
	SourceVMID  string                `json:"source_vm_id"`
	Protocol    model.TrafficProtocol `json:"protocol"`
	PortStart   uint16                `json:"port_start"`
	PortEnd     uint16                `json:"port_end"`
}

// Validate rejects noncanonical sources and implicit/unbounded transport access before root effects.
func (request VMHostTrafficPolicyRequest) Validate() error {
	if err := validatePolicyOwnership(request.ClaimID, request.Incarnation); err != nil {
		return err
	}
	if !vmIDPattern.MatchString(request.SourceVMID) {
		return errs.New(errs.CodeValidationFailed, "host traffic policy requires a canonical source VM identifier")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

// PolicyID derives identity without accepting a caller-supplied destination or grant ID.
func (request VMHostTrafficPolicyRequest) PolicyID() string {
	return crypto.VMToHostTrafficPolicyDigest(model.VMToHostTrafficAllowIntent{
		SourceVMID: request.SourceVMID, Protocol: request.Protocol,
		DestinationPortStart: int(request.PortStart), DestinationPortEnd: int(request.PortEnd),
	})
}

// NetworkHostTrafficPolicyRequest selects one managed source network and explicit gateway-service ports.
type NetworkHostTrafficPolicyRequest struct {
	ClaimID         string                `json:"claim_id,omitempty"`
	Incarnation     string                `json:"incarnation,omitempty"`
	SourceNetworkID string                `json:"source_network_id"`
	Protocol        model.TrafficProtocol `json:"protocol"`
	PortStart       uint16                `json:"port_start"`
	PortEnd         uint16                `json:"port_end"`
}

// Validate requires a canonical network and explicit TCP/UDP port bounds.
func (request NetworkHostTrafficPolicyRequest) Validate() error {
	if err := validatePolicyOwnership(request.ClaimID, request.Incarnation); err != nil {
		return err
	}
	if !networkIDPattern.MatchString(request.SourceNetworkID) {
		return errs.New(errs.CodeValidationFailed, "host traffic policy requires a canonical source network identifier")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

// PolicyID derives the network-to-host policy identity independently of incidental gateway addresses.
func (request NetworkHostTrafficPolicyRequest) PolicyID() string {
	return crypto.NetworkToHostTrafficPolicyDigest(model.NetworkToHostTrafficAllowIntent{
		SourceNetworkID: request.SourceNetworkID, Protocol: request.Protocol,
		DestinationPortStart: int(request.PortStart), DestinationPortEnd: int(request.PortEnd),
	})
}
