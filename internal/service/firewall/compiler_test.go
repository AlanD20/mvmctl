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

func TestCompileGenerationSeparatesIngressGuardsFromForwardPolicy(t *testing.T) {
	intent := compatibilityIntent()
	routedGrant := vmTrafficGrant{
		id:              strings.Repeat("8", 32),
		sourceVMID:      intent.endpoints[0].id,
		destinationVMID: intent.endpoints[2].id,
		protocol:        protocolTCP,
		ports:           portInterval{first: 8443, last: 8443},
	}
	intent.vmGrants = append(intent.vmGrants, routedGrant)

	compiled, err := compileGeneration(intent)
	require.NoError(t, err)

	prerouting := requireCompiledChain(t, compiled.bridge, managedBridgePreroutingChainName)
	require.NotNil(t, prerouting.Hooknum)
	require.NotNil(t, prerouting.Priority)
	assert.Equal(t, *nftables.ChainHookPrerouting, *prerouting.Hooknum)
	assert.Equal(t, *nftables.ChainPriorityRaw, *prerouting.Priority)
	assert.Equal(t, nftables.ChainTypeFilter, prerouting.Type)

	forward := requireCompiledChain(t, compiled.bridge, managedBridgeForwardChainName)
	for _, identityPrefix := range []string{
		"bridge.spoof.mac.",
		"bridge.spoof.ipv4.",
		"bridge.spoof.arp-mac.",
		"bridge.spoof.arp-ipv4.",
		"bridge.unsupported-l3.",
	} {
		rule := requireCompiledRule(t, compiled, identityPrefix+intent.endpoints[0].id)
		assert.Same(t, prerouting, rule.Chain)
	}
	for _, identity := range []string{
		"bridge.arp." + intent.endpoints[0].id,
		"bridge.established." + intent.endpoints[0].id,
		"bridge.allow.vm." + intent.vmGrants[0].id,
		"bridge.default-deny." + intent.endpoints[0].id,
	} {
		assert.Same(t, forward, requireCompiledRule(t, compiled, identity).Chain)
	}

	routedRule := requireCompiledRule(t, compiled, "inet.forward.allow.vm."+routedGrant.id)
	assert.NotNil(t, routedRule)
	guard := requireCompiledRule(t, compiled, "bridge.spoof.ipv4."+routedGrant.sourceVMID)
	guard.Chain = forward
	assert.Error(t, validateIngressGuardInvariant(intent, compiled),
		"an exact-source routed policy must be rejected if its source ingress guard is absent from prerouting")
}

func TestCompileGenerationBindsInternetEgressToMasqueradeCapability(t *testing.T) {
	intent := compatibilityIntent()
	intent.networks[0].masquerade = false

	compiled, err := compileGeneration(intent)
	require.NoError(t, err)
	assertCompiledRuleAbsent(t, compiled, "inet.forward.egress."+intent.networks[0].id)
	assertCompiledRuleAbsent(t, compiled, "inet.nat.masquerade."+intent.networks[0].id)
	requireCompiledRule(t, compiled, "inet.forward.egress."+intent.networks[1].id)
	requireCompiledRule(t, compiled, "inet.nat.masquerade."+intent.networks[1].id)
}

func TestCompileGenerationRejectsInvalidResolvedIntent(t *testing.T) {
	tests := map[string]func(*generationIntent){
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
		"overlapping managed prefixes": func(intent *generationIntent) {
			intent.networks[1].subnet = netip.MustParsePrefix("10.0.0.0/25")
			intent.networks[1].hostIPv4 = netip.MustParseAddr("10.0.0.10")
			intent.endpoints[2].ipv4 = netip.MustParseAddr("10.0.0.20")
		},
		"network address as host": func(intent *generationIntent) {
			intent.networks[0].hostIPv4 = netip.MustParseAddr("10.0.0.0")
		},
		"broadcast address as host": func(intent *generationIntent) {
			intent.networks[0].hostIPv4 = netip.MustParseAddr("10.0.0.255")
		},
		"network address as endpoint": func(intent *generationIntent) {
			intent.endpoints[0].ipv4 = netip.MustParseAddr("10.0.0.0")
		},
		"broadcast address as endpoint": func(intent *generationIntent) {
			intent.endpoints[0].ipv4 = netip.MustParseAddr("10.0.0.255")
		},
		"host address as endpoint": func(intent *generationIntent) {
			intent.endpoints[0].ipv4 = intent.networks[0].hostIPv4
		},
		"prefix without usable hosts": func(intent *generationIntent) {
			intent.networks[0].subnet = netip.MustParsePrefix("10.0.0.0/31")
			intent.networks[0].hostIPv4 = netip.MustParseAddr("10.0.0.0")
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

func TestCompileGenerationAllowsAdjacentManagedPrefixes(t *testing.T) {
	intent := compatibilityIntent()
	intent.networks[1].subnet = netip.MustParsePrefix("10.0.1.0/24")
	intent.networks[1].hostIPv4 = netip.MustParseAddr("10.0.1.1")
	intent.endpoints[2].ipv4 = netip.MustParseAddr("10.0.1.2")

	_, err := compileGeneration(intent)
	require.NoError(t, err)
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
	ordered.vmGrants = append(ordered.vmGrants, vmTrafficGrant{
		id:              "policy-vm-second",
		sourceVMID:      ordered.endpoints[2].id,
		destinationVMID: ordered.endpoints[0].id,
		protocol:        protocolUDP,
		ports:           portInterval{first: 53, last: 53},
	})
	ordered.hostServices = append(ordered.hostServices, hostServiceIntent{
		id:              "service-second",
		sourceNetworkID: ordered.networks[1].id,
		protocol:        protocolUDP,
		ports:           portInterval{first: 5353, last: 5353},
	})
	shuffled := compatibilityIntent()
	shuffled.vmGrants = append(shuffled.vmGrants, vmTrafficGrant{
		id:              "policy-vm-second",
		sourceVMID:      shuffled.endpoints[2].id,
		destinationVMID: shuffled.endpoints[0].id,
		protocol:        protocolUDP,
		ports:           portInterval{first: 53, last: 53},
	})
	shuffled.hostServices = append(shuffled.hostServices, hostServiceIntent{
		id:              "service-second",
		sourceNetworkID: shuffled.networks[1].id,
		protocol:        protocolUDP,
		ports:           portInterval{first: 5353, last: 5353},
	})
	shuffled.endpoints[0], shuffled.endpoints[2] = shuffled.endpoints[2], shuffled.endpoints[0]
	shuffled.networks[0], shuffled.networks[1] = shuffled.networks[1], shuffled.networks[0]
	shuffled.vmGrants[0], shuffled.vmGrants[1] = shuffled.vmGrants[1], shuffled.vmGrants[0]
	shuffled.networkGrants[0], shuffled.networkGrants[1] = shuffled.networkGrants[1], shuffled.networkGrants[0]
	shuffled.hostServices[0], shuffled.hostServices[1] = shuffled.hostServices[1], shuffled.hostServices[0]

	first, err := compileGeneration(ordered)
	require.NoError(t, err)
	second, err := compileGeneration(shuffled)
	require.NoError(t, err)

	assert.Equal(t, first.id, second.id)
	if diff := cmp.Diff(
		compiledRuleIdentities(t, first),
		compiledRuleIdentities(t, second),
		cmp.AllowUnexported(ruleIdentity{}),
	); diff != "" {
		t.Errorf("compiled rule identities mismatch (-want +got):\n%s", diff)
	}
}

func TestCompileGenerationCanonicalDigestGoldenVectors(t *testing.T) {
	// Rationale: fixed literals make these vectors independent of the implementation under test. The empty vector is
	// also reproducible directly from the documented v1 TLV field sequence without compiling an nftables expression.
	tests := map[string]struct {
		intent    generationIntent
		wantID    string
		wantEmpty bool
	}{
		"explicit empty generation": {
			intent:    generationIntent{},
			wantID:    "a686883941eb9b92f4c31e6fd9e82861b1f0135f64954041cb890a4f5826f4bb",
			wantEmpty: true,
		},
		"complete compatibility generation": {
			intent: compatibilityIntent(),
			wantID: "3ade5c53dfc88ffda31d76216cd61f9174a63296266109f42fd265183916644e",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			compiled, err := compileGeneration(tc.intent)
			require.NoError(t, err)

			assert.Equal(t, tc.wantID, compiled.id)
			assert.Equal(t, tc.wantEmpty, compiled.empty)
			if tc.wantEmpty {
				assert.Empty(t, compiled.tableIdentities())
				assert.Empty(t, compiled.rules())
			}
		})
	}
}

func TestCompileGenerationDigestCoversSemanticIntentAndOutputOrder(t *testing.T) {
	baseline, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)

	intentMutations := map[string]func(*generationIntent){
		"endpoint identity": func(intent *generationIntent) {
			intent.endpoints[0].id = strings.Repeat("4", 32)
			intent.vmGrants[0].sourceVMID = intent.endpoints[0].id
		},
		"endpoint network": func(intent *generationIntent) {
			intent.endpoints[0].networkID = intent.networks[1].id
			intent.endpoints[0].ipv4 = netip.MustParseAddr("10.1.0.4")
		},
		"endpoint host interface": func(intent *generationIntent) {
			intent.endpoints[0].hostVeth = "mvma1"
		},
		"endpoint IPv4": func(intent *generationIntent) {
			intent.endpoints[0].ipv4 = netip.MustParseAddr("10.0.0.4")
		},
		"endpoint MAC": func(intent *generationIntent) {
			intent.endpoints[0].mac[5] = 4
		},
		"network host bridge": func(intent *generationIntent) {
			intent.networks[0].hostBridge = "mvmbr2"
		},
		"network identity": func(intent *generationIntent) {
			intent.networks[0].id = strings.Repeat("7", 64)
			for index := range 2 {
				intent.endpoints[index].networkID = intent.networks[0].id
			}
			intent.networkGrants[0].sourceNetworkID = intent.networks[0].id
			intent.networkGrants[1].sourceNetworkID = intent.networks[0].id
			intent.hostServices[0].sourceNetworkID = intent.networks[0].id
		},
		"network subnet": func(intent *generationIntent) {
			intent.networks[0].subnet = netip.MustParsePrefix("10.2.0.0/24")
			intent.networks[0].hostIPv4 = netip.MustParseAddr("10.2.0.1")
			intent.endpoints[0].ipv4 = netip.MustParseAddr("10.2.0.2")
			intent.endpoints[1].ipv4 = netip.MustParseAddr("10.2.0.3")
		},
		"network host IPv4": func(intent *generationIntent) {
			intent.networks[0].hostIPv4 = netip.MustParseAddr("10.0.0.254")
		},
		"network uplink": func(intent *generationIntent) {
			intent.networks[0].uplinkInterface = "eth1"
		},
		"network masquerade": func(intent *generationIntent) {
			intent.networks[0].masquerade = false
		},
		"VM grant protocol": func(intent *generationIntent) {
			intent.vmGrants[0].protocol = protocolUDP
		},
		"VM grant identity": func(intent *generationIntent) {
			intent.vmGrants[0].id = "policy-vm-changed"
		},
		"VM grant source": func(intent *generationIntent) {
			intent.vmGrants[0].sourceVMID = intent.endpoints[2].id
		},
		"VM grant destination": func(intent *generationIntent) {
			intent.vmGrants[0].destinationVMID = intent.endpoints[2].id
		},
		"VM grant first port": func(intent *generationIntent) {
			intent.vmGrants[0].ports = portInterval{first: 444, last: 444}
		},
		"VM grant last port": func(intent *generationIntent) {
			intent.vmGrants[0].ports = portInterval{first: 443, last: 444}
		},
		"network grant identity": func(intent *generationIntent) {
			intent.networkGrants[1].id = "policy-network-changed"
		},
		"network grant source": func(intent *generationIntent) {
			intent.networkGrants[1].sourceNetworkID = strings.Repeat("c", 64)
		},
		"network grant destination": func(intent *generationIntent) {
			intent.networkGrants[1].destinationVMID = intent.endpoints[1].id
		},
		"network grant protocol": func(intent *generationIntent) {
			intent.networkGrants[1].protocol = protocolTCP
		},
		"network grant first port": func(intent *generationIntent) {
			intent.networkGrants[1].ports = portInterval{first: 5001, last: 5010}
		},
		"network grant last port": func(intent *generationIntent) {
			intent.networkGrants[1].ports = portInterval{first: 5000, last: 5011}
		},
		"host service identity": func(intent *generationIntent) {
			intent.hostServices[0].id = "service-changed"
		},
		"host service source": func(intent *generationIntent) {
			intent.hostServices[0].sourceNetworkID = intent.networks[1].id
		},
		"host service protocol": func(intent *generationIntent) {
			intent.hostServices[0].protocol = protocolUDP
		},
		"host service first port": func(intent *generationIntent) {
			intent.hostServices[0].ports = portInterval{first: 8081, last: 8088}
		},
		"host service last port": func(intent *generationIntent) {
			intent.hostServices[0].ports = portInterval{first: 8080, last: 8089}
		},
	}

	for name, mutate := range intentMutations {
		t.Run(name, func(t *testing.T) {
			intent := compatibilityIntent()
			mutate(&intent)

			compiled, err := compileGeneration(intent)
			require.NoError(t, err)
			assert.NotEqual(t, baseline.id, compiled.id)
		})
	}

	outputMutations := map[string]func(*compiledGeneration){
		"table family": func(generation *compiledGeneration) {
			generation.bridge.table.Family = nftables.TableFamilyIPv4
		},
		"table name": func(generation *compiledGeneration) {
			generation.bridge.table.Name = "mvmctl_bridge_changed"
		},
		"table flags": func(generation *compiledGeneration) {
			generation.bridge.table.Flags = 1
		},
		"chain order": func(generation *compiledGeneration) {
			generation.bridge.chains[0], generation.bridge.chains[1] =
				generation.bridge.chains[1], generation.bridge.chains[0]
		},
		"chain name": func(generation *compiledGeneration) {
			generation.bridge.chains[0].Name = "prerouting-changed"
		},
		"chain hook": func(generation *compiledGeneration) {
			hook := nftables.ChainHookInput
			generation.bridge.chains[0].Hooknum = hook
		},
		"chain priority": func(generation *compiledGeneration) {
			priority := nftables.ChainPriority(-299)
			generation.bridge.chains[0].Priority = &priority
		},
		"chain type": func(generation *compiledGeneration) {
			generation.bridge.chains[0].Type = nftables.ChainTypeRoute
		},
		"chain policy": func(generation *compiledGeneration) {
			policy := nftables.ChainPolicyDrop
			generation.bridge.chains[0].Policy = &policy
		},
		"chain device": func(generation *compiledGeneration) {
			generation.bridge.chains[0].Device = "eth0"
		},
		"rule identity": func(generation *compiledGeneration) {
			generation.bridge.rules[0].identity = "bridge.spoof.mac.changed"
		},
		"rule table": func(generation *compiledGeneration) {
			generation.bridge.rules[0].rule.Table = generation.inet.table
		},
		"rule chain": func(generation *compiledGeneration) {
			generation.bridge.rules[0].rule.Chain = generation.bridge.chains[1]
		},
		"rule position": func(generation *compiledGeneration) {
			generation.bridge.rules[0].rule.Position = 1
		},
		"rule flags": func(generation *compiledGeneration) {
			generation.bridge.rules[0].rule.Flags = 1
		},
		"expression field": func(generation *compiledGeneration) {
			meta := generation.bridge.rules[0].rule.Exprs[0].(*expr.Meta)
			meta.Register = 2
		},
		"compiled rule order": func(generation *compiledGeneration) {
			generation.bridge.rules[0], generation.bridge.rules[1] =
				generation.bridge.rules[1], generation.bridge.rules[0]
		},
		"compiled expression order": func(generation *compiledGeneration) {
			generation.bridge.rules[0].rule.Exprs[0], generation.bridge.rules[0].rule.Exprs[1] =
				generation.bridge.rules[0].rule.Exprs[1], generation.bridge.rules[0].rule.Exprs[0]
		},
	}

	for name, mutate := range outputMutations {
		t.Run(name, func(t *testing.T) {
			mutated, err := compileGeneration(compatibilityIntent())
			require.NoError(t, err)
			mutate(&mutated)

			got, err := deriveGenerationID(compatibilityIntent(), mutated)
			require.NoError(t, err)
			assert.NotEqual(t, baseline.id, got)
		})
	}
}

func TestCompileGenerationBindsDerivedDigestToRuleUserData(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)

	require.NotEmpty(t, compiled.rules())
	for _, rule := range compiled.rules() {
		identity, err := decodeRuleIdentity(rule.UserData)
		require.NoError(t, err)
		assert.Equal(t, compiled.id, identity.generation)
	}
}

func TestDeriveGenerationIDRejectsInconsistentCompiledOutput(t *testing.T) {
	tests := map[string]func(generationIntent, compiledGeneration) (generationIntent, compiledGeneration){
		"empty marker with non-empty intent": func(
			intent generationIntent,
			generation compiledGeneration,
		) (generationIntent, compiledGeneration) {
			generation.empty = true
			return intent, generation
		},
		"empty generation with table output": func(
			_ generationIntent,
			generation compiledGeneration,
		) (generationIntent, compiledGeneration) {
			return generationIntent{}, compiledGeneration{empty: true, bridge: generation.bridge}
		},
		"rule without identity": func(
			intent generationIntent,
			generation compiledGeneration,
		) (generationIntent, compiledGeneration) {
			generation.bridge.rules[0].identity = ""
			return intent, generation
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := compatibilityIntent()
			compiled, err := compileGeneration(intent)
			require.NoError(t, err)
			intent, compiled = mutate(intent, compiled)

			_, err = deriveGenerationID(intent, compiled)
			require.Error(t, err)
			assert.ErrorContains(t, err, "firewall generation is invalid")
		})
	}
}

func compatibilityIntent() generationIntent {
	return generationIntent{
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

func assertCompiledRuleAbsent(t *testing.T, compiled compiledGeneration, identity string) {
	t.Helper()
	for _, rule := range compiled.rules() {
		decoded, err := decodeRuleIdentity(rule.UserData)
		require.NoError(t, err)
		assert.NotEqual(t, identity, decoded.rule)
	}
}

func requireCompiledChain(t *testing.T, table compiledTable, name string) *nftables.Chain {
	t.Helper()
	for _, chain := range table.chains {
		if chain.Name == name {
			return chain
		}
	}
	t.Fatalf("compiled chain %q not found", name)
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
