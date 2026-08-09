package results

import "mvmctl/internal/lib/model"

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
