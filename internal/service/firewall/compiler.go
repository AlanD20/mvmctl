package firewall

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"mvmctl/pkg/errs"
)

type generationCompiler struct {
	generation *compiledGeneration
	endpoints  map[string]endpointIntent
	networks   map[string]networkIntent
}

func compileGeneration(intent generationIntent) (compiledGeneration, error) {
	if err := validateIntent(intent); err != nil {
		return compiledGeneration{}, err
	}
	if generationIntentIsEmpty(intent) {
		generation := compiledGeneration{empty: true, ingressGuards: map[string]ingressGuardSet{}}
		generationID, err := deriveGenerationID(intent, generation)
		if err != nil {
			return compiledGeneration{}, err
		}
		generation.id = generationID
		if err := bindGenerationApplicationSeal(&generation); err != nil {
			return compiledGeneration{}, err
		}
		return generation, nil
	}

	compiler := newGenerationCompiler(intent)

	compiler.compileBridge(intent)
	compiler.compileINet(intent)
	if err := validateIngressGuardInvariant(intent, *compiler.generation); err != nil {
		return compiledGeneration{}, err
	}
	generationID, err := deriveGenerationID(intent, *compiler.generation)
	if err != nil {
		return compiledGeneration{}, err
	}
	compiler.generation.id = generationID
	if err := bindGenerationRuleData(compiler.generation); err != nil {
		return compiledGeneration{}, err
	}
	if err := bindGenerationApplicationSeal(compiler.generation); err != nil {
		return compiledGeneration{}, err
	}
	return *compiler.generation, nil
}

func newGenerationCompiler(intent generationIntent) *generationCompiler {
	bridgeTable := &nftables.Table{Family: nftables.TableFamilyBridge, Name: managedBridgeTableName}
	inetTable := &nftables.Table{Family: nftables.TableFamilyINet, Name: managedINetTableName}
	bridgePrerouting := filterChain(
		bridgeTable,
		managedBridgePreroutingChainName,
		nftables.ChainHookPrerouting,
		nftables.ChainPriorityRaw,
	)
	bridgeForward := filterChain(
		bridgeTable,
		managedBridgeForwardChainName,
		nftables.ChainHookForward,
		nftables.ChainPriorityFilter,
	)
	inetForward := filterChain(
		inetTable,
		managedINetForwardChainName,
		nftables.ChainHookForward,
		nftables.ChainPriorityFilter,
	)
	inetInput := filterChain(
		inetTable,
		managedINetInputChainName,
		nftables.ChainHookInput,
		nftables.ChainPriorityFilter,
	)
	inetNAT := &nftables.Chain{
		Name:     managedINetNATChainName,
		Table:    inetTable,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityNATSource,
		Type:     nftables.ChainTypeNAT,
	}

	generation := &compiledGeneration{
		ingressGuards: make(map[string]ingressGuardSet, len(intent.endpoints)),
		endpointIDs:   make([]string, 0, len(intent.endpoints)),
		networkIDs:    make([]string, 0, len(intent.networks)),
		bridge: compiledTable{
			table:  bridgeTable,
			chains: []*nftables.Chain{bridgePrerouting, bridgeForward},
		},
		inet: compiledTable{
			table:  inetTable,
			chains: []*nftables.Chain{inetForward, inetInput, inetNAT},
		},
	}
	for _, endpoint := range intent.endpoints {
		generation.endpointIDs = append(generation.endpointIDs, endpoint.id)
	}
	for _, network := range intent.networks {
		generation.networkIDs = append(generation.networkIDs, network.id)
	}
	slices.Sort(generation.endpointIDs)
	slices.Sort(generation.networkIDs)

	compiler := &generationCompiler{
		generation: generation,
		endpoints:  make(map[string]endpointIntent, len(intent.endpoints)),
		networks:   make(map[string]networkIntent, len(intent.networks)),
	}
	for _, endpoint := range intent.endpoints {
		compiler.endpoints[endpoint.id] = endpoint
	}
	for _, network := range intent.networks {
		compiler.networks[network.id] = network
	}
	return compiler
}

func filterChain(
	table *nftables.Table,
	name string,
	hook *nftables.ChainHook,
	priority *nftables.ChainPriority,
) *nftables.Chain {
	policy := nftables.ChainPolicyAccept
	return &nftables.Chain{
		Name:     name,
		Table:    table,
		Hooknum:  hook,
		Priority: priority,
		Type:     nftables.ChainTypeFilter,
		Policy:   &policy,
	}
}

func (compiler *generationCompiler) addRule(
	table *compiledTable,
	chain *nftables.Chain,
	identity string,
	expressions []expr.Any,
) *nftables.Rule {
	rule := &nftables.Rule{
		Table: table.table,
		Chain: chain,
		Exprs: expressions,
	}
	table.rules = append(table.rules, compiledRule{identity: identity, rule: rule})
	return rule
}

func validateIntent(intent generationIntent) error {
	networks := make(map[string]networkIntent, len(intent.networks))
	interfaces := make(map[string]string, len(intent.networks)*2+len(intent.endpoints))
	uplinks := make(map[string]struct{}, len(intent.networks))
	for _, network := range intent.networks {
		if !validInterfaceName(network.uplinkInterface) {
			return invalidGeneration("invalid uplink interface")
		}
		uplinks[network.uplinkInterface] = struct{}{}
	}
	for _, network := range intent.networks {
		if err := validateNetwork(network, interfaces, uplinks); err != nil {
			return err
		}
		if _, exists := networks[network.id]; exists {
			return invalidGeneration("duplicate network identity")
		}
		networks[network.id] = network
	}
	if err := validateManagedPrefixSet(intent.networks); err != nil {
		return err
	}

	endpoints := make(map[string]endpointIntent, len(intent.endpoints))
	endpointAddresses := make(map[string]string, len(intent.endpoints))
	endpointMACs := make(map[macAddress]string, len(intent.endpoints))
	for _, endpoint := range intent.endpoints {
		if err := validateEndpoint(endpoint, networks, interfaces, uplinks); err != nil {
			return err
		}
		if _, exists := endpoints[endpoint.id]; exists {
			return invalidGeneration("duplicate VM identity")
		}
		addressAuthority := endpoint.networkID + "/" + endpoint.ipv4.String()
		if _, exists := endpointAddresses[addressAuthority]; exists {
			return invalidGeneration("duplicate VM IPv4 authority")
		}
		if _, exists := endpointMACs[endpoint.mac]; exists {
			return invalidGeneration("duplicate VM MAC authority")
		}
		endpoints[endpoint.id] = endpoint
		endpointAddresses[addressAuthority] = endpoint.id
		endpointMACs[endpoint.mac] = endpoint.id
	}

	grantIDs := make(map[string]struct{}, len(intent.vmGrants)+len(intent.networkGrants)+len(intent.hostServices))
	for _, grant := range intent.vmGrants {
		if err := validateGrantBase(grant.id, grant.protocol, grant.ports, grantIDs); err != nil {
			return err
		}
		source, sourceExists := endpoints[grant.sourceVMID]
		destination, destinationExists := endpoints[grant.destinationVMID]
		if !sourceExists || !destinationExists || source.id == destination.id {
			return invalidGeneration("VM grant references an invalid endpoint pair")
		}
	}
	for _, grant := range intent.networkGrants {
		if err := validateGrantBase(grant.id, grant.protocol, grant.ports, grantIDs); err != nil {
			return err
		}
		if _, exists := networks[grant.sourceNetworkID]; !exists {
			return invalidGeneration("network grant references an unknown source network")
		}
		if _, exists := endpoints[grant.destinationVMID]; !exists {
			return invalidGeneration("network grant references an unknown destination VM")
		}
	}
	for _, service := range intent.hostServices {
		if err := validateGrantBase(service.id, service.protocol, service.ports, grantIDs); err != nil {
			return err
		}
		if _, exists := networks[service.sourceNetworkID]; !exists {
			return invalidGeneration("host service references an unknown source network")
		}
	}
	return nil
}

func generationIntentIsEmpty(intent generationIntent) bool {
	return len(intent.endpoints) == 0 && len(intent.networks) == 0 && len(intent.vmGrants) == 0 &&
		len(intent.networkGrants) == 0 && len(intent.hostServices) == 0
}

func validateNetwork(
	network networkIntent,
	interfaces map[string]string,
	uplinks map[string]struct{},
) error {
	if !validResourceID(network.id) {
		return invalidGeneration("invalid network identity")
	}
	if err := reserveInterface(network.hostBridge, "bridge", interfaces); err != nil {
		return err
	}
	if _, collision := uplinks[network.hostBridge]; collision {
		return invalidGeneration("bridge interface collides with an uplink interface")
	}
	if !validIPv4Prefix(network.subnet) || !usableIPv4Address(network.subnet, network.hostIPv4) {
		return invalidGeneration("invalid network IPv4 authority")
	}
	return nil
}

func validateEndpoint(
	endpoint endpointIntent,
	networks map[string]networkIntent,
	interfaces map[string]string,
	uplinks map[string]struct{},
) error {
	if !validResourceID(endpoint.id) {
		return invalidGeneration("invalid VM identity")
	}
	network, exists := networks[endpoint.networkID]
	if !exists {
		return invalidGeneration("VM references an unknown network")
	}
	if err := reserveInterface(endpoint.hostVeth, "VM", interfaces); err != nil {
		return err
	}
	if _, collision := uplinks[endpoint.hostVeth]; collision {
		return invalidGeneration("VM interface collides with an uplink interface")
	}
	if !usableIPv4Address(network.subnet, endpoint.ipv4) || endpoint.ipv4 == network.hostIPv4 {
		return invalidGeneration("invalid VM IPv4 authority")
	}
	if endpoint.mac == (macAddress{}) || endpoint.mac[0]&1 != 0 {
		return invalidGeneration("invalid VM MAC authority")
	}
	return nil
}

func validateGrantBase(
	id string,
	protocolValue protocol,
	ports portInterval,
	identities map[string]struct{},
) error {
	if !validResourceID(id) {
		return invalidGeneration("invalid policy identity")
	}
	if _, exists := identities[id]; exists {
		return invalidGeneration("duplicate policy identity")
	}
	identities[id] = struct{}{}
	if protocolValue != protocolTCP && protocolValue != protocolUDP {
		return invalidGeneration("unsupported policy protocol")
	}
	if ports.first == 0 || ports.last < ports.first {
		return invalidGeneration("invalid policy port interval")
	}
	return nil
}

func validateGenerationID(id string) error {
	if len(id) != 64 || !allCharacters(id, func(value byte) bool {
		return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
	}) {
		return invalidGeneration("generation identity must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validateRuleID(id string) error {
	if len(id) == 0 || len(id) > 255 || !allCharacters(id, func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '.' || value == '-'
	}) {
		return invalidGeneration("invalid compiled rule identity")
	}
	return nil
}

func validResourceID(id string) bool {
	return len(id) > 0 && len(id) <= 64 && allCharacters(id, func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-'
	})
}

func validInterfaceName(name string) bool {
	return len(name) > 0 && len(name) <= 15 && allCharacters(name, func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' || strings.ContainsRune("_.-", rune(value))
	})
}

func reserveInterface(name string, owner string, interfaces map[string]string) error {
	if !validInterfaceName(name) {
		return invalidGeneration(fmt.Sprintf("invalid %s interface", owner))
	}
	if existing, exists := interfaces[name]; exists {
		return invalidGeneration(fmt.Sprintf("%s interface collides with %s interface", owner, existing))
	}
	interfaces[name] = owner
	return nil
}

func validIPv4(address netip.Addr) bool {
	return address.IsValid() && address.Is4() && !address.IsUnspecified() && !address.IsMulticast()
}

func validIPv4Prefix(prefix netip.Prefix) bool {
	return prefix.IsValid() && prefix.Addr().Is4() && prefix == prefix.Masked() && prefix.Bits() <= 30
}

func usableIPv4Address(prefix netip.Prefix, address netip.Addr) bool {
	if !validIPv4(address) || !validIPv4Prefix(prefix) || !prefix.Contains(address) {
		return false
	}
	value := ipv4Value(address)
	start, end := ipv4PrefixRange(prefix)
	return value != start && value != end
}

type managedPrefixRange struct {
	start uint32
	end   uint32
}

func validateManagedPrefixSet(networks []networkIntent) error {
	ranges := make([]managedPrefixRange, 0, len(networks))
	for _, network := range networks {
		start, end := ipv4PrefixRange(network.subnet)
		ranges = append(ranges, managedPrefixRange{start: start, end: end})
	}
	slices.SortFunc(ranges, func(left, right managedPrefixRange) int {
		if result := cmp.Compare(left.start, right.start); result != 0 {
			return result
		}
		return cmp.Compare(left.end, right.end)
	})
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start <= ranges[index-1].end {
			return invalidGeneration("managed IPv4 prefixes overlap")
		}
	}
	return nil
}

func ipv4PrefixRange(prefix netip.Prefix) (uint32, uint32) {
	start := ipv4Value(prefix.Masked().Addr())
	hostBits := 32 - prefix.Bits()
	hostMask := uint32((uint64(1) << hostBits) - 1)
	return start, start | hostMask
}

func ipv4Value(address netip.Addr) uint32 {
	bytes := address.As4()
	return binary.BigEndian.Uint32(bytes[:])
}

func validateIngressGuardInvariant(intent generationIntent, generation compiledGeneration) error {
	var prerouting *nftables.Chain
	for _, chain := range generation.bridge.chains {
		if chain.Name == managedBridgePreroutingChainName {
			prerouting = chain
			break
		}
	}
	if prerouting == nil || prerouting.Table != generation.bridge.table || prerouting.Hooknum == nil ||
		*prerouting.Hooknum != *nftables.ChainHookPrerouting || prerouting.Priority == nil ||
		*prerouting.Priority != *nftables.ChainPriorityRaw || prerouting.Type != nftables.ChainTypeFilter {
		return invalidGeneration("bridge ingress guard chain is invalid")
	}

	for _, endpoint := range intent.endpoints {
		guards, exists := generation.ingressGuards[endpoint.id]
		if !exists {
			return invalidGeneration("VM ingress guard is absent from bridge prerouting")
		}
		for _, rule := range [...](*nftables.Rule){
			guards.mac,
			guards.ipv4,
			guards.arpMAC,
			guards.arpIPv4,
			guards.unsupportedL3,
		} {
			if rule == nil || rule.Table != generation.bridge.table || rule.Chain != prerouting {
				return invalidGeneration("VM ingress guard is absent from bridge prerouting")
			}
		}
	}
	return nil
}

func allCharacters(value string, allowed func(byte) bool) bool {
	for index := range len(value) {
		if !allowed(value[index]) {
			return false
		}
	}
	return true
}

func invalidGeneration(reason string) error {
	return errs.New(
		errs.CodeNetworkFirewallFailed,
		"firewall generation is invalid: "+reason,
		errs.WithClass(errs.ClassInternal),
	)
}

func sortedCopy[T any](values []T, identity func(T) string) []T {
	result := slices.Clone(values)
	slices.SortFunc(result, func(left, right T) int {
		return cmp.Compare(identity(left), identity(right))
	})
	return result
}
