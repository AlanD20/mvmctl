package model

// VMToHostTrafficAllowIntent grants a VM access to explicitly selected ports on its managed host gateway.
// Host addresses remain root-derived runtime state, not persisted policy identity.
type VMToHostTrafficAllowIntent struct {
	ClaimID              string          `json:"claim_id,omitempty" db:"claim_id"`
	Incarnation          string          `json:"incarnation,omitempty" db:"incarnation"`
	ID                   string          `json:"id" db:"id"`
	SourceVMID           string          `json:"source_vm_id" db:"source_vm_id"`
	Protocol             TrafficProtocol `json:"protocol" db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end" db:"destination_port_end"`
	CreatedAt            string          `json:"created_at" db:"created_at"`
	UpdatedAt            string          `json:"updated_at" db:"updated_at"`
}

// NetworkToHostTrafficAllowIntent grants a managed network access to explicitly selected ports on its host gateway.
type NetworkToHostTrafficAllowIntent struct {
	ClaimID              string          `json:"claim_id,omitempty" db:"claim_id"`
	Incarnation          string          `json:"incarnation,omitempty" db:"incarnation"`
	ID                   string          `json:"id" db:"id"`
	SourceNetworkID      string          `json:"source_network_id" db:"source_network_id"`
	Protocol             TrafficProtocol `json:"protocol" db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end" db:"destination_port_end"`
	CreatedAt            string          `json:"created_at" db:"created_at"`
	UpdatedAt            string          `json:"updated_at" db:"updated_at"`
}
