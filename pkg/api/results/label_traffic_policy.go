package results

import "mvmctl/internal/lib/model"

type VMPolicySelector struct {
	VMID   string                `json:"vm_id,omitempty"`
	Labels model.VMLabelSelector `json:"labels,omitempty"`
}

type LabelTrafficPolicy struct {
	ID                   string                `json:"id"`
	Source               VMPolicySelector      `json:"source"`
	Destination          VMPolicySelector      `json:"destination"`
	Protocol             model.TrafficProtocol `json:"protocol"`
	DestinationPortStart uint16                `json:"destination_port_start"`
	DestinationPortEnd   uint16                `json:"destination_port_end"`
}
