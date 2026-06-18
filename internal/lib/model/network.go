package model

import "fmt"

// --- Firewall enums ---

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

const (
	FirewallChainMVMForward      FirewallChain = "MVM-FORWARD"
	FirewallChainMVMPostrouting  FirewallChain = "MVM-POSTROUTING"
	FirewallChainMVMNocloudNetIn FirewallChain = "MVM-NOCLOUDNET-INPUT"
)

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
	NATGateways  *string `json:"nat_gateways,omitempty" db:"nat_gateways"`

	// Resolved relations (not stored in DB directly)
	Leases        []*NetworkLeaseItem `json:"leases,omitempty"`
	IPTablesRules []*FirewallRule     `json:"iptables_rules,omitempty"`
	VMs           []*VMItem               `json:"vms,omitempty"`
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

// --- FirewallRule ---

// FirewallRule represents a firewall rule.
type FirewallRule struct {
	TableName    FirewallTable    `json:"table_name"    db:"table_name"`
	ChainName    FirewallChain    `json:"chain_name"    db:"chain_name"`
	RuleType     FirewallRuleType `json:"rule_type"     db:"rule_type"`
	Protocol     FirewallProtocol `json:"protocol"      db:"protocol"`
	Source       string           `json:"source"        db:"source"`
	Destination  string           `json:"destination"   db:"destination"`
	InInterface  string           `json:"in_interface"  db:"in_interface"`
	OutInterface string           `json:"out_interface" db:"out_interface"`
	Target       FirewallTarget   `json:"target"        db:"target"`
	SPort        int              `json:"sport"         db:"sport"`
	DPort        int              `json:"dport"         db:"dport"`
	NetworkID    string           `json:"network_id"    db:"network_id"`
	IsActive     bool             `json:"is_active"     db:"is_active"`

	ID             *int64  `json:"id,omitempty"               db:"id"`
	NetworkName    *string `json:"network_name,omitempty"`
	CommentTag     *string `json:"comment_tag,omitempty"      db:"comment_tag"`
	CommandString  *string `json:"command_string,omitempty"   db:"command_string"`
	CreatedAt      *string `json:"created_at,omitempty"       db:"created_at"`
	LastVerifiedAt *string `json:"last_verified_at,omitempty" db:"last_verified_at"`
}

// Key builds a dedup key from a rule's identifying fields.
func (r FirewallRule) Key() string {
	return fmt.Sprintf("%s:%s:%s:%s:%s:%d:%d",
		r.TableName, r.ChainName, r.RuleType,
		r.Source, r.OutInterface, r.SPort, r.DPort)
}

// NewTapForwardRules returns the two FORWARD rules needed for a TAP device:
// bridge→tap (forward_out) and tap→bridge (forward_in).
func NewTapForwardRules(tap, bridge, networkID, subnet string) []FirewallRule {
	wildcard := string(FirewallWildcardAnyCIDR)

	fwdBridgeToTap := FirewallRule{
		TableName:    FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     FirewallRuleTypeForwardOut,
		Target:       FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     FirewallProtocolAll,
		Source:       wildcard,
		Destination:  wildcard,
		InInterface:  bridge,
		OutInterface: tap,
		SPort:        int(FirewallPortAny),
		DPort:        int(FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}
	fwdTapToBridge := FirewallRule{
		TableName:    FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     FirewallRuleTypeForwardIn,
		Target:       FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     FirewallProtocolAll,
		Source:       wildcard,
		Destination:  wildcard,
		InInterface:  tap,
		OutInterface: bridge,
		SPort:        int(FirewallPortAny),
		DPort:        int(FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}

	if subnet != "" {
		fwdBridgeToTap.Source = subnet
		fwdTapToBridge.Destination = subnet
	}

	return []FirewallRule{fwdBridgeToTap, fwdTapToBridge}
}

// --- FirewallRuleResult ---

// FirewallRuleResult holds the result of a rule operation.
type FirewallRuleResult struct {
	Success         bool          `json:"success"`
	Rule            *FirewallRule `json:"rule,omitempty"`
	ErrorMessage    *string       `json:"error_message,omitempty"`
	CommandExecuted *string       `json:"command_executed,omitempty"`
}
