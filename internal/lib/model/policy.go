package model

// TrafficProtocol is the only transport protocol granted by traffic policies.
type TrafficProtocol string

const (
	// TrafficProtocolTCP grants TCP traffic.
	TrafficProtocolTCP TrafficProtocol = "tcp"
	// TrafficProtocolUDP grants UDP traffic.
	TrafficProtocolUDP TrafficProtocol = "udp"
)

// NetworkTrafficAllowIntent stores a grant from one managed network to one exact VM.
// It is desired policy state, not a compiled firewall rule or applied generation.
type NetworkTrafficAllowIntent struct {
	ID                   string          `json:"id"                     db:"id"`
	SourceNetworkID      string          `json:"source_network_id"      db:"source_network_id"`
	DestinationVMID      string          `json:"destination_vm_id"      db:"destination_vm_id"`
	Protocol             TrafficProtocol `json:"protocol"               db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end"   db:"destination_port_end"`
	CreatedAt            string          `json:"created_at"             db:"created_at"`
	UpdatedAt            string          `json:"updated_at"             db:"updated_at"`
}

// VMTrafficAllowIntent stores a grant from one exact VM to another exact VM.
// It is desired policy state, not a compiled firewall rule or applied generation.
type VMTrafficAllowIntent struct {
	ID                   string          `json:"id"                     db:"id"`
	SourceVMID           string          `json:"source_vm_id"           db:"source_vm_id"`
	DestinationVMID      string          `json:"destination_vm_id"      db:"destination_vm_id"`
	Protocol             TrafficProtocol `json:"protocol"               db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end"   db:"destination_port_end"`
	CreatedAt            string          `json:"created_at"             db:"created_at"`
	UpdatedAt            string          `json:"updated_at"             db:"updated_at"`
}

// ExecAllowIntent stores a directional VM-to-VM execution grant for one exact non-root user.
type ExecAllowIntent struct {
	ID              string `json:"id"                db:"id"`
	SourceVMID      string `json:"source_vm_id"      db:"source_vm_id"`
	DestinationVMID string `json:"destination_vm_id" db:"destination_vm_id"`
	TargetUser      string `json:"target_user"       db:"target_user"`
	CreatedAt       string `json:"created_at"        db:"created_at"`
	UpdatedAt       string `json:"updated_at"        db:"updated_at"`
}
