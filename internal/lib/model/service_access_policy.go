package model

// ServiceAccessPolicyProtocol is the transport protocol exposed by a policy.
type ServiceAccessPolicyProtocol string

const (
	ServiceAccessPolicyProtocolTCP ServiceAccessPolicyProtocol = "tcp"
	ServiceAccessPolicyProtocolUDP ServiceAccessPolicyProtocol = "udp"
)

// ServiceAccessPolicy persists routed service-access intent using resource identities only.
type ServiceAccessPolicy struct {
	ID                   string                      `json:"id"                     db:"id"`
	SourceNetworkID      string                      `json:"source_network_id"      db:"source_network_id"`
	DestinationVMID      string                      `json:"destination_vm_id"      db:"destination_vm_id"`
	Protocol             ServiceAccessPolicyProtocol `json:"protocol"               db:"protocol"`
	DestinationPortStart int                         `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int                         `json:"destination_port_end"   db:"destination_port_end"`
	CreatedAt            string                      `json:"created_at"             db:"created_at"`
	UpdatedAt            string                      `json:"updated_at"             db:"updated_at"`
}

// ResolvedServiceAccessPolicy contains API-resolved current resource attributes used during compilation.
// The additional fields are runtime-only and are never persisted as policy intent.
type ResolvedServiceAccessPolicy struct {
	ServiceAccessPolicy
	SourceNetworkName        string `json:"source_network_name"      db:"source_network_name"`
	SourceNetworkBridge      string `json:"-"                        db:"source_network_bridge"`
	DestinationVMName        string `json:"destination_vm_name"      db:"destination_vm_name"`
	DestinationIPv4          string `json:"-"                        db:"destination_ipv4"`
	DestinationNetworkID     string `json:"destination_network_id"   db:"destination_network_id"`
	DestinationNetworkName   string `json:"destination_network_name" db:"destination_network_name"`
	DestinationNetworkBridge string `json:"-"                      db:"destination_network_bridge"`
}
