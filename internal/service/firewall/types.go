package firewall

import (
	"net/netip"

	"github.com/google/nftables"
)

const (
	managedBridgeTableName = "mvmctl_bridge"
	managedINetTableName   = "mvmctl_inet"

	managedBridgePreroutingChainName = "prerouting"
	managedBridgeForwardChainName    = "forward"
	managedINetForwardChainName      = "forward"
	managedINetInputChainName        = "input"
	managedINetNATChainName          = "postrouting"
)

type protocol uint8

const (
	protocolTCP protocol = 6
	protocolUDP protocol = 17
)

type portInterval struct {
	first uint16
	last  uint16
}

type macAddress [6]byte

type endpointIntent struct {
	id        string
	networkID string
	hostVeth  string
	ipv4      netip.Addr
	mac       macAddress
}

type networkIntent struct {
	id              string
	hostBridge      string
	subnet          netip.Prefix
	hostIPv4        netip.Addr
	uplinkInterface string
	masquerade      bool
}

type vmTrafficGrant struct {
	id              string
	sourceVMID      string
	destinationVMID string
	protocol        protocol
	ports           portInterval
}

type networkTrafficGrant struct {
	id              string
	sourceNetworkID string
	destinationVMID string
	protocol        protocol
	ports           portInterval
}

type hostServiceIntent struct {
	id              string
	sourceNetworkID string
	protocol        protocol
	ports           portInterval
}

// generationIntent is fully resolved root-side intent. It deliberately exposes neither nftables expressions, generic
// actions, nor a caller-selectable generation identity. The compiler derives identity from normalized semantic state.
type generationIntent struct {
	endpoints     []endpointIntent
	networks      []networkIntent
	vmGrants      []vmTrafficGrant
	networkGrants []networkTrafficGrant
	hostServices  []hostServiceIntent
}

type tableIdentity struct {
	family nftables.TableFamily
	name   string
}

var managedTableIdentities = [...]tableIdentity{
	{family: nftables.TableFamilyBridge, name: managedBridgeTableName},
	{family: nftables.TableFamilyINet, name: managedINetTableName},
}

type compiledTable struct {
	table  *nftables.Table
	chains []*nftables.Chain
	rules  []compiledRule
}

type compiledRule struct {
	identity string
	rule     *nftables.Rule
}

type compiledGeneration struct {
	id              string
	empty           bool
	applicationSeal [32]byte
	endpointIDs     []string
	networkIDs      []string
	bridge          compiledTable
	inet            compiledTable
	ingressGuards   map[string]ingressGuardSet
}

type ingressGuardSet struct {
	mac           *nftables.Rule
	ipv4          *nftables.Rule
	arpHeader     *nftables.Rule
	arpOperation  *nftables.Rule
	arpMAC        *nftables.Rule
	arpIPv4       *nftables.Rule
	unsupportedL3 *nftables.Rule
}

func (generation compiledGeneration) tableIdentities() []tableIdentity {
	if generation.empty {
		return nil
	}
	return []tableIdentity{
		{family: generation.bridge.table.Family, name: generation.bridge.table.Name},
		{family: generation.inet.table.Family, name: generation.inet.table.Name},
	}
}

func (generation compiledGeneration) rules() []*nftables.Rule {
	rules := make([]*nftables.Rule, 0, len(generation.bridge.rules)+len(generation.inet.rules))
	for _, compiled := range generation.bridge.rules {
		rules = append(rules, compiled.rule)
	}
	for _, compiled := range generation.inet.rules {
		rules = append(rules, compiled.rule)
	}
	return rules
}
