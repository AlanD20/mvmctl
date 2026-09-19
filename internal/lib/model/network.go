package model

// --- Network ---

// NetworkItem represents a VM network.
type NetworkItem struct {
	ID           string  `json:"id"                     db:"id"`
	Name         string  `json:"name"                   db:"name"`
	Subnet       string  `json:"subnet"                 db:"subnet"`
	Bridge       string  `json:"bridge"                 db:"bridge"`
	IPv4Gateway  string  `json:"ipv4_gateway"           db:"ipv4_gateway"`
	BridgeActive bool    `json:"bridge_active"          db:"bridge_active"`
	NATEnabled   bool    `json:"nat_enabled"            db:"nat_enabled"`
	IsDefault    bool    `json:"is_default"             db:"is_default"`
	IsPresent    bool    `json:"is_present"             db:"is_present"`
	CreatedAt    string  `json:"created_at"             db:"created_at"`
	UpdatedAt    string  `json:"updated_at"             db:"updated_at"`
	DeletedAt    *string `json:"deleted_at,omitempty"   db:"deleted_at"`

	// Resolved relations (not stored in DB directly)
	Leases    []*NetworkLeaseItem `json:"leases,omitempty"`
	VMs       []*VMItem           `json:"vms,omitempty"`
	Snapshots []*SnapshotItem     `json:"snapshots,omitempty"`
}

// --- NetworkLeaseItem ---

// NetworkLeaseItem represents an IP lease.
type NetworkLeaseItem struct {
	NetworkID string  `json:"network_id"           db:"network_id"`
	IPv4      string  `json:"ipv4"                 db:"ipv4"`
	LeasedAt  string  `json:"leased_at"            db:"leased_at"`
	ID        *int64  `json:"id,omitempty"         db:"id"`
	VMID      *string `json:"vm_id,omitempty"      db:"vm_id"`
	ExpiresAt *string `json:"expires_at,omitempty" db:"expires_at"`
}
