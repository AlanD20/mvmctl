package firewall

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileGenerationBuildsClosedRuleFamilies(t *testing.T) {
	intent := compatibilityIntent()

	compiled, err := compileGeneration(intent)
	require.NoError(t, err)

	wantTables := []tableIdentity{
		{family: nftables.TableFamilyBridge, name: managedBridgeTableName},
		{family: nftables.TableFamilyINet, name: managedINetTableName},
	}
	if diff := cmp.Diff(wantTables, compiled.tableIdentities(), cmp.AllowUnexported(tableIdentity{})); diff != "" {
		t.Errorf("managed tables mismatch (-want +got):\n%s", diff)
	}

	bridgeMACRule := requireCompiledRule(t, compiled, "bridge.spoof.mac."+intent.endpoints[0].id)
	wantBridgeMAC := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName("mvma0")},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 6, Len: 6},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0x02, 0, 0, 0, 0, 1}},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
	if diff := cmp.Diff(wantBridgeMAC, bridgeMACRule.Exprs); diff != "" {
		t.Errorf("bridge MAC anti-spoof expressions mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, expr.VerdictDrop,
		finalVerdict(t, requireCompiledRule(t, compiled, "bridge.spoof.ipv4."+intent.endpoints[0].id).Exprs).Kind)
	assert.Equal(t, expr.VerdictDrop,
		finalVerdict(t, requireCompiledRule(t, compiled, "bridge.spoof.arp-mac."+intent.endpoints[0].id).Exprs).Kind)
	assert.Equal(t, expr.VerdictDrop,
		finalVerdict(t, requireCompiledRule(t, compiled, "bridge.spoof.arp-ipv4."+intent.endpoints[0].id).Exprs).Kind)
	assert.Equal(t, expr.VerdictDrop,
		finalVerdict(t, requireCompiledRule(t, compiled, "bridge.unsupported-l3."+intent.endpoints[0].id).Exprs).Kind)

	established := requireCompiledRule(t, compiled, "bridge.established."+intent.endpoints[0].id)
	stateMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	wantEstablished := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName("mvma0")},
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: stateMask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
	if diff := cmp.Diff(wantEstablished, established.Exprs); diff != "" {
		t.Errorf("established/related expressions mismatch (-want +got):\n%s", diff)
	}

	sameNetwork := requireCompiledRule(t, compiled, "bridge.allow.vm."+intent.vmGrants[0].id)
	assertExpressionKinds(t, sameNetwork.Exprs, []string{
		"meta", "cmp", "meta", "cmp", "payload", "cmp", "payload", "cmp", "payload", "cmp",
		"meta", "cmp", "payload", "cmp", "immediate",
	})
	assert.Contains(t, expressionData(sameNetwork.Exprs), []byte{0x01, 0xbb},
		"TCP destination port 443 must be encoded in network byte order")

	sameNetworkRange := requireCompiledRule(t, compiled, "bridge.allow.network."+intent.networkGrants[0].id)
	assert.Contains(t, expressionKinds(sameNetworkRange.Exprs), "bitwise",
		"source-network grants must mask the source prefix")
	assert.Contains(t, expressionKinds(sameNetworkRange.Exprs), "range",
		"port ranges must use the typed range expression")

	routed := requireCompiledRule(t, compiled, "inet.forward.allow.network."+intent.networkGrants[1].id)
	assertExpressionKinds(t, routed.Exprs, []string{
		"meta", "cmp", "meta", "cmp", "meta", "cmp", "payload", "bitwise", "cmp", "payload", "cmp",
		"meta", "cmp", "payload", "range", "immediate",
	})

	input := requireCompiledRule(t, compiled, "inet.input.allow."+intent.hostServices[0].id)
	assert.Contains(t, expressionKinds(input.Exprs), "range")
	assert.Equal(t, expr.VerdictAccept, finalVerdict(t, input.Exprs).Kind)

	nat := requireCompiledRule(t, compiled, "inet.nat.masquerade."+intent.networks[0].id)
	assert.Contains(t, expressionKinds(nat.Exprs), "masq")

	bridgeDrop := requireCompiledRule(t, compiled, "bridge.default-deny."+intent.endpoints[0].id)
	assert.Equal(t, expr.VerdictDrop, finalVerdict(t, bridgeDrop.Exprs).Kind)
	inetDrop := requireCompiledRule(t, compiled, "inet.forward.default-deny."+intent.networks[0].id)
	assert.Equal(t, expr.VerdictDrop, finalVerdict(t, inetDrop.Exprs).Kind)
	inputDrop := requireCompiledRule(t, compiled, "inet.input.default-deny."+intent.networks[0].id)
	assert.Equal(t, expr.VerdictDrop, finalVerdict(t, inputDrop.Exprs).Kind)
}

func TestCompileGenerationRejectsInvalidResolvedIntent(t *testing.T) {
	tests := map[string]func(*generationIntent){
		"generation identity": func(intent *generationIntent) {
			intent.id = "caller supplied text"
		},
		"duplicate VM identity": func(intent *generationIntent) {
			intent.endpoints[1].id = intent.endpoints[0].id
		},
		"unknown VM grant source": func(intent *generationIntent) {
			intent.vmGrants[0].sourceVMID = strings.Repeat("f", 32)
		},
		"interface name exceeds kernel limit": func(intent *generationIntent) {
			intent.endpoints[0].hostVeth = "interface-name-too-long"
		},
		"IPv6 endpoint": func(intent *generationIntent) {
			intent.endpoints[0].ipv4 = netip.MustParseAddr("2001:db8::1")
		},
		"reversed port range": func(intent *generationIntent) {
			intent.networkGrants[0].ports = portInterval{first: 9000, last: 8000}
		},
		"duplicate endpoint IPv4": func(intent *generationIntent) {
			intent.endpoints[1].ipv4 = intent.endpoints[0].ipv4
		},
		"duplicate endpoint MAC": func(intent *generationIntent) {
			intent.endpoints[1].mac = intent.endpoints[0].mac
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := compatibilityIntent()
			mutate(&intent)

			_, err := compileGeneration(intent)
			require.Error(t, err)
			assert.ErrorContains(t, err, "firewall generation is invalid")
		})
	}
}

func TestRuleUserDataRoundTripRejectsAmbiguity(t *testing.T) {
	generationID := strings.Repeat("a", 64)
	encoded, err := encodeRuleIdentity(ruleIdentity{generation: generationID, rule: "bridge.allow.vm.policy-1"})
	require.NoError(t, err)

	decoded, err := decodeRuleIdentity(encoded)
	require.NoError(t, err)
	want := ruleIdentity{generation: generationID, rule: "bridge.allow.vm.policy-1"}
	if diff := cmp.Diff(want, decoded, cmp.AllowUnexported(ruleIdentity{})); diff != "" {
		t.Errorf("rule identity mismatch (-want +got):\n%s", diff)
	}

	for name, malformed := range map[string][]byte{
		"truncated":        encoded[:len(encoded)-1],
		"trailing bytes":   append(append([]byte{}, encoded...), 0),
		"wrong magic":      append([]byte("NOTNFT01"), encoded[8:]...),
		"empty generation": append([]byte(ruleUserDataMagic), append([]byte{0, 1}, 'x')...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRuleIdentity(malformed)
			assert.Error(t, err)
		})
	}
}

func TestCompileGenerationIsDeterministicAcrossInputOrder(t *testing.T) {
	ordered := compatibilityIntent()
	shuffled := compatibilityIntent()
	shuffled.endpoints[0], shuffled.endpoints[2] = shuffled.endpoints[2], shuffled.endpoints[0]
	shuffled.networks[0], shuffled.networks[1] = shuffled.networks[1], shuffled.networks[0]
	shuffled.networkGrants[0], shuffled.networkGrants[1] = shuffled.networkGrants[1], shuffled.networkGrants[0]

	first, err := compileGeneration(ordered)
	require.NoError(t, err)
	second, err := compileGeneration(shuffled)
	require.NoError(t, err)

	assert.Equal(t, compiledRuleIdentities(t, first), compiledRuleIdentities(t, second))
}

func compatibilityIntent() generationIntent {
	return generationIntent{
		id: strings.Repeat("a", 64),
		endpoints: []endpointIntent{
			{
				id:        strings.Repeat("1", 32),
				networkID: strings.Repeat("b", 64),
				hostVeth:  "mvma0",
				ipv4:      netip.MustParseAddr("10.0.0.2"),
				mac:       macAddress{0x02, 0, 0, 0, 0, 1},
			},
			{
				id:        strings.Repeat("2", 32),
				networkID: strings.Repeat("b", 64),
				hostVeth:  "mvmb0",
				ipv4:      netip.MustParseAddr("10.0.0.3"),
				mac:       macAddress{0x02, 0, 0, 0, 0, 2},
			},
			{
				id:        strings.Repeat("3", 32),
				networkID: strings.Repeat("c", 64),
				hostVeth:  "mvmc0",
				ipv4:      netip.MustParseAddr("10.1.0.2"),
				mac:       macAddress{0x02, 0, 0, 0, 0, 3},
			},
		},
		networks: []networkIntent{
			{
				id:              strings.Repeat("b", 64),
				hostBridge:      "mvmbr0",
				subnet:          netip.MustParsePrefix("10.0.0.0/24"),
				hostIPv4:        netip.MustParseAddr("10.0.0.1"),
				uplinkInterface: "eth0",
				masquerade:      true,
			},
			{
				id:              strings.Repeat("c", 64),
				hostBridge:      "mvmbr1",
				subnet:          netip.MustParsePrefix("10.1.0.0/24"),
				hostIPv4:        netip.MustParseAddr("10.1.0.1"),
				uplinkInterface: "eth0",
				masquerade:      true,
			},
		},
		vmGrants: []vmTrafficGrant{
			{
				id: strings.Repeat("d", 32), sourceVMID: strings.Repeat("1", 32),
				destinationVMID: strings.Repeat("2", 32), protocol: protocolTCP,
				ports: portInterval{first: 443, last: 443},
			},
		},
		networkGrants: []networkTrafficGrant{
			{
				id: strings.Repeat("e", 32), sourceNetworkID: strings.Repeat("b", 64),
				destinationVMID: strings.Repeat("2", 32), protocol: protocolUDP,
				ports: portInterval{first: 8000, last: 8010},
			},
			{
				id: strings.Repeat("f", 32), sourceNetworkID: strings.Repeat("b", 64),
				destinationVMID: strings.Repeat("3", 32), protocol: protocolUDP,
				ports: portInterval{first: 5000, last: 5010},
			},
		},
		hostServices: []hostServiceIntent{
			{
				id: strings.Repeat("9", 32), sourceNetworkID: strings.Repeat("b", 64),
				protocol: protocolTCP, ports: portInterval{first: 8080, last: 8088},
			},
		},
	}
}

func requireCompiledRule(t *testing.T, compiled compiledGeneration, identity string) *nftables.Rule {
	t.Helper()
	for _, rule := range compiled.rules() {
		decoded, err := decodeRuleIdentity(rule.UserData)
		require.NoError(t, err)
		if decoded.rule == identity {
			return rule
		}
	}
	t.Fatalf("compiled rule %q not found", identity)
	return nil
}

func compiledRuleIdentities(t *testing.T, compiled compiledGeneration) []ruleIdentity {
	t.Helper()
	identities := make([]ruleIdentity, 0, len(compiled.rules()))
	for _, rule := range compiled.rules() {
		identity, err := decodeRuleIdentity(rule.UserData)
		require.NoError(t, err)
		identities = append(identities, identity)
	}
	return identities
}

func expectedInterfaceName(name string) []byte {
	result := make([]byte, 16)
	copy(result, name)
	return result
}

func expressionKinds(expressions []expr.Any) []string {
	result := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		switch expression.(type) {
		case *expr.Meta:
			result = append(result, "meta")
		case *expr.Payload:
			result = append(result, "payload")
		case *expr.Cmp:
			result = append(result, "cmp")
		case *expr.Range:
			result = append(result, "range")
		case *expr.Bitwise:
			result = append(result, "bitwise")
		case *expr.Ct:
			result = append(result, "ct")
		case *expr.Verdict:
			result = append(result, "immediate")
		case *expr.Masq:
			result = append(result, "masq")
		default:
			result = append(result, "unknown")
		}
	}
	return result
}

func assertExpressionKinds(t *testing.T, expressions []expr.Any, want []string) {
	t.Helper()
	if diff := cmp.Diff(want, expressionKinds(expressions)); diff != "" {
		t.Errorf("expression kinds mismatch (-want +got):\n%s", diff)
	}
}

func expressionData(expressions []expr.Any) [][]byte {
	result := make([][]byte, 0, len(expressions))
	for _, expression := range expressions {
		switch value := expression.(type) {
		case *expr.Cmp:
			result = append(result, value.Data)
		case *expr.Range:
			result = append(result, value.FromData, value.ToData)
		}
	}
	return result
}

func finalVerdict(t *testing.T, expressions []expr.Any) *expr.Verdict {
	t.Helper()
	require.NotEmpty(t, expressions)
	verdict, ok := expressions[len(expressions)-1].(*expr.Verdict)
	require.True(t, ok, "final expression has type %T, want *expr.Verdict", expressions[len(expressions)-1])
	return verdict
}
