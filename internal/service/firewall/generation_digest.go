package firewall

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"mvmctl/pkg/errs"
)

const (
	// CRITICAL: v1 explicitly projects every field of the closed google/nftables expression set emitted by this compiler.
	// Adding an expression type or dependency field requires a schema audit and version change before it can reach output.
	generationDigestSchema      = "mvmctl.firewall.generation.v1"
	generationDigestPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"
)

// deriveGenerationID hashes a versioned encoding of normalized intent and the exact compiled semantic output.
// Rule userdata uses a fixed placeholder for the self-referential digest; bindGenerationRuleData substitutes the
// derived value without changing any other userdata byte.
func deriveGenerationID(intent generationIntent, generation compiledGeneration) (string, error) {
	digest := sha256.New()
	encoder := generationDigestEncoder{writer: bufio.NewWriterSize(digest, generationDigestBufferBytes)}
	if err := encodeCanonicalGeneration(&encoder, intent, generation); err != nil {
		return "", err
	}
	if err := encoder.writer.Flush(); err != nil {
		return "", errs.WrapMsg(
			errs.CodeNetworkFirewallFailed,
			"firewall generation is invalid: cannot finalize canonical digest",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func encodeCanonicalGeneration(
	encoder *generationDigestEncoder,
	intent generationIntent,
	generation compiledGeneration,
) error {
	if generation.empty != generationIntentIsEmpty(intent) {
		return invalidGeneration("compiled empty state disagrees with normalized intent")
	}
	if generation.empty && (generation.bridge.table != nil || generation.inet.table != nil ||
		len(generation.bridge.chains) != 0 || len(generation.inet.chains) != 0 ||
		len(generation.bridge.rules) != 0 || len(generation.inet.rules) != 0) {
		return invalidGeneration("compiled empty generation contains nftables output")
	}

	encoder.text("schema", generationDigestSchema)
	encoder.boolean("generation.empty", generation.empty)
	encodeCanonicalIntent(encoder, intent)
	if err := encodeCanonicalOutput(encoder, generation); err != nil {
		return err
	}
	return nil
}

func encodeCanonicalIntent(encoder *generationDigestEncoder, intent generationIntent) {
	endpoints := sortedCopy(intent.endpoints, func(endpoint endpointIntent) string { return endpoint.id })
	encoder.count("intent.endpoints", len(endpoints))
	for _, endpoint := range endpoints {
		encoder.beginRecord("intent.endpoint")
		encoder.text("id", endpoint.id)
		encoder.text("network-id", endpoint.networkID)
		encoder.text("host-veth", endpoint.hostVeth)
		address := endpoint.ipv4.As4()
		encoder.bytes("ipv4", address[:])
		encoder.bytes("mac", endpoint.mac[:])
		encoder.endRecord("intent.endpoint")
	}

	networks := sortedCopy(intent.networks, func(network networkIntent) string { return network.id })
	encoder.count("intent.networks", len(networks))
	for _, network := range networks {
		encoder.beginRecord("intent.network")
		encoder.text("id", network.id)
		encoder.text("host-bridge", network.hostBridge)
		subnet := network.subnet.Addr().As4()
		encoder.bytes("subnet-address", subnet[:])
		encoder.uint32("subnet-bits", uint32(network.subnet.Bits()))
		hostAddress := network.hostIPv4.As4()
		encoder.bytes("host-ipv4", hostAddress[:])
		encoder.text("uplink-interface", network.uplinkInterface)
		encoder.boolean("masquerade", network.masquerade)
		encoder.endRecord("intent.network")
	}

	vmGrants := sortedCopy(intent.vmGrants, func(grant vmTrafficGrant) string { return grant.id })
	encoder.count("intent.vm-grants", len(vmGrants))
	for _, grant := range vmGrants {
		encoder.beginRecord("intent.vm-grant")
		encoder.text("id", grant.id)
		encoder.text("source-vm-id", grant.sourceVMID)
		encoder.text("destination-vm-id", grant.destinationVMID)
		encodeCanonicalProtocolAndPorts(encoder, grant.protocol, grant.ports)
		encoder.endRecord("intent.vm-grant")
	}

	networkGrants := sortedCopy(intent.networkGrants, func(grant networkTrafficGrant) string { return grant.id })
	encoder.count("intent.network-grants", len(networkGrants))
	for _, grant := range networkGrants {
		encoder.beginRecord("intent.network-grant")
		encoder.text("id", grant.id)
		encoder.text("source-network-id", grant.sourceNetworkID)
		encoder.text("destination-vm-id", grant.destinationVMID)
		encodeCanonicalProtocolAndPorts(encoder, grant.protocol, grant.ports)
		encoder.endRecord("intent.network-grant")
	}

	hostServices := sortedCopy(intent.hostServices, func(service hostServiceIntent) string { return service.id })
	encoder.count("intent.host-services", len(hostServices))
	for _, service := range hostServices {
		encoder.beginRecord("intent.host-service")
		encoder.text("id", service.id)
		encoder.text("source-network-id", service.sourceNetworkID)
		encodeCanonicalProtocolAndPorts(encoder, service.protocol, service.ports)
		encoder.endRecord("intent.host-service")
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
		if err := encodeCanonicalTable(encoder, generation.id, table); err != nil {
			return err
		}
	}
	return nil
}

func encodeCanonicalTable(encoder *generationDigestEncoder, boundGenerationID string, table compiledTable) error {
	if table.table == nil {
		return invalidGeneration("compiled table is absent from canonical generation")
	}
	encoder.beginRecord("output.table")
	encodeCanonicalTableIdentity(encoder, "table", table.table)
	// Table.Use and Rule.Handle are kernel-assigned observations, not compiler output, so authority excludes them.
	encoder.uint32("table.flags", table.table.Flags)
	encoder.count("chains", len(table.chains))
	for _, chain := range table.chains {
		if chain == nil || chain.Table == nil {
			return invalidGeneration("compiled chain is absent from canonical generation")
		}
		encoder.beginRecord("chain")
		encoder.text("name", chain.Name)
		encodeCanonicalTableIdentity(encoder, "table", chain.Table)
		encoder.optionalUint32("hook", chain.Hooknum != nil, canonicalChainHook(chain))
		encoder.optionalInt32("priority", chain.Priority != nil, canonicalChainPriority(chain))
		encoder.text("type", string(chain.Type))
		encoder.optionalUint32("policy", chain.Policy != nil, canonicalChainPolicy(chain))
		encoder.text("device", chain.Device)
		encoder.endRecord("chain")
	}

	encoder.count("rules", len(table.rules))
	for _, compiled := range table.rules {
		rule := compiled.rule
		if rule == nil || rule.Table == nil || rule.Chain == nil || rule.Chain.Table == nil {
			return invalidGeneration("compiled rule is absent from canonical generation")
		}
		encoder.beginRecord("rule")
		encoder.text("identity", compiled.identity)
		encodeCanonicalTableIdentity(encoder, "table", rule.Table)
		encodeCanonicalTableIdentity(encoder, "chain-table", rule.Chain.Table)
		encoder.text("chain", rule.Chain.Name)
		encoder.uint64("position", rule.Position)
		encoder.uint32("flags", rule.Flags)
		placeholderUserData, err := appendRuleIdentity(encoder.placeholderUserDataScratch[:0], ruleIdentity{
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
		if len(rule.UserData) != 0 {
			expectedUserData, encodeErr := appendRuleIdentity(encoder.expectedUserDataScratch[:0], ruleIdentity{
				generation: boundGenerationID,
				rule:       compiled.identity,
			})
			if encodeErr != nil || !bytes.Equal(rule.UserData, expectedUserData) {
				return invalidGeneration("compiled rule contains inconsistent generation userdata")
			}
		}
		encoder.bytes("userdata-with-digest-placeholder", placeholderUserData)
		encoder.count("expressions", len(rule.Exprs))
		for _, expression := range rule.Exprs {
			if err := encodeCanonicalExpression(encoder, expression); err != nil {
				return err
			}
		}
		encoder.endRecord("rule")
	}

	encoder.endRecord("output.table")
	return nil
}

// encodeCanonicalExpression is intentionally a closed type switch. This keeps generation hashing bounded while
// making every compiler-emitted expression field authoritative. Unknown dependency types fail before an ID exists.
func encodeCanonicalExpression(encoder *generationDigestEncoder, expression expr.Any) error {
	switch value := expression.(type) {
	case *expr.Meta:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent meta expression")
		}
		encoder.beginRecord("expression.meta")
		encoder.uint32("key", uint32(value.Key))
		encoder.boolean("source-register", value.SourceRegister)
		encoder.uint32("register", value.Register)
		encoder.endRecord("expression.meta")
	case *expr.Masq:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent masquerade expression")
		}
		encoder.beginRecord("expression.masquerade")
		encoder.boolean("random", value.Random)
		encoder.boolean("fully-random", value.FullyRandom)
		encoder.boolean("persistent", value.Persistent)
		encoder.boolean("to-ports", value.ToPorts)
		encoder.uint32("register-protocol-min", value.RegProtoMin)
		encoder.uint32("register-protocol-max", value.RegProtoMax)
		encoder.endRecord("expression.masquerade")
	case *expr.Bitwise:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent bitwise expression")
		}
		encoder.beginRecord("expression.bitwise")
		encoder.uint32("source-register", value.SourceRegister)
		encoder.uint32("destination-register", value.DestRegister)
		encoder.uint32("length", value.Len)
		encoder.bytes("mask", value.Mask)
		encoder.bytes("xor", value.Xor)
		encoder.endRecord("expression.bitwise")
	case *expr.Cmp:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent comparison expression")
		}
		encoder.beginRecord("expression.comparison")
		encoder.uint32("operation", uint32(value.Op))
		encoder.uint32("register", value.Register)
		encoder.bytes("data", value.Data)
		encoder.endRecord("expression.comparison")
	case *expr.Ct:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent conntrack expression")
		}
		encoder.beginRecord("expression.conntrack")
		encoder.uint32("register", value.Register)
		encoder.boolean("source-register", value.SourceRegister)
		encoder.uint32("key", uint32(value.Key))
		encoder.uint32("direction", value.Direction)
		encoder.endRecord("expression.conntrack")
	case *expr.Payload:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent payload expression")
		}
		encoder.beginRecord("expression.payload")
		encoder.uint32("operation", uint32(value.OperationType))
		encoder.uint32("destination-register", value.DestRegister)
		encoder.uint32("source-register", value.SourceRegister)
		encoder.uint32("base", uint32(value.Base))
		encoder.uint32("offset", value.Offset)
		encoder.uint32("length", value.Len)
		encoder.uint32("checksum-type", uint32(value.CsumType))
		encoder.uint32("checksum-offset", value.CsumOffset)
		encoder.uint32("checksum-flags", value.CsumFlags)
		encoder.endRecord("expression.payload")
	case *expr.Range:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent range expression")
		}
		encoder.beginRecord("expression.range")
		encoder.uint32("operation", uint32(value.Op))
		encoder.uint32("register", value.Register)
		encoder.bytes("from-data", value.FromData)
		encoder.bytes("to-data", value.ToData)
		encoder.endRecord("expression.range")
	case *expr.Verdict:
		if value == nil {
			return invalidGeneration("compiled rule contains an absent verdict expression")
		}
		encoder.beginRecord("expression.verdict")
		encoder.uint64("kind", uint64(value.Kind))
		encoder.text("chain", value.Chain)
		encoder.endRecord("expression.verdict")
	default:
		return invalidGeneration(fmt.Sprintf("compiled rule contains unsupported expression %T", expression))
	}
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

// generationDigestEncoder streams the fixed v1 TLV grammar directly into SHA-256. Every name and value is
// length-delimited, while explicit begin/end records preserve nesting without retaining or repeatedly copying the
// complete generation in memory.
type generationDigestEncoder struct {
	writer        *bufio.Writer
	lengthScratch [binary.MaxVarintLen64]byte
	numberScratch [8]byte
	boolScratch   [1]byte
	// Both encodings are live until the bound userdata is checked and the placeholder is written.
	// They never back retained rule.UserData; Write consumes the placeholder before the next rule reuses it.
	placeholderUserDataScratch [maxRuleUserDataBytes]byte
	expectedUserDataScratch    [maxRuleUserDataBytes]byte
}

const generationDigestBufferBytes = 64 << 10

func (encoder *generationDigestEncoder) beginRecord(name string) {
	encoder.text("record.begin", name)
}

func (encoder *generationDigestEncoder) endRecord(name string) {
	encoder.text("record.end", name)
}

func (encoder *generationDigestEncoder) text(name string, value string) {
	encoder.writeHeader(name, len(value))
	_, _ = encoder.writer.WriteString(value)
}

func (encoder *generationDigestEncoder) bytes(name string, value []byte) {
	encoder.writeHeader(name, len(value))
	_, _ = encoder.writer.Write(value)
}

func (encoder *generationDigestEncoder) writeHeader(name string, valueBytes int) {
	lengthBytes := binary.PutUvarint(encoder.lengthScratch[:], uint64(len(name)))
	_, _ = encoder.writer.Write(encoder.lengthScratch[:lengthBytes])
	_, _ = encoder.writer.WriteString(name)
	lengthBytes = binary.PutUvarint(encoder.lengthScratch[:], uint64(valueBytes))
	_, _ = encoder.writer.Write(encoder.lengthScratch[:lengthBytes])
}

func (encoder *generationDigestEncoder) count(name string, value int) {
	encoder.uint64(name, uint64(value))
}

func (encoder *generationDigestEncoder) boolean(name string, value bool) {
	encoder.boolScratch[0] = 0
	if value {
		encoder.boolScratch[0] = 1
	}
	encoder.bytes(name, encoder.boolScratch[:])
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
	binary.BigEndian.PutUint32(encoder.numberScratch[:4], value)
	encoder.bytes(name, encoder.numberScratch[:4])
}

func (encoder *generationDigestEncoder) uint64(name string, value uint64) {
	binary.BigEndian.PutUint64(encoder.numberScratch[:], value)
	encoder.bytes(name, encoder.numberScratch[:])
}
