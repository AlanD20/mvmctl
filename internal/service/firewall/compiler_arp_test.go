package firewall

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: Linux include/uapi/linux/if_arp.h defines struct arphdr as two big-endian uint16 types, two uint8
// lengths, and a big-endian uint16 operation. Ethernet/IPv4 uses 1, 0x0800, 6, 4, and request/reply operations 1/2.
// These literal offsets and values come from that packet contract, independently of the compiler helpers.
func TestCompileGenerationARPGrammarExpressions(t *testing.T) {
	intent := compatibilityIntent()
	compiled, err := compileGeneration(intent)
	require.NoError(t, err)
	endpoint := intent.endpoints[0]
	prefix := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName(endpoint.hostVeth)},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x08, 0x06}},
	}
	header := requireCompiledRule(t, compiled, "bridge.spoof.arp-header."+endpoint.id)
	wantHeader := append(slices.Clone(prefix),
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 0, Len: 6},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 1, 8, 0, 6, 4}},
		&expr.Verdict{Kind: expr.VerdictDrop},
	)
	if diff := cmp.Diff(wantHeader, header.Exprs); diff != "" {
		t.Errorf("ARP header guard mismatch (-want +got):\n%s", diff)
	}
	opcode := requireCompiledRule(t, compiled, "bridge.spoof.arp-operation."+endpoint.id)
	wantOpcode := append(slices.Clone(prefix),
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 6, Len: 2},
		&expr.Range{Op: expr.CmpOpNeq, Register: 1, FromData: []byte{0, 1}, ToData: []byte{0, 2}},
		&expr.Verdict{Kind: expr.VerdictDrop},
	)
	if diff := cmp.Diff(wantOpcode, opcode.Exprs); diff != "" {
		t.Errorf("ARP opcode guard mismatch (-want +got):\n%s", diff)
	}
	allow := requireCompiledRule(t, compiled, "bridge.arp."+endpoint.id)
	wantAllow := append(slices.Clone(prefix),
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 0, Len: 6},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0, 1, 8, 0, 6, 4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 6, Len: 2},
		&expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: []byte{0, 1}, ToData: []byte{0, 2}},
		// Ethernet/IPv4 ARP is 8 + 6 + 4 + 6 + 4 = 28 bytes; the last field begins at offset 24.
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 4},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)
	if diff := cmp.Diff(wantAllow, allow.Exprs); diff != "" {
		t.Errorf("ARP allow grammar mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Endpoint-local ARP primitives can be shared only while canonical encoding and dependency marshaling
// leave compiled output unchanged. Digest scratch must also never back retained rule userdata.
func TestCompileGenerationARPOutputSurvivesDigestAndMarshaling(t *testing.T) {
	intent := compatibilityIntent()
	compiled, err := compileGeneration(intent)
	require.NoError(t, err)
	rules := compiled.rules()
	userdata := make([][]byte, 0, len(rules))
	for _, rule := range rules {
		userdata = append(userdata, slices.Clone(rule.UserData))
		for _, expression := range rule.Exprs {
			_, err := expr.Marshal(byte(rule.Table.Family), expression)
			require.NoError(t, err)
		}
	}
	id, err := deriveGenerationID(intent, compiled)
	require.NoError(t, err)
	assert.Equal(t, compiled.id, id)
	for index, rule := range rules {
		assert.Equal(t, userdata[index], rule.UserData)
	}
	require.NoError(t, validateKernelApplicationTarget(compiled))
}

// Rationale: A failed or short payload load breaks a rule; it is not a mismatch verdict. The allow must therefore
// positively require both the supported header and the complete address payload, even when earlier drop rules miss.
func TestCompileGenerationARPAllowRejectsMalformedAndTruncatedPackets(t *testing.T) {
	intent := compatibilityIntent()
	compiled, err := compileGeneration(intent)
	require.NoError(t, err)
	allow := requireCompiledRule(t, compiled, "bridge.arp."+intent.endpoints[0].id)
	packet := []byte{
		0, 1, 8, 0, 6, 4, 0, 1,
		2, 0, 0, 0, 0, 1, 10, 0, 0, 2,
		0, 0, 0, 0, 0, 0, 10, 0, 0, 1,
	}
	tests := map[string]struct {
		offset int
		value  byte
	}{
		"other hardware type":        {offset: 1, value: 2},
		"little endian hardware":     {offset: 0, value: 1},
		"other protocol type":        {offset: 2, value: 0x86},
		"short hardware address":     {offset: 4, value: 5},
		"long hardware address":      {offset: 4, value: 7},
		"short protocol address":     {offset: 5, value: 3},
		"long protocol address":      {offset: 5, value: 16},
		"zero operation":             {offset: 7, value: 0},
		"reverse ARP operation":      {offset: 7, value: 3},
		"operation above byte range": {offset: 6, value: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			invalid := slices.Clone(packet)
			invalid[tc.offset] = tc.value
			assert.False(t, arpRuleMatches(t, allow.Exprs, invalid, intent.endpoints[0].hostVeth))
		})
	}
	for length := range len(packet) {
		t.Run(fmt.Sprintf("truncated_%d_bytes", length), func(t *testing.T) {
			assert.False(t, arpRuleMatches(t, allow.Exprs, packet[:length], intent.endpoints[0].hostVeth))
		})
	}
	for _, operation := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("complete_operation_%d", operation), func(t *testing.T) {
			valid := slices.Clone(packet)
			binary.BigEndian.PutUint16(valid[6:8], operation)
			assert.True(t, arpRuleMatches(t, allow.Exprs, valid, intent.endpoints[0].hostVeth))
			valid = append(valid, make([]byte, 18)...)
			assert.True(t, arpRuleMatches(t, allow.Exprs, valid, intent.endpoints[0].hostVeth),
				"Ethernet frame padding is allowed")
		})
	}
}

// arpRuleMatches evaluates only the Payload/Meta/Cmp/Range/Verdict subset in the ARP rule, with one register.
// It is an emitted-rule oracle, not a live-kernel qualification. Unsupported instructions fail the test.
func arpRuleMatches(t *testing.T, expressions []expr.Any, payload []byte, inputInterface string) bool {
	t.Helper()
	var register []byte
	for _, expression := range expressions {
		switch value := expression.(type) {
		case *expr.Meta:
			require.Equal(t, expr.MetaKeyIIFNAME, value.Key)
			require.Equal(t, uint32(1), value.Register)
			register = expectedInterfaceName(inputInterface)
		case *expr.Payload:
			require.Equal(t, uint32(1), value.DestRegister)
			data := payload
			if value.Base == expr.PayloadBaseLLHeader {
				data = make([]byte, 14)
				copy(data[12:], []byte{8, 6})
			} else {
				require.Equal(t, expr.PayloadBaseNetworkHeader, value.Base)
			}
			end := uint64(value.Offset) + uint64(value.Len)
			if end > uint64(len(data)) {
				return false
			}
			register = data[value.Offset:end]
		case *expr.Cmp:
			require.Equal(t, uint32(1), value.Register)
			require.Contains(t, []expr.CmpOp{expr.CmpOpEq, expr.CmpOpNeq}, value.Op)
			if bytes.Equal(register, value.Data) != (value.Op == expr.CmpOpEq) {
				return false
			}
		case *expr.Range:
			require.Equal(t, uint32(1), value.Register)
			require.Contains(t, []expr.CmpOp{expr.CmpOpEq, expr.CmpOpNeq}, value.Op)
			within := bytes.Compare(register, value.FromData) >= 0 && bytes.Compare(register, value.ToData) <= 0
			if within != (value.Op == expr.CmpOpEq) {
				return false
			}
		case *expr.Verdict:
			require.Equal(t, expr.VerdictAccept, value.Kind)
			return true
		default:
			t.Fatalf("unsupported ARP instruction %T", expression)
		}
	}
	return false
}

// Rationale: Each grammar guard must precede fixed-offset sender checks and survive both invariant boundaries.
// Resealing a malformed value isolates the application inventory check from the separate integrity seal check.
func TestCompileGenerationARPGuardsAreRequiredBeforeSenderChecks(t *testing.T) {
	for _, field := range []string{"header", "operation"} {
		t.Run(field, func(t *testing.T) {
			intent := compatibilityIntent()
			compiled, err := compileGeneration(intent)
			require.NoError(t, err)
			identity := "bridge.spoof.arp-" + field + "." + intent.endpoints[0].id
			guard := requireCompiledRule(t, compiled, identity)
			assert.Same(t, compiled.bridge.chains[0], guard.Chain)
			guardIndex := -1
			senderIndex := -1
			for index, rule := range compiled.bridge.rules {
				if rule.identity == identity {
					guardIndex = index
				}
				if rule.identity == "bridge.spoof.arp-mac."+intent.endpoints[0].id {
					senderIndex = index
				}
			}
			require.NotEqual(t, -1, guardIndex)
			require.NotEqual(t, -1, senderIndex)
			assert.Less(t, guardIndex, senderIndex)
			require.NoError(t, validateKernelApplicationTarget(compiled))

			guard.Chain = compiled.bridge.chains[1]
			assert.Error(t, validateIngressGuardInvariant(intent, compiled))
			require.NoError(t, bindGenerationApplicationSeal(&compiled))
			assert.Error(t, validateKernelApplicationTarget(compiled))

			guard.Chain = compiled.bridge.chains[0]
			compiled.bridge.rules = slices.Delete(compiled.bridge.rules, guardIndex, guardIndex+1)
			require.NoError(t, bindGenerationApplicationSeal(&compiled))
			assert.Error(t, validateKernelApplicationTarget(compiled))
		})
	}
}
