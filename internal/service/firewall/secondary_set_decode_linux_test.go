package firewall

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// These fixtures encode Linux UAPI attributes directly, independently of AddSet and the decoder.
func secondaryLeaseSetFixture(t *testing.T) netlink.Message {
	t.Helper()
	field := func(width uint32) netlink.Attribute {
		return netlink.Attribute{Type: 1, Data: mustMarshalKernelAttributes(t, []netlink.Attribute{
			{Type: 1, Data: testBigEndianUint32(width)},
		})}
	}
	concat := mustMarshalKernelAttributes(t, []netlink.Attribute{field(16), field(4)})
	desc := mustMarshalKernelAttributes(t, []netlink.Attribute{
		{Type: 1, Data: testBigEndianUint32(0)},
		{Type: 2, Data: concat},
	})
	return testKernelMessage(t, unix.NFT_MSG_NEWSET, nftables.TableFamilyBridge, netlink.Multi, []netlink.Attribute{
		{Type: 1, Data: []byte("mvmctl_bridge\x00")},
		{Type: 2, Data: []byte("secondary_ipv4\x00")},
		{Type: 3, Data: testBigEndianUint32(0x90)},          // TIMEOUT | CONCAT, no other flags.
		{Type: 4, Data: testBigEndianUint32((41 << 6) | 7)}, // ifname . ipv4_addr.
		{Type: 5, Data: testBigEndianUint32(20)},
		{Type: 8, Data: testBigEndianUint32(0)},
		{Type: 9, Data: desc},
		{Type: 11, Data: testBigEndianUint64(0)},
		{Type: 13, Data: []byte{0, 4, 2, 0, 0, 0}},
		{Type: 14},
		{Type: 16, Data: testBigEndianUint64(42)},
	})
}

func TestDecodeSecondaryLeaseSetReturnsClosedMetadata(t *testing.T) {
	for _, withID := range []bool{false, true} {
		name := "without_transaction_id"
		if withID {
			name = "ignores_transaction_id"
		}
		t.Run(name, func(t *testing.T) {
			message := secondaryLeaseSetFixture(t)
			if withID {
				mutateMessageAttributes(t, &message, func(attrs []netlink.Attribute) []netlink.Attribute {
					return append(attrs, netlink.Attribute{Type: 10, Data: testBigEndianUint32(900)})
				})
			}
			sequence := kernelBaseSequence{}
			got, err := decodeSecondaryLeaseSet(message, &sequence)
			require.NoError(t, err)
			want := &nftables.Set{
				Table: &nftables.Table{Family: nftables.TableFamilyBridge, Name: "mvmctl_bridge"},
				Name:  "secondary_ipv4", HasTimeout: true, Concatenation: true,
				KeyType:      nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr),
				KeyByteOrder: binaryutil.BigEndian,
			}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(nftables.SetDatatype{})); diff != "" {
				t.Errorf("decoded metadata (-want +got):\n%s", diff)
			}
			require.True(t, sequence.set)
			require.Equal(t, testNFTBaseSequence, sequence.value)
		})
	}
}

func TestDecodeSecondaryLeaseSetRejectsForeignMetadata(t *testing.T) {
	mutations := map[string]func(*testing.T, *netlink.Message){
		"foreign_family": func(_ *testing.T, m *netlink.Message) { m.Data[0] = byte(nftables.TableFamilyIPv4) },
		"foreign_table": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 1, []byte("foreign\x00"))
		},
		"foreign_name": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 2, []byte("foreign\x00"))
		},
		"wrong_key_type": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 4, testBigEndianUint32((7<<6)|41))
		},
		"wrong_key_length": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 5, testBigEndianUint32(24))
		},
		"default_timeout": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 11, testBigEndianUint64(1000))
		},
		"memory_policy": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 8, testBigEndianUint32(1))
		},
		"zero_handle": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 16, testBigEndianUint64(0))
		},
		"short_handle": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 16, []byte{1})
		},
		"foreign_userdata": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 13, []byte{0, 4, 1, 0, 0, 0})
		},
		"truncated_userdata": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 13, []byte{0, 255})
		},
		"padding_payload": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 14, []byte{0})
		},
		"duplicate_name":       func(t *testing.T, m *netlink.Message) { appendMessageAttribute(t, m, 2) },
		"unknown_attribute_21": func(t *testing.T, m *netlink.Message) { appendMessageAttribute(t, m, 21) },
		"malformed_backend_type": func(t *testing.T, m *netlink.Message) {
			mutateMessageAttributes(t, m, func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: 19, Data: []byte("not-terminated")})
			})
		},
		"malformed_element_count": func(t *testing.T, m *netlink.Message) {
			mutateMessageAttributes(t, m, func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: 20, Data: []byte{1}})
			})
		},
		"unknown_attribute": func(t *testing.T, m *netlink.Message) { appendMessageAttribute(t, m, 127) },
		"nested_flag":       func(t *testing.T, m *netlink.Message) { setMessageAttributeFlags(t, m, 9, netlink.Nested) },
		"response_flags":    func(_ *testing.T, m *netlink.Message) { m.Header.Flags = 0 },
		"response_type":     func(_ *testing.T, m *netlink.Message) { m.Header.Type++ },
		"response_length":   func(_ *testing.T, m *netlink.Message) { m.Header.Length-- },
		"netfilter_version": func(_ *testing.T, m *netlink.Message) { m.Data[1] = 1 },
	}
	for _, attrType := range []uint16{6, 7, 15, 17, 18} {
		mutations["foreign_attribute_"+strconv.Itoa(int(attrType))] = func(t *testing.T, m *netlink.Message) {
			appendMessageAttribute(t, m, attrType)
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			message := secondaryLeaseSetFixture(t)
			mutate(t, &message)
			got, err := decodeSecondaryLeaseSet(message, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
	for name, flags := range map[string]uint32{
		"no_timeout": 0x80, "no_concat": 0x10, "anonymous": 0x91, "constant": 0x92,
		"interval": 0x94, "map": 0x98, "dynamic": 0xb0, "object": 0xd0, "expression": 0x190,
	} {
		t.Run(name, func(t *testing.T) {
			message := secondaryLeaseSetFixture(t)
			setMessageAttributeData(t, &message, 3, testBigEndianUint32(flags))
			got, err := decodeSecondaryLeaseSet(message, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestDecodeSecondaryLeaseSetRejectsMalformedDescriptors(t *testing.T) {
	for name, fields := range map[string][]uint32{
		"reversed": {4, 16}, "bit_widths": {128, 32}, "short_field": {15, 4},
		"missing_field": {16}, "extra_field": {16, 4, 4},
	} {
		t.Run(name, func(t *testing.T) {
			message := secondaryLeaseSetFixture(t)
			var concat []netlink.Attribute
			for _, width := range fields {
				concat = append(concat, netlink.Attribute{Type: 1, Data: mustMarshalKernelAttributes(t,
					[]netlink.Attribute{{Type: 1, Data: testBigEndianUint32(width)}})})
			}
			setMessageAttributeData(t, &message, 9, mustMarshalKernelAttributes(t, []netlink.Attribute{
				{Type: 2, Data: mustMarshalKernelAttributes(t, concat)},
			}))
			got, err := decodeSecondaryLeaseSet(message, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestDecodeSecondaryLeaseSetRequiresMetadataAndConsistentSequence(t *testing.T) {
	for _, attrType := range []uint16{1, 2, 3, 4, 5, 9, 13} {
		t.Run("missing_"+strconv.Itoa(int(attrType)), func(t *testing.T) {
			message := secondaryLeaseSetFixture(t)
			removeMessageAttribute(t, &message, attrType)
			got, err := decodeSecondaryLeaseSet(message, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
	t.Run("inconsistent_sequence", func(t *testing.T) {
		message := secondaryLeaseSetFixture(t)
		sequence := kernelBaseSequence{set: true, value: binary.BigEndian.Uint16(message.Data[2:4]) + 1}
		got, err := decodeSecondaryLeaseSet(message, &sequence)
		require.Error(t, err)
		require.Nil(t, got)
	})
}

// Captured from the real Linux7.0.11 QA timed-set probe. The pointer-rendered backend
// diagnostic and the live element count carry no ownership or lease authority.
func TestDecodeSecondaryLeaseSetCapturedKernelMetadata(t *testing.T) {
	data, err := hex.DecodeString(
		"07000002120001006d766d63746c5f627269646765000000130002007365636f6e646172795f697076340000" +
			"0c001000000000000000000108000300000000900800040000000a470800050000000014" +
			"0a000d000004020000000000200009001c0002000c00010008000100000000100c0001000800010000000004" +
			"1700130030786666666666666666383235303235653000000800140000000001")
	require.NoError(t, err)
	message := netlink.Message{
		Header: netlink.Header{Type: 2569, Flags: netlink.Multi, Length: uint32(16 + len(data))},
		Data:   data,
	}
	sequence := kernelBaseSequence{}
	got, err := decodeSecondaryLeaseSet(message, &sequence)
	require.NoError(t, err)
	want := &nftables.Set{
		Table: &nftables.Table{Family: nftables.TableFamilyBridge, Name: "mvmctl_bridge"},
		Name:  "secondary_ipv4", HasTimeout: true, Concatenation: true,
		KeyType:      nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr),
		KeyByteOrder: binaryutil.BigEndian,
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(nftables.SetDatatype{})); diff != "" {
		t.Errorf("captured metadata (-want +got):\n%s", diff)
	}
	require.True(t, sequence.set)
	require.Equal(t, uint16(2), sequence.value)
}
