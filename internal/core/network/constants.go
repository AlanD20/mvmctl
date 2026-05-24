package network

import (
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

// FirewallChain values are derived from CLI_NAME at init time.
// They were previously in model.go alongside the firewall enums.
// Now the enums live in infra/model; these runtime-resolved chain
// variables stay in the network domain because they are initialised
// from infra config at package init time.

var (
	FirewallChainMVMForward      model.FirewallChain
	FirewallChainMVMPostrouting  model.FirewallChain
	FirewallChainMVMNocloudNetIn model.FirewallChain
)

// InitFirewallChains initialises firewall chain variables.
// TODO: call InitFirewallChains() from app/app.go explicitly
func InitFirewallChains() {
	FirewallChainMVMForward = model.FirewallChain(infra.MVMFwdChain())
	FirewallChainMVMPostrouting = model.FirewallChain(infra.MVMPostroutingChain())
	FirewallChainMVMNocloudNetIn = model.FirewallChain(infra.MVMNocloudNetInputChain())
}

// Type aliases so existing code continues to compile without rename churn.
// These resolve to the canonical types in infra/model.
type (
	Network             = model.Network
	NetworkLeaseItem    = model.NetworkLeaseItem
	FirewallRule        = model.FirewallRule
	FirewallRuleResult  = model.FirewallRuleResult
	FirewallBackendType = model.FirewallBackendType
	FirewallTable       = model.FirewallTable
	FirewallRuleType    = model.FirewallRuleType
	FirewallProtocol    = model.FirewallProtocol
	FirewallTarget      = model.FirewallTarget
	FirewallWildcard    = model.FirewallWildcard
)

const (
	FirewallTableFilter   = model.FirewallTableFilter
	FirewallTableNat      = model.FirewallTableNat
	FirewallTableMangle   = model.FirewallTableMangle
	FirewallTableRaw      = model.FirewallTableRaw
	FirewallTableSecurity = model.FirewallTableSecurity
)

const (
	FirewallRuleTypeMasquerade      = model.FirewallRuleTypeMasquerade
	FirewallRuleTypeForwardIn       = model.FirewallRuleTypeForwardIn
	FirewallRuleTypeForwardOut      = model.FirewallRuleTypeForwardOut
	FirewallRuleTypeNocloudNetInput = model.FirewallRuleTypeNocloudNetInput

	// Legacy aliases — match the naming in the original network/model.go.
	FirewallRuleMasquerade      = model.FirewallRuleTypeMasquerade
	FirewallRuleForwardIn       = model.FirewallRuleTypeForwardIn
	FirewallRuleForwardOut      = model.FirewallRuleTypeForwardOut
	FirewallRuleNocloudNetInput = model.FirewallRuleTypeNocloudNetInput
)

const (
	FirewallProtocolTCP  = model.FirewallProtocolTCP
	FirewallProtocolUDP  = model.FirewallProtocolUDP
	FirewallProtocolICMP = model.FirewallProtocolICMP
	FirewallProtocolAll  = model.FirewallProtocolAll
)

const (
	FirewallTargetMasquerade = model.FirewallTargetMasquerade
	FirewallTargetAccept     = model.FirewallTargetAccept
	FirewallTargetDrop       = model.FirewallTargetDrop
	FirewallTargetReject     = model.FirewallTargetReject
	FirewallTargetLog        = model.FirewallTargetLog
	FirewallTargetMark       = model.FirewallTargetMark
)

const (
	FirewallWildcardAnyCIDR      = model.FirewallWildcardAnyCIDR
	FirewallWildcardAnyInterface = model.FirewallWildcardAnyInterface
	FirewallPortAny              = model.FirewallPortAny
)

// NatGatewaysList returns nat_gateways as a list of strings.
// This is effectively a method moved out of the old local Network type
// (which was deleted in favour of the model.Network alias).
func NatGatewaysList(n *Network) []string {
	if n.NATGateways == nil {
		return []string{}
	}
	v := *n.NATGateways
	if v == "" || v == "0" || v == "false" || v == "False" || v == "none" || v == "None" {
		return []string{}
	}
	parts := strings.Split(*n.NATGateways, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		stripped := strings.TrimSpace(p)
		if stripped != "" {
			result = append(result, stripped)
		}
	}
	return result
}
