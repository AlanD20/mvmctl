package results

import "mvmctl/internal/lib/model"

// NetworkToVMTrafficPolicy describes immutable allow intent and current display names.
type NetworkToVMTrafficPolicy struct {
	model.NetworkToVMTrafficAllowIntent
	SourceNetworkName      string                       `json:"source_network_name"`
	DestinationVMName      string                       `json:"destination_vm_name"`
	DestinationNetworkID   string                       `json:"destination_network_id"`
	DestinationNetworkName string                       `json:"destination_network_name"`
	NetworkRelation        model.ManagedNetworkRelation `json:"network_relation"`
}

// VMToVMTrafficPolicy describes exact directional allow intent and current display names.
type VMToVMTrafficPolicy struct {
	model.VMToVMTrafficAllowIntent
	SourceVMName           string                       `json:"source_vm_name"`
	SourceNetworkID        string                       `json:"source_network_id"`
	SourceNetworkName      string                       `json:"source_network_name"`
	DestinationVMName      string                       `json:"destination_vm_name"`
	DestinationNetworkID   string                       `json:"destination_network_id"`
	DestinationNetworkName string                       `json:"destination_network_name"`
	NetworkRelation        model.ManagedNetworkRelation `json:"network_relation"`
}

// TrafficPolicies keeps the two closed traffic source forms distinct in list output.
type TrafficPolicies struct {
	NetworkToVM []*NetworkToVMTrafficPolicy `json:"network_to_vm"`
	VMToVM      []*VMToVMTrafficPolicy      `json:"vm_to_vm"`
}

// Policy describes typed intent with current resource names for display.
type Policy struct {
	ID                     string                            `json:"id"`
	SourceNetworkID        string                            `json:"source_network_id"`
	SourceNetworkName      string                            `json:"source_network_name"`
	DestinationVMID        string                            `json:"destination_vm_id"`
	DestinationVMName      string                            `json:"destination_vm_name"`
	DestinationNetworkID   string                            `json:"destination_network_id"`
	DestinationNetworkName string                            `json:"destination_network_name"`
	Protocol               model.ServiceAccessPolicyProtocol `json:"protocol"`
	DestinationPortStart   int                               `json:"destination_port_start"`
	DestinationPortEnd     int                               `json:"destination_port_end"`
	CreatedAt              string                            `json:"created_at"`
	UpdatedAt              string                            `json:"updated_at"`
}

// PolicySync summarizes a policy reconciliation.
type PolicySync struct {
	Policies int `json:"policies"`
}

// VMToVMExecPolicy describes one directional non-root execution grant with current VM names.
type VMToVMExecPolicy struct {
	ID                string `json:"id"`
	SourceVMID        string `json:"source_vm_id"`
	SourceVMName      string `json:"source_vm_name"`
	DestinationVMID   string `json:"destination_vm_id"`
	DestinationVMName string `json:"destination_vm_name"`
	TargetUser        string `json:"target_user"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}
