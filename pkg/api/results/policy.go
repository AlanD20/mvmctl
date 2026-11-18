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

// TrafficPolicies keeps closed source and destination forms distinct in list output.
type TrafficPolicies struct {
	VMToFloatingIP      []*VMToFloatingIPTrafficPolicy      `json:"vm_to_floating_ip"`
	NetworkToFloatingIP []*NetworkToFloatingIPTrafficPolicy `json:"network_to_floating_ip"`
	NetworkToVM         []*NetworkToVMTrafficPolicy         `json:"network_to_vm"`
	VMToVM              []*VMToVMTrafficPolicy              `json:"vm_to_vm"`
	NetworkToHost       []*NetworkToHostTrafficPolicy       `json:"network_to_host"`
	VMToHost            []*VMToHostTrafficPolicy            `json:"vm_to_host"`
}

// VMToHostTrafficPolicy describes a gateway grant; Gateway is display data, not mutation authority.
type VMToHostTrafficPolicy struct {
	model.VMToHostTrafficAllowIntent
	SourceVMName      string `json:"source_vm_name"`
	SourceNetworkID   string `json:"source_network_id"`
	SourceNetworkName string `json:"source_network_name"`
	Gateway           string `json:"gateway"`
}

// NetworkToHostTrafficPolicy describes access to the source managed network's own host gateway.
type NetworkToHostTrafficPolicy struct {
	model.NetworkToHostTrafficAllowIntent
	SourceNetworkName string `json:"source_network_name"`
	Gateway           string `json:"gateway"`
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

// VMToFloatingIPTrafficPolicy describes exact directional allow intent and current display names.
type VMToFloatingIPTrafficPolicy struct {
	model.VMToFloatingIPTrafficAllowIntent
	SourceVMName              string                       `json:"source_vm_name"`
	SourceNetworkID           string                       `json:"source_network_id"`
	SourceNetworkName         string                       `json:"source_network_name"`
	DestinationFloatingIPName string                       `json:"destination_floating_ip_name"`
	DestinationNetworkID      string                       `json:"destination_network_id"`
	DestinationNetworkName    string                       `json:"destination_network_name"`
	DestinationIPv4           string                       `json:"destination_ipv4"`
	NetworkRelation           model.ManagedNetworkRelation `json:"network_relation"`
}

// NetworkToFloatingIPTrafficPolicy describes immutable allow intent and current display names.
type NetworkToFloatingIPTrafficPolicy struct {
	model.NetworkToFloatingIPTrafficAllowIntent
	SourceNetworkName         string                       `json:"source_network_name"`
	DestinationFloatingIPName string                       `json:"destination_floating_ip_name"`
	DestinationNetworkID      string                       `json:"destination_network_id"`
	DestinationNetworkName    string                       `json:"destination_network_name"`
	DestinationIPv4           string                       `json:"destination_ipv4"`
	NetworkRelation           model.ManagedNetworkRelation `json:"network_relation"`
}
