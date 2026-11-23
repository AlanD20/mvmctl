package firewall

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostTrafficCompilerRestrictsGatewayAndSource(t *testing.T) {
	// Rationale: A host allow must not become a wildcard host-address, protocol, port or source-VM grant.
	cases := map[string]struct {
		networkSource bool
		protocol      protocol
		ports         portInterval
	}{
		"exact VM TCP port": {protocol: protocolTCP, ports: portInterval{first: 5432, last: 5432}},
		"network UDP range": {networkSource: true, protocol: protocolUDP, ports: portInterval{first: 53, last: 54}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			intent := compatibilityIntent()
			id := strings.Repeat("f", 64)
			identity := "inet.input.allow.vm-host." + id
			if tc.networkSource {
				intent.networkHostGrants = []networkHostTrafficGrant{{
					id: id, sourceNetworkID: intent.networks[0].id, protocol: tc.protocol, ports: tc.ports,
				}}
				identity = "inet.input.allow.network-host." + id
			} else {
				intent.vmHostGrants = []vmHostTrafficGrant{{
					id: id, sourceVMID: intent.endpoints[0].id, protocol: tc.protocol, ports: tc.ports,
				}}
			}
			compiled, err := compileGeneration(intent)
			require.NoError(t, err)
			rule := requireCompiledRule(t, compiled, identity)
			assert.Equal(t, "input", rule.Chain.Name)
			// CONTRACT: Match the input bridge and IPv4 source, then the gateway at IPv4 offset 16.
			want := []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName("mvmbr0")},
				&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{2}},
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
			}
			if tc.networkSource {
				want = append(want,
					&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
						Mask: []byte{255, 255, 255, 0}, Xor: []byte{0, 0, 0, 0}},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 0, 0, 0}})
			} else {
				want = append(want, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 0, 0, 2}})
			}
			want = append(want,
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 0, 0, 1}},
				&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(tc.protocol)}},
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2})
			if tc.networkSource {
				want = append(want, &expr.Range{Op: expr.CmpOpEq, Register: 1,
					FromData: []byte{0, 53}, ToData: []byte{0, 54}})
			} else {
				want = append(want, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x15, 0x38}})
			}
			want = append(want, &expr.Verdict{Kind: expr.VerdictAccept})
			if diff := cmp.Diff(want, rule.Exprs); diff != "" {
				t.Errorf("host grant expressions (-want +got):\n%s", diff)
			}
			allowIndex, denyIndex := -1, -1
			for index, candidate := range compiled.inet.rules {
				if candidate.identity == identity {
					allowIndex = index
				}
				if candidate.identity == "inet.input.default-deny."+intent.networks[0].id {
					denyIndex = index
				}
			}
			assert.Greater(t, denyIndex, allowIndex)
			for _, chain := range compiled.inet.chains {
				assert.NotEqual(
					t,
					"output",
					chain.Name,
					"host-originated traffic must remain outside policy management",
				)
			}
		})
	}
}

func TestHostTrafficCompilerRejectsInvalidGrants(t *testing.T) {
	cases := map[string]func(*generationIntent){
		"missing VM": func(i *generationIntent) { i.vmHostGrants[0].sourceVMID = "unknown" },
		"missing network": func(i *generationIntent) {
			i.vmHostGrants = nil
			i.networkHostGrants = []networkHostTrafficGrant{{id: "host", sourceNetworkID: "unknown",
				protocol: protocolTCP, ports: portInterval{first: 1, last: 1}}}
		},
		"implicit ports":       func(i *generationIntent) { i.vmHostGrants[0].ports = portInterval{} },
		"descending ports":     func(i *generationIntent) { i.vmHostGrants[0].ports = portInterval{first: 5, last: 4} },
		"unsupported protocol": func(i *generationIntent) { i.vmHostGrants[0].protocol = 1 },
		"duplicate identity":   func(i *generationIntent) { i.vmHostGrants[0].id = i.vmGrants[0].id },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			intent := compatibilityIntent()
			intent.vmHostGrants = []vmHostTrafficGrant{{id: "host", sourceVMID: intent.endpoints[0].id,
				protocol: protocolTCP, ports: portInterval{first: 1, last: 65535}}}
			mutate(&intent)
			_, err := compileGeneration(intent)
			assert.Error(t, err)
		})
	}
}

func TestHostTrafficGenerationBindsIntentAndReturnsToExactBefore(t *testing.T) {
	// Rationale: Adding and revoking host access must preserve exact-before admission for existing generations.
	intent := compatibilityIntent()
	before, err := compileGeneration(intent)
	require.NoError(t, err)
	intent.vmHostGrants = []vmHostTrafficGrant{{id: "host", sourceVMID: intent.endpoints[0].id,
		protocol: protocolTCP, ports: portInterval{first: 5432, last: 5432}}}
	withHost, err := compileGeneration(intent)
	require.NoError(t, err)
	assert.NotEqual(t, before.id, withHost.id)
	intent.vmHostGrants[0].sourceVMID = intent.endpoints[1].id
	otherSource, err := compileGeneration(intent)
	require.NoError(t, err)
	assert.NotEqual(t, withHost.id, otherSource.id)
	intent.vmHostGrants = nil
	after, err := compileGeneration(intent)
	require.NoError(t, err)
	assert.Equal(t, before.id, after.id)
}
