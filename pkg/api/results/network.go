package results

// NetworkLease is a single lease entry in the network inspect response.
type NetworkLease struct {
	ID        any    `json:"id"`
	VMID      any    `json:"vm_id"`
	IPv4      string `json:"ipv4"`
	LeasedAt  string `json:"leased_at"`
	ExpiresAt any    `json:"expires_at"`
}

// NetworkItemInfo groups network metadata in an inspect response.
type NetworkItemInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Subnet      string `json:"subnet"`
	Bridge      string `json:"bridge"`
	IPv4Gateway string `json:"ipv4_gateway"`
	IsDefault   bool   `json:"is_default"`
	IsPresent   bool   `json:"is_present"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// NetworkStatusInfo groups network status in an inspect response.
type NetworkStatusInfo struct {
	BridgeActive bool `json:"bridge_active"`
	IsPresent    bool `json:"is_present"`
	IsDefault    bool `json:"is_default"`
}

// NetworkNATInfo groups network NAT info in an inspect response.
type NetworkNATInfo struct {
	NATEnabled  bool     `json:"nat_enabled"`
	NATGateways []string `json:"nat_gateways"`
}

// NetworkInspect is the structured response for network inspection.
type NetworkInspect struct {
	Network NetworkItemInfo   `json:"network"`
	Status  NetworkStatusInfo `json:"status"`
	NAT     NetworkNATInfo    `json:"nat"`
	Leases  []NetworkLease    `json:"leases"`
}
