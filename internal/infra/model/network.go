package model

// ── Firewall enums ──

// FirewallBackendType selects the firewall implementation.
type FirewallBackendType string

const (
	FirewallBackendNFTables FirewallBackendType = "nftables"
	FirewallBackendIPTables FirewallBackendType = "iptables"
)

// FirewallTable names.
type FirewallTable string

const (
	FirewallTableFilter   FirewallTable = "filter"
	FirewallTableNat      FirewallTable = "nat"
	FirewallTableMangle   FirewallTable = "mangle"
	FirewallTableRaw      FirewallTable = "raw"
	FirewallTableSecurity FirewallTable = "security"
)

// FirewallChain names.
type FirewallChain string

// FirewallRuleType categorises firewall rules.
type FirewallRuleType string

const (
	FirewallRuleTypeMasquerade      FirewallRuleType = "masquerade"
	FirewallRuleTypeForwardIn       FirewallRuleType = "forward_in"
	FirewallRuleTypeForwardOut      FirewallRuleType = "forward_out"
	FirewallRuleTypeNocloudNetInput FirewallRuleType = "nocloudnet_input"
)

// FirewallProtocol specifies the IP protocol.
type FirewallProtocol string

const (
	FirewallProtocolTCP  FirewallProtocol = "tcp"
	FirewallProtocolUDP  FirewallProtocol = "udp"
	FirewallProtocolICMP FirewallProtocol = "icmp"
	FirewallProtocolAll  FirewallProtocol = "all"
)

// FirewallTarget specifies the firewall action.
type FirewallTarget string

const (
	FirewallTargetMasquerade FirewallTarget = "MASQUERADE"
	FirewallTargetAccept     FirewallTarget = "ACCEPT"
	FirewallTargetDrop       FirewallTarget = "DROP"
	FirewallTargetReject     FirewallTarget = "REJECT"
	FirewallTargetLog        FirewallTarget = "LOG"
	FirewallTargetMark       FirewallTarget = "MARK"
)

// FirewallWildcard constants.
type FirewallWildcard string

const (
	FirewallWildcardAnyCIDR      FirewallWildcard = "0.0.0.0/0"
	FirewallWildcardAnyInterface FirewallWildcard = "*"
)

// FirewallPortAny is the sentinel value meaning "any port".
const FirewallPortAny = 0

// ── Network ──

// Network matches Python's NetworkItem dataclass exactly.
type Network struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Subnet       string  `json:"subnet"`
	Bridge       string  `json:"bridge"`
	IPv4Gateway  string  `json:"ipv4_gateway"`
	BridgeActive bool    `json:"bridge_active"`
	NATEnabled   bool    `json:"nat_enabled"`
	IsDefault    bool    `json:"is_default"`
	IsPresent    bool    `json:"is_present"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	DeletedAt    *string `json:"deleted_at,omitempty"`
	NATGateways  *string `json:"nat_gateways,omitempty"`

	// Resolved relations (not stored in DB directly)
	Leases        []*NetworkLeaseItem `json:"leases,omitempty"`
	IPTablesRules []*FirewallRule     `json:"iptables_rules,omitempty"`
	VMs           []*VM               `json:"vms,omitempty"`
}

// ── NetworkLeaseItem ──

// NetworkLeaseItem matches Python's NetworkLeaseItem dataclass.
type NetworkLeaseItem struct {
	NetworkID string  `json:"network_id"`
	IPv4      string  `json:"ipv4"`
	LeasedAt  string  `json:"leased_at"`
	ID        *int64  `json:"id,omitempty"`
	VMID      *string `json:"vm_id,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// ── FirewallRule ──

// FirewallRule matches Python's FirewallRule dataclass.
type FirewallRule struct {
	TableName      FirewallTable    `json:"table_name"`
	ChainName      FirewallChain    `json:"chain_name"`
	RuleType       FirewallRuleType `json:"rule_type"`
	Protocol       FirewallProtocol `json:"protocol"`
	Source         string           `json:"source"`
	Destination    string           `json:"destination"`
	InInterface    string           `json:"in_interface"`
	OutInterface   string           `json:"out_interface"`
	Target         FirewallTarget   `json:"target"`
	SPort          int              `json:"sport"`
	DPort          int              `json:"dport"`
	NetworkID      string           `json:"network_id"`
	IsActive       bool             `json:"is_active"`

	ID             *int64  `json:"id,omitempty"`
	NetworkName    *string `json:"network_name,omitempty"`
	CommentTag     *string `json:"comment_tag,omitempty"`
	CommandString  *string `json:"command_string,omitempty"`
	CreatedAt      *string `json:"created_at,omitempty"`
	LastVerifiedAt *string `json:"last_verified_at,omitempty"`
}

// ── FirewallRuleResult ──

// FirewallRuleResult matches Python's FirewallRuleResult.
type FirewallRuleResult struct {
	Success         bool          `json:"success"`
	Rule            *FirewallRule `json:"rule,omitempty"`
	ErrorMessage    *string       `json:"error_message,omitempty"`
	CommandExecuted *string       `json:"command_executed,omitempty"`
}
