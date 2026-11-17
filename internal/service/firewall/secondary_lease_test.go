package firewall

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
)

func TestSecondaryLeaseKeyBindsExactInterfaceAndIPv4(t *testing.T) {
	want := [20]byte{'m', 'v', 'm', 'v', '-', 'a', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 25, 0, 100}
	key, err := secondaryLeaseKey("mvmv-a", netip.MustParseAddr("10.25.0.100"))
	require.NoError(t, err)
	require.Equal(t, want, key)
	name, address, err := decodeSecondaryLeaseKey(want[:])
	require.NoError(t, err)
	require.Equal(t, "mvmv-a", name)
	require.Equal(t, netip.MustParseAddr("10.25.0.100"), address)
	other, err := secondaryLeaseKey("mvmv-b", address)
	require.NoError(t, err)
	require.NotEqual(t, key, other, "one guest's key must not authorize another interface")
	for _, tc := range []struct{ name, address string }{
		{"", "10.25.0.100"}, {"long-interface-name", "10.25.0.100"}, {"x/y", "10.25.0.100"},
		{"mvmv-a", "8.8.8.8"}, {"mvmv-a", "::ffff:10.25.0.100"}, {"mvmv-a", "fd00::1"},
	} {
		_, err := secondaryLeaseKey(tc.name, netip.MustParseAddr(tc.address))
		require.Error(t, err)
	}
}

func TestSecondaryLeaseKeyRejectsAmbiguousPaddingAndShape(t *testing.T) {
	for name, raw := range map[string][]byte{
		"short": make([]byte, 19), "long": make([]byte, 21), "empty interface": make([]byte, 20),
		"no terminator":     []byte{'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 10, 1, 0, 2},
		"nonzero padding":   []byte{'a', 0, 'b', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 1, 0, 2},
		"foreign interface": []byte{'a', '/', 'b', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 1, 0, 2},
		"public IPv4":       []byte{'a', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 8, 8, 8, 8},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeSecondaryLeaseKey(raw)
			require.Error(t, err)
		})
	}
}

func TestSecondaryLeaseLookupAcceptsOnlyTheClosedInvertedGuard(t *testing.T) {
	valid := []netlink.Attribute{
		{Type: 1, Data: []byte("secondary_ipv4\x00")},
		{Type: 2, Data: testBigEndianUint32(1)},
		{Type: 5, Data: testBigEndianUint32(1)},
	}
	got, err := decodeKernelExpression("lookup", mustMarshalKernelAttributes(t, valid), nftables.TableFamilyBridge)
	require.NoError(t, err)
	want := &expr.Lookup{SourceRegister: 1, SetName: "secondary_ipv4", Invert: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("lookup (-want +got):\n%s", diff)
	}
	for name, attrs := range map[string][]netlink.Attribute{
		"foreign set":       {{Type: 1, Data: []byte("other\x00")}, valid[1], valid[2]},
		"wrong register":    {valid[0], {Type: 2, Data: testBigEndianUint32(2)}, valid[2]},
		"non-inverted":      {valid[0], valid[1], {Type: 5, Data: testBigEndianUint32(0)}},
		"unknown flags":     {valid[0], valid[1], {Type: 5, Data: testBigEndianUint32(3)}},
		"map destination":   {valid[0], valid[1], valid[2], {Type: 3, Data: testBigEndianUint32(2)}},
		"transaction ID":    {valid[0], valid[1], valid[2], {Type: 4, Data: testBigEndianUint32(1)}},
		"duplicate field":   {valid[0], valid[1], valid[1], valid[2]},
		"missing inversion": valid[:2],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSecondaryLeaseLookup(mustMarshalKernelAttributes(t, attrs))
			require.Error(t, err)
		})
	}
}

// IPv4 source/destination offsets are12/16; Ethernet/IPv4 ARP's sender protocol address starts at14.
// The16-byte interface register followed by the4-byte address register forms the Linux20-byte concatenated key.
func TestSecondaryLeaseGuardsBindPacketOffsetsAndInterfaceDirection(t *testing.T) {
	primary := netip.MustParseAddr("10.25.0.2")
	for _, tc := range []struct {
		name string
		got  []expr.Any
		want []expr.Any
	}{
		{"IPv4 source", rejectSecondaryIPv4Source(primary), []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
			&expr.Cmp{Register: 1, Op: expr.CmpOpNeq, Data: []byte{10, 25, 0, 2}},
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
			&expr.Lookup{SourceRegister: 1, SetName: "secondary_ipv4", Invert: true},
		}},
		{"ARP sender", rejectSecondaryARPSource(primary), []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 14, Len: 4},
			&expr.Cmp{Register: 1, Op: expr.CmpOpNeq, Data: []byte{10, 25, 0, 2}},
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: 14, Len: 4},
			&expr.Lookup{SourceRegister: 1, SetName: "secondary_ipv4", Invert: true},
		}},
		{"destination fence", rejectSecondaryDestination(networkIntent{hostBridge: "mvmb-a"},
			netip.MustParseAddr("10.25.0.100")), []expr.Any{
			&expr.Meta{Key: expr.MetaKeyBRIOIFNAME, Register: 1},
			&expr.Cmp{Register: 1, Op: expr.CmpOpEq, Data: expectedInterfaceName("mvmb-a")},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
			&expr.Cmp{Register: 1, Op: expr.CmpOpEq, Data: []byte{8, 0}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Cmp{Register: 1, Op: expr.CmpOpEq, Data: []byte{10, 25, 0, 100}},
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Lookup{SourceRegister: 1, SetName: "secondary_ipv4", Invert: true},
			&expr.Verdict{Kind: expr.VerdictDrop},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.got); diff != "" {
				t.Fatalf("guard expressions (-want +got):\n%s", diff)
			}
		})
	}
}
