package firewall

import (
	"encoding/binary"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"mvmctl/internal/lib/crypto"
	"mvmctl/pkg/errs"
)

const (
	// CRITICAL: v1 incorporates the pinned google/nftables expression wire encoding. Any dependency or encoding change
	// that can alter those bytes requires a schema version change so existing authority records cannot alias new output.
	generationDigestSchema      = "mvmctl.firewall.generation.v1"
	generationDigestPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"
)

// deriveGenerationID hashes a versioned encoding of normalized intent and the exact compiled semantic output.
// Rule userdata uses a fixed placeholder for the self-referential digest; bindGenerationRuleData substitutes the
// derived value without changing any other userdata byte.
func deriveGenerationID(intent generationIntent, generation compiledGeneration) (string, error) {
	encoded, err := encodeCanonicalGeneration(intent, generation)
	if err != nil {
		return "", err
	}
	return crypto.SHA256(encoded), nil
}

func encodeCanonicalGeneration(intent generationIntent, generation compiledGeneration) ([]byte, error) {
	if generation.empty != generationIntentIsEmpty(intent) {
		return nil, invalidGeneration("compiled empty state disagrees with normalized intent")
	}
	if generation.empty && (generation.bridge.table != nil || generation.inet.table != nil ||
		len(generation.bridge.chains) != 0 || len(generation.inet.chains) != 0 ||
		len(generation.bridge.rules) != 0 || len(generation.inet.rules) != 0) {
		return nil, invalidGeneration("compiled empty generation contains nftables output")
	}

	encoder := generationDigestEncoder{}
	encoder.text("schema", generationDigestSchema)
	encoder.boolean("generation.empty", generation.empty)
	encodeCanonicalIntent(&encoder, intent)
	if err := encodeCanonicalOutput(&encoder, generation); err != nil {
		return nil, err
	}
	return encoder.data, nil
}

func encodeCanonicalIntent(encoder *generationDigestEncoder, intent generationIntent) {
	endpoints := sortedCopy(intent.endpoints, func(endpoint endpointIntent) string { return endpoint.id })
	encoder.count("intent.endpoints", len(endpoints))
	for _, endpoint := range endpoints {
		nested := generationDigestEncoder{}
		nested.text("id", endpoint.id)
		nested.text("network-id", endpoint.networkID)
		nested.text("host-veth", endpoint.hostVeth)
		address := endpoint.ipv4.As4()
		nested.bytes("ipv4", address[:])
		nested.bytes("mac", endpoint.mac[:])
		encoder.record("intent.endpoint", nested)
	}

	networks := sortedCopy(intent.networks, func(network networkIntent) string { return network.id })
	encoder.count("intent.networks", len(networks))
	for _, network := range networks {
		nested := generationDigestEncoder{}
		nested.text("id", network.id)
		nested.text("host-bridge", network.hostBridge)
		subnet := network.subnet.Addr().As4()
		nested.bytes("subnet-address", subnet[:])
		nested.uint32("subnet-bits", uint32(network.subnet.Bits()))
		hostAddress := network.hostIPv4.As4()
		nested.bytes("host-ipv4", hostAddress[:])
		nested.text("uplink-interface", network.uplinkInterface)
		nested.boolean("masquerade", network.masquerade)
		encoder.record("intent.network", nested)
	}

	vmGrants := sortedCopy(intent.vmGrants, func(grant vmTrafficGrant) string { return grant.id })
	encoder.count("intent.vm-grants", len(vmGrants))
	for _, grant := range vmGrants {
		nested := generationDigestEncoder{}
		nested.text("id", grant.id)
		nested.text("source-vm-id", grant.sourceVMID)
		nested.text("destination-vm-id", grant.destinationVMID)
		encodeCanonicalProtocolAndPorts(&nested, grant.protocol, grant.ports)
		encoder.record("intent.vm-grant", nested)
	}

	networkGrants := sortedCopy(intent.networkGrants, func(grant networkTrafficGrant) string { return grant.id })
	encoder.count("intent.network-grants", len(networkGrants))
	for _, grant := range networkGrants {
		nested := generationDigestEncoder{}
		nested.text("id", grant.id)
		nested.text("source-network-id", grant.sourceNetworkID)
		nested.text("destination-vm-id", grant.destinationVMID)
		encodeCanonicalProtocolAndPorts(&nested, grant.protocol, grant.ports)
		encoder.record("intent.network-grant", nested)
	}

	hostServices := sortedCopy(intent.hostServices, func(service hostServiceIntent) string { return service.id })
	encoder.count("intent.host-services", len(hostServices))
	for _, service := range hostServices {
		nested := generationDigestEncoder{}
		nested.text("id", service.id)
		nested.text("source-network-id", service.sourceNetworkID)
		encodeCanonicalProtocolAndPorts(&nested, service.protocol, service.ports)
		encoder.record("intent.host-service", nested)
	}
}

func encodeCanonicalProtocolAndPorts(
	encoder *generationDigestEncoder,
	protocolValue protocol,
	ports portInterval,
) {
	encoder.uint32("protocol", uint32(protocolValue))
	encoder.uint32("first-port", uint32(ports.first))
	encoder.uint32("last-port", uint32(ports.last))
}

func encodeCanonicalOutput(encoder *generationDigestEncoder, generation compiledGeneration) error {
	if generation.empty {
		encoder.count("output.tables", 0)
		return nil
	}

	tables := [...]compiledTable{generation.bridge, generation.inet}
	encoder.count("output.tables", len(tables))
	for _, table := range tables {
		if err := encodeCanonicalTable(encoder, table); err != nil {
			return err
		}
	}
	return nil
}

func encodeCanonicalTable(encoder *generationDigestEncoder, table compiledTable) error {
	if table.table == nil {
		return invalidGeneration("compiled table is absent from canonical generation")
	}
	nested := generationDigestEncoder{}
	encodeCanonicalTableIdentity(&nested, "table", table.table)
	// Table.Use and Rule.Handle are kernel-assigned observations, not compiler output, so authority excludes them.
	nested.uint32("table.flags", table.table.Flags)
	nested.count("chains", len(table.chains))
	for _, chain := range table.chains {
		if chain == nil || chain.Table == nil {
			return invalidGeneration("compiled chain is absent from canonical generation")
		}
		chainEncoder := generationDigestEncoder{}
		chainEncoder.text("name", chain.Name)
		encodeCanonicalTableIdentity(&chainEncoder, "table", chain.Table)
		chainEncoder.optionalUint32("hook", chain.Hooknum != nil, canonicalChainHook(chain))
		chainEncoder.optionalInt32("priority", chain.Priority != nil, canonicalChainPriority(chain))
		chainEncoder.text("type", string(chain.Type))
		chainEncoder.optionalUint32("policy", chain.Policy != nil, canonicalChainPolicy(chain))
		chainEncoder.text("device", chain.Device)
		nested.record("chain", chainEncoder)
	}

	nested.count("rules", len(table.rules))
	for _, compiled := range table.rules {
		rule := compiled.rule
		if rule == nil || rule.Table == nil || rule.Chain == nil || rule.Chain.Table == nil {
			return invalidGeneration("compiled rule is absent from canonical generation")
		}
		ruleEncoder := generationDigestEncoder{}
		ruleEncoder.text("identity", compiled.identity)
		encodeCanonicalTableIdentity(&ruleEncoder, "table", rule.Table)
		encodeCanonicalTableIdentity(&ruleEncoder, "chain-table", rule.Chain.Table)
		ruleEncoder.text("chain", rule.Chain.Name)
		ruleEncoder.uint64("position", rule.Position)
		ruleEncoder.uint32("flags", rule.Flags)
		placeholderUserData, err := encodeRuleIdentity(ruleIdentity{
			generation: generationDigestPlaceholder,
			rule:       compiled.identity,
		})
		if err != nil {
			return errs.WrapMsg(
				errs.CodeNetworkFirewallFailed,
				"firewall generation is invalid: compiled rule identity cannot be encoded",
				err,
				errs.WithClass(errs.ClassInternal),
			)
		}
		ruleEncoder.bytes("userdata-with-digest-placeholder", placeholderUserData)
		ruleEncoder.count("expressions", len(rule.Exprs))
		for _, expression := range rule.Exprs {
			if expression == nil {
				return invalidGeneration("compiled rule contains an absent expression")
			}
			// Hash the emitted wire form rather than a partial struct projection. The pinned encoder therefore makes every
			// expression type and field that reaches the kernel part of generation authority.
			encodedExpression, err := expr.Marshal(byte(rule.Table.Family), expression)
			if err != nil {
				return errs.WrapMsg(
					errs.CodeNetworkFirewallFailed,
					fmt.Sprintf("firewall generation is invalid: cannot encode compiled expression %T", expression),
					err,
					errs.WithClass(errs.ClassInternal),
				)
			}
			ruleEncoder.bytes("expression", encodedExpression)
		}
		nested.record("rule", ruleEncoder)
	}

	encoder.record("output.table", nested)
	return nil
}

func encodeCanonicalTableIdentity(encoder *generationDigestEncoder, prefix string, table *nftables.Table) {
	encoder.uint32(prefix+".family", uint32(table.Family))
	encoder.text(prefix+".name", table.Name)
}

func canonicalChainHook(chain *nftables.Chain) uint32 {
	if chain.Hooknum == nil {
		return 0
	}
	return uint32(*chain.Hooknum)
}

func canonicalChainPriority(chain *nftables.Chain) int32 {
	if chain.Priority == nil {
		return 0
	}
	return int32(*chain.Priority)
}

func canonicalChainPolicy(chain *nftables.Chain) uint32 {
	if chain.Policy == nil {
		return 0
	}
	return uint32(*chain.Policy)
}

func bindGenerationRuleData(generation *compiledGeneration) error {
	for _, table := range []*compiledTable{&generation.bridge, &generation.inet} {
		for _, compiled := range table.rules {
			userdata, err := encodeRuleIdentity(ruleIdentity{
				generation: generation.id,
				rule:       compiled.identity,
			})
			if err != nil {
				return errs.WrapMsg(
					errs.CodeNetworkFirewallFailed,
					"firewall generation is invalid: compiled rule identity cannot be encoded",
					err,
					errs.WithClass(errs.ClassInternal),
				)
			}
			compiled.rule.UserData = userdata
		}
	}
	return nil
}

// generationDigestEncoder is a private TLV encoder for the fixed v1 generation schema. Every field and nested record
// is length-delimited so values cannot cross field boundaries or create concatenation ambiguity.
type generationDigestEncoder struct {
	data []byte
}

func (encoder *generationDigestEncoder) record(name string, nested generationDigestEncoder) {
	encoder.bytes(name, nested.data)
}

func (encoder *generationDigestEncoder) text(name string, value string) {
	encoder.bytes(name, []byte(value))
}

func (encoder *generationDigestEncoder) bytes(name string, value []byte) {
	encoder.data = binary.AppendUvarint(encoder.data, uint64(len(name)))
	encoder.data = append(encoder.data, name...)
	encoder.data = binary.AppendUvarint(encoder.data, uint64(len(value)))
	encoder.data = append(encoder.data, value...)
}

func (encoder *generationDigestEncoder) count(name string, value int) {
	encoder.uint64(name, uint64(value))
}

func (encoder *generationDigestEncoder) boolean(name string, value bool) {
	encoded := byte(0)
	if value {
		encoded = 1
	}
	encoder.bytes(name, []byte{encoded})
}

func (encoder *generationDigestEncoder) optionalUint32(name string, present bool, value uint32) {
	encoder.boolean(name+".present", present)
	if present {
		encoder.uint32(name+".value", value)
	}
}

func (encoder *generationDigestEncoder) optionalInt32(name string, present bool, value int32) {
	encoder.boolean(name+".present", present)
	if present {
		encoder.uint32(name+".value", uint32(value))
	}
}

func (encoder *generationDigestEncoder) uint32(name string, value uint32) {
	encoded := make([]byte, 4)
	binary.BigEndian.PutUint32(encoded, value)
	encoder.bytes(name, encoded)
}

func (encoder *generationDigestEncoder) uint64(name string, value uint64) {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	encoder.bytes(name, encoded)
}
