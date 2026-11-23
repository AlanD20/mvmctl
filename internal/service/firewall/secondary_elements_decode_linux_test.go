package firewall

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Raw UAPI fixture: KEY1 contains VALUE1, TIMEOUT4 and EXPIRATION5 are
// big-endian milliseconds. No producer or decoder constructs the expected key.
func secondaryElementFields(t *testing.T) []netlink.Attribute {
	t.Helper()
	key := append([]byte{'m', 'v', 'm', 'v', '-', 't', 'e', 's', 't', 0, 0, 0, 0, 0, 0, 0}, 10, 25, 0, 100)
	return []netlink.Attribute{
		{Type: 1, Data: mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1, Data: key}})},
		{Type: 4, Data: testBigEndianUint64(5000)},
		{Type: 5, Data: testBigEndianUint64(3210)},
	}
}

func secondaryElementsMessage(t *testing.T, fields ...[]netlink.Attribute) netlink.Message {
	t.Helper()
	var wrappers []netlink.Attribute
	for _, attrs := range fields {
		wrappers = append(wrappers, netlink.Attribute{Type: 1, Data: mustMarshalKernelAttributes(t, attrs)})
	}
	return testKernelMessage(t, unix.NFT_MSG_NEWSETELEM, nftables.TableFamilyBridge, netlink.Multi,
		[]netlink.Attribute{
			{Type: 1, Data: []byte("mvmctl_bridge\x00")},
			{Type: 2, Data: []byte("secondary_ipv4\x00")},
			{Type: 3, Data: mustMarshalKernelAttributes(t, wrappers)},
		})
}

func TestDecodeSecondaryLeaseElementsPreservesReadback(t *testing.T) {
	for name, remaining := range map[string]uint64{"live": 3210, "expired_at_observation": 0, "full_timeout": 5000} {
		t.Run(name, func(t *testing.T) {
			fields := secondaryElementFields(t)
			fields[2].Data = testBigEndianUint64(remaining)
			fields = append(fields, netlink.Attribute{Type: 8}) // optional u64 alignment pad
			message := secondaryElementsMessage(t, fields)
			sequence := kernelBaseSequence{}
			got, err := decodeSecondaryLeaseElements([]netlink.Message{message, secondaryElementsMessage(t)}, &sequence)
			require.NoError(t, err)
			want := []secondaryLeaseElement{{
				key:      [20]byte{'m', 'v', 'm', 'v', '-', 't', 'e', 's', 't', 0, 0, 0, 0, 0, 0, 0, 10, 25, 0, 100},
				lifetime: FloatingIPFinite, timeout: 5 * time.Second, remaining: time.Duration(remaining) * time.Millisecond,
			}}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(secondaryLeaseElement{})); diff != "" {
				t.Errorf("lease observations (-want +got):\n%s", diff)
			}
			require.Equal(t, kernelBaseSequence{set: true, value: testNFTBaseSequence}, sequence)
		})
	}
}

func TestDecodeSecondaryLeaseElementsRejectsForeignFields(t *testing.T) {
	mutations := map[string]func([]netlink.Attribute) []netlink.Attribute{
		"zero_timeout": func(a []netlink.Attribute) []netlink.Attribute { a[1].Data = testBigEndianUint64(0); return a },
		"overflow_timeout": func(a []netlink.Attribute) []netlink.Attribute {
			a[1].Data = testBigEndianUint64(uint64(math.MaxInt64/int64(time.Millisecond)) + 1)
			return a
		},
		"expiration_exceeds_timeout": func(a []netlink.Attribute) []netlink.Attribute {
			a[2].Data = testBigEndianUint64(5001)
			return a
		},
		"overflow_expiration": func(a []netlink.Attribute) []netlink.Attribute {
			a[2].Data = testBigEndianUint64(math.MaxUint64)
			return a
		},
		"short_timeout":    func(a []netlink.Attribute) []netlink.Attribute { a[1].Data = []byte{1}; return a },
		"short_expiration": func(a []netlink.Attribute) []netlink.Attribute { a[2].Data = []byte{1}; return a },
		"key_flag":         func(a []netlink.Attribute) []netlink.Attribute { a[0].Type |= netlink.Nested; return a },
		"timeout_flag":     func(a []netlink.Attribute) []netlink.Attribute { a[1].Type |= netlink.NetByteOrder; return a },
		"padding_payload": func(a []netlink.Attribute) []netlink.Attribute {
			return append(a, netlink.Attribute{Type: 8, Data: []byte{1}})
		},
	}
	for _, attr := range []uint16{2, 3, 6, 7, 9, 10, 11, 127} {
		mutations["foreign_attribute_"+strconv.Itoa(int(attr))] = func(a []netlink.Attribute) []netlink.Attribute {
			return append(a, netlink.Attribute{Type: attr})
		}
	}
	for index := range 3 {
		mutations["missing_"+strconv.Itoa(index)] = func(a []netlink.Attribute) []netlink.Attribute {
			return append(a[:index], a[index+1:]...)
		}
		mutations["duplicate_"+strconv.Itoa(index)] = func(a []netlink.Attribute) []netlink.Attribute {
			return append(a, a[index])
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			message := secondaryElementsMessage(t, mutate(secondaryElementFields(t)))
			got, err := decodeSecondaryLeaseElements([]netlink.Message{message}, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestDecodeSecondaryLeaseElementsRejectsMalformedKeys(t *testing.T) {
	for name, key := range map[string][]byte{
		"short": make([]byte, 19), "long": make([]byte, 21),
		"empty_interface":           append(make([]byte, 16), 10, 25, 0, 100),
		"unterminated_interface":    append([]byte("mvmv-abcdefghijkx"), 10, 25, 0, 100),
		"nonzero_interface_padding": {'m', 'v', 'm', 'v', '-', 'a', 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 10, 25, 0, 100},
		"public_address":            {'m', 'v', 'm', 'v', '-', 'a', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 8, 8, 8, 8},
	} {
		t.Run(name, func(t *testing.T) {
			fields := secondaryElementFields(t)
			fields[0].Data = mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1, Data: key}})
			got, err := decodeSecondaryLeaseElements(
				[]netlink.Message{secondaryElementsMessage(t, fields)},
				&kernelBaseSequence{},
			)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
	for name, attrs := range map[string][]netlink.Attribute{
		"verdict": {{Type: 2}}, "duplicate_value": {{Type: 1, Data: make([]byte, 20)}, {Type: 1}},
		"nested_value": {{Type: 1 | netlink.Nested, Data: make([]byte, 20)}},
	} {
		t.Run(name, func(t *testing.T) {
			fields := secondaryElementFields(t)
			fields[0].Data = mustMarshalKernelAttributes(t, attrs)
			got, err := decodeSecondaryLeaseElements(
				[]netlink.Message{secondaryElementsMessage(t, fields)},
				&kernelBaseSequence{},
			)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestDecodeSecondaryLeaseElementsRejectsInvalidEnvelopes(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *netlink.Message){
		"wrong_family":   func(_ *testing.T, m *netlink.Message) { m.Data[0] = byte(nftables.TableFamilyIPv4) },
		"wrong_type":     func(_ *testing.T, m *netlink.Message) { m.Header.Type++ },
		"wrong_flags":    func(_ *testing.T, m *netlink.Message) { m.Header.Flags |= netlink.Append },
		"wrong_length":   func(_ *testing.T, m *netlink.Message) { m.Header.Length-- },
		"wrong_version":  func(_ *testing.T, m *netlink.Message) { m.Data[1] = 1 },
		"wrong_table":    func(t *testing.T, m *netlink.Message) { setMessageAttributeData(t, m, 1, []byte("other\x00")) },
		"wrong_set":      func(t *testing.T, m *netlink.Message) { setMessageAttributeData(t, m, 2, []byte("other\x00")) },
		"missing_list":   func(t *testing.T, m *netlink.Message) { removeMessageAttribute(t, m, 3) },
		"duplicate_list": func(t *testing.T, m *netlink.Message) { appendMessageAttribute(t, m, 3) },
		"transaction_id": func(t *testing.T, m *netlink.Message) { appendMessageAttribute(t, m, 4) },
		"nested_list":    func(t *testing.T, m *netlink.Message) { setMessageAttributeFlags(t, m, 3, netlink.Nested) },
		"wrong_wrapper": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 3, mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 2}}))
		},
		"nested_wrapper": func(t *testing.T, m *netlink.Message) {
			setMessageAttributeData(t, m, 3, mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1 | netlink.Nested}}))
		},
		"truncated_wrapper": func(t *testing.T, m *netlink.Message) { setMessageAttributeData(t, m, 3, []byte{4}) },
		"nonzero_wrapper_padding": func(t *testing.T, m *netlink.Message) {
			raw := []byte{5, 0, 1, 0, 0, 1, 0, 0}
			binary.NativeEndian.PutUint16(raw[:2], 5)
			setMessageAttributeData(t, m, 3, raw)
		},
	} {
		t.Run(name, func(t *testing.T) {
			message := secondaryElementsMessage(t, secondaryElementFields(t))
			mutate(t, &message)
			got, err := decodeSecondaryLeaseElements([]netlink.Message{message}, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
	t.Run("inconsistent_base_sequence", func(t *testing.T) {
		message := secondaryElementsMessage(t, secondaryElementFields(t))
		got, err := decodeSecondaryLeaseElements(
			[]netlink.Message{message},
			&kernelBaseSequence{set: true, value: testNFTBaseSequence + 1},
		)
		require.Error(t, err)
		require.Nil(t, got)
	})
	for name, messages := range map[string][]netlink.Message{
		"duplicate_in_message":      {secondaryElementsMessage(t, secondaryElementFields(t), secondaryElementFields(t))},
		"duplicate_across_messages": {secondaryElementsMessage(t, secondaryElementFields(t)), secondaryElementsMessage(t, secondaryElementFields(t))},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeSecondaryLeaseElements(messages, &kernelBaseSequence{})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

// Captured from a namespace-only Linux7.0.11 QA probe, independently of this
// decoder. The second message is the kernel's empty trailing dump chunk.
func TestDecodeSecondaryLeaseElementsCapturedKernel(t *testing.T) {
	var messages []netlink.Message
	for _, raw := range []string{
		"07000002120001006d766d63746c5f627269646765000000130002007365636f6e646172795f6970763400003c000300380001001c000100180001006d766d762d74657374000000000000000a1900640c00040000000000000013880c0005000000000000001388",
		"07000002120001006d766d63746c5f627269646765000000130002007365636f6e646172795f69707634000004000300",
	} {
		data, err := hex.DecodeString(raw)
		require.NoError(t, err)
		messages = append(messages, netlink.Message{Header: netlink.Header{
			Type: 2572, Flags: netlink.Multi, Length: uint32(16 + len(data)),
		}, Data: data})
	}
	got, err := decodeSecondaryLeaseElements(messages, &kernelBaseSequence{})
	require.NoError(t, err)
	want := []secondaryLeaseElement{{
		key:      [20]byte{'m', 'v', 'm', 'v', '-', 't', 'e', 's', 't', 0, 0, 0, 0, 0, 0, 0, 10, 25, 0, 100},
		lifetime: FloatingIPFinite, timeout: 5 * time.Second, remaining: 5 * time.Second,
	}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(secondaryLeaseElement{})); diff != "" {
		t.Errorf("captured observations (-want +got):\n%s", diff)
	}
}

func TestDecodeSecondaryLeaseElementsBoundsAggregateCount(t *testing.T) {
	var messages []netlink.Message
	for start := 0; start < maxKernelObservationRules; start += 128 {
		var fields [][]netlink.Attribute
		for index := start; index < start+128; index++ {
			attrs := secondaryElementFields(t)
			key := []byte{
				'm',
				'v',
				'm',
				'v',
				'-',
				'a',
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				10,
				25,
				byte(index >> 8),
				byte(index),
			}
			attrs[0].Data = mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1, Data: key}})
			fields = append(fields, attrs)
		}
		messages = append(messages, secondaryElementsMessage(t, fields...))
	}
	got, err := decodeSecondaryLeaseElements(messages, &kernelBaseSequence{})
	require.NoError(t, err)
	require.Len(t, got, maxKernelObservationRules)
	for index, element := range got {
		want := secondaryLeaseElement{
			key: [20]byte{
				'm',
				'v',
				'm',
				'v',
				'-',
				'a',
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				0,
				10,
				25,
				byte(index >> 8),
				byte(index),
			},
			lifetime: FloatingIPFinite, timeout: 5 * time.Second,
			remaining: 3210 * time.Millisecond,
		}
		if diff := cmp.Diff(want, element, cmp.AllowUnexported(secondaryLeaseElement{})); diff != "" {
			t.Fatalf("element %d (-want +got):\n%s", index, diff)
		}
	}
	// A fresh key beyond the admitted maximum must fail rather than truncate.
	messages = append(messages, secondaryElementsMessage(t, secondaryElementFields(t)))
	got, err = decodeSecondaryLeaseElements(messages, &kernelBaseSequence{})
	require.Error(t, err)
	require.Nil(t, got)
}
