package network

import (
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

// Firewall chain names as typed compile-time constants.
const (
	FirewallChainMVMForward      = model.FirewallChain(infra.MVMForwardChain)
	FirewallChainMVMPostrouting  = model.FirewallChain(infra.MVMPostroutingChain)
	FirewallChainMVMNocloudNetIn = model.FirewallChain(infra.MVMNocloudNetInputChain)
)

// NatGatewaysList returns nat_gateways as a list of strings.
func NatGatewaysList(n *model.Network) []string {
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
