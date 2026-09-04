package network

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDecodeRouteAddressMessageAcceptsCanonicalIPv4Address(t *testing.T) {
	t.Parallel()

	message := testRouteAddressMessage(t, 10, "10.44.0.1/24", unix.RT_SCOPE_UNIVERSE)
	got, err := decodeRouteAddressMessage(message)

	require.NoError(t, err)
	want := routeAddressObservation{
		ifIndex: 10,
		prefix:  netip.MustParsePrefix("10.44.0.1/24"),
		scope:   unix.RT_SCOPE_UNIVERSE,
	}
	if diff := cmp.Diff(want, got,
		cmp.AllowUnexported(routeAddressObservation{}),
		cmpopts.EquateComparable(netip.Prefix{}),
	); diff != "" {
		t.Errorf("decoded route address mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Address replies are kernel evidence at a privileged boundary. Missing, duplicate, non-IPv4, mismatched
// local/peer, or host-bit-masked values must not be normalized into an apparently canonical address.
func TestDecodeRouteAddressMessageRejectsMalformedOrAmbiguousIPv4(t *testing.T) {
	t.Parallel()

	valid := testRouteAddressMessage(t, 10, "10.44.0.1/24", unix.RT_SCOPE_UNIVERSE)
	tests := map[string]func(*netlink.Message){
		"wrong message type": func(message *netlink.Message) {
			message.Header.Type = netlink.HeaderType(unix.RTM_DELADDR)
		},
		"wrong family": func(message *netlink.Message) { message.Data[0] = unix.AF_INET6 },
		"zero ifindex": func(message *netlink.Message) {
			binary.NativeEndian.PutUint32(message.Data[4:8], 0)
		},
		"duplicate local": func(message *netlink.Message) {
			message.Data = appendTestRouteAttribute(t, message.Data, netlink.Attribute{
				Type: unix.IFA_LOCAL, Data: netip.MustParseAddr("10.44.0.1").AsSlice(),
			})
		},
		"missing local": func(message *netlink.Message) {
			message.Data = testRemoveL3RouteAttribute(t, message.Data, unix.SizeofIfAddrmsg, unix.IFA_LOCAL)
		},
		"mismatched peer": func(message *netlink.Message) {
			message.Data = testReplaceL3RouteAttribute(
				t, message.Data, unix.SizeofIfAddrmsg, unix.IFA_ADDRESS,
				netip.MustParseAddr("10.44.0.2").AsSlice(),
			)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			message := netlink.Message{Header: valid.Header, Data: append([]byte(nil), valid.Data...)}
			mutate(&message)

			_, err := decodeRouteAddressMessage(message)
			assert.Error(t, err)
		})
	}
}

func TestDecodeRouteMessageAcceptsConnectedDefaultAndLocalRoutes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		message netlink.Message
		want    routeIPv4Observation
	}{
		"connected": {
			message: testRouteIPv4Message(t, testRouteMessageSpec{
				destination: "10.44.0.0/24", source: "10.44.0.1", ifIndex: 10,
				table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_KERNEL,
				scope: unix.RT_SCOPE_LINK, kind: unix.RTN_UNICAST,
			}),
			want: routeIPv4Observation{
				destination: netip.MustParsePrefix("10.44.0.0/24"),
				source:      netip.MustParseAddr("10.44.0.1"),
				hasSource:   true,
				ifIndex:     10,
				table:       unix.RT_TABLE_MAIN,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_LINK,
				kind:        unix.RTN_UNICAST,
			},
		},
		"default": {
			message: testRouteIPv4Message(t, testRouteMessageSpec{
				destination: "0.0.0.0/0", gateway: "10.44.0.1", ifIndex: 10,
				table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_BOOT,
				scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST,
			}),
			want: routeIPv4Observation{
				destination: netip.MustParsePrefix("0.0.0.0/0"),
				gateway:     netip.MustParseAddr("10.44.0.1"),
				hasGateway:  true,
				ifIndex:     10,
				table:       unix.RT_TABLE_MAIN,
				protocol:    unix.RTPROT_BOOT,
				scope:       unix.RT_SCOPE_UNIVERSE,
				kind:        unix.RTN_UNICAST,
			},
		},
		"local": {
			message: testRouteIPv4Message(t, testRouteMessageSpec{
				destination: "127.0.0.1/32", source: "127.0.0.1", ifIndex: 1,
				table: unix.RT_TABLE_LOCAL, protocol: unix.RTPROT_KERNEL,
				scope: unix.RT_SCOPE_HOST, kind: unix.RTN_LOCAL,
			}),
			want: routeIPv4Observation{
				destination: netip.MustParsePrefix("127.0.0.1/32"),
				source:      netip.MustParseAddr("127.0.0.1"),
				hasSource:   true,
				ifIndex:     1,
				table:       unix.RT_TABLE_LOCAL,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_HOST,
				kind:        unix.RTN_LOCAL,
			},
		},
		"address-derived broadcast bookkeeping": {
			message: testRouteIPv4Message(t, testRouteMessageSpec{
				destination: "10.44.0.255/32", source: "10.44.0.1", ifIndex: 10,
				table: unix.RT_TABLE_LOCAL, protocol: unix.RTPROT_KERNEL,
				scope: unix.RT_SCOPE_LINK, kind: unix.RTN_BROADCAST,
			}),
			want: routeIPv4Observation{
				destination: netip.MustParsePrefix("10.44.0.255/32"),
				source:      netip.MustParseAddr("10.44.0.1"),
				hasSource:   true,
				ifIndex:     10,
				table:       unix.RT_TABLE_LOCAL,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_LINK,
				kind:        unix.RTN_BROADCAST,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := decodeRouteIPv4Message(test.message)

			require.NoError(t, err)
			if diff := cmp.Diff(test.want, got,
				cmp.AllowUnexported(routeIPv4Observation{}),
				cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
			); diff != "" {
				t.Errorf("decoded IPv4 route mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: Multipath, duplicate authority attributes, explicit metrics, and non-canonical destinations cannot be
// accepted as the exact metric-free single-output routes fixed by ADR-0017.
func TestDecodeRouteMessageRetainsMetricAndRejectsAmbiguousAuthority(t *testing.T) {
	t.Parallel()

	metric := testRouteIPv4Message(t, testRouteMessageSpec{
		destination: "10.44.0.0/24", source: "10.44.0.1", ifIndex: 10, metric: 7,
		table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_KERNEL,
		scope: unix.RT_SCOPE_LINK, kind: unix.RTN_UNICAST,
	})
	got, err := decodeRouteIPv4Message(metric)
	require.NoError(t, err)
	assert.True(t, got.hasMetric)
	assert.Equal(t, uint32(7), got.metric)

	valid := testRouteIPv4Message(t, testRouteMessageSpec{
		destination: "10.44.0.0/24", source: "10.44.0.1", ifIndex: 10,
		table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_KERNEL,
		scope: unix.RT_SCOPE_LINK, kind: unix.RTN_UNICAST,
	})
	tests := map[string]func(*netlink.Message){
		"wrong family": func(message *netlink.Message) { message.Data[0] = unix.AF_INET6 },
		"zero output": func(message *netlink.Message) {
			message.Data = testReplaceL3RouteAttribute(
				t, message.Data, unix.SizeofRtMsg, unix.RTA_OIF, make([]byte, 4),
			)
		},
		"duplicate output": func(message *netlink.Message) {
			data := make([]byte, 4)
			binary.NativeEndian.PutUint32(data, 10)
			message.Data = appendTestRouteAttribute(t, message.Data, netlink.Attribute{Type: unix.RTA_OIF, Data: data})
		},
		"multipath": func(message *netlink.Message) {
			message.Data = appendTestRouteAttribute(t, message.Data, netlink.Attribute{
				Type: unix.RTA_MULTIPATH, Data: []byte{1, 2, 3, 4},
			})
		},
		"nested metrics authority": func(message *netlink.Message) {
			message.Data = appendTestRouteAttribute(t, message.Data, netlink.Attribute{
				Type: unix.RTA_METRICS | netlink.Nested, Data: []byte{1, 2, 3, 4},
			})
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			message := netlink.Message{Header: valid.Header, Data: append([]byte(nil), valid.Data...)}
			mutate(&message)
			_, err := decodeRouteIPv4Message(message)
			assert.Error(t, err)
		})
	}
}

type testRouteMessageSpec struct {
	destination string
	source      string
	gateway     string
	ifIndex     int
	table       uint8
	protocol    uint8
	scope       uint8
	kind        uint8
	metric      uint32
}

func testRouteAddressMessage(t *testing.T, ifIndex int, prefix string, scope uint8) netlink.Message {
	t.Helper()
	value := netip.MustParsePrefix(prefix)
	data, err := marshalIfAddrMessage(ifIndex, value.Bits(), scope)
	require.NoError(t, err)
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Bytes(unix.IFA_LOCAL, value.Addr().AsSlice())
		encoder.Bytes(unix.IFA_ADDRESS, value.Addr().AsSlice())
		return nil
	})
	require.NoError(t, err)
	return netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWADDR)}, Data: data}
}

func testRouteIPv4Message(t *testing.T, spec testRouteMessageSpec) netlink.Message {
	t.Helper()
	destination := netip.MustParsePrefix(spec.destination)
	data, err := marshalRouteMessage(
		destination.Bits(), spec.table, spec.protocol, spec.scope, spec.kind,
	)
	require.NoError(t, err)
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		if destination.Bits() != 0 {
			encoder.Bytes(unix.RTA_DST, destination.Addr().AsSlice())
		}
		if spec.source != "" {
			encoder.Bytes(unix.RTA_PREFSRC, netip.MustParseAddr(spec.source).AsSlice())
		}
		if spec.gateway != "" {
			encoder.Bytes(unix.RTA_GATEWAY, netip.MustParseAddr(spec.gateway).AsSlice())
		}
		encoder.Uint32(unix.RTA_OIF, uint32(spec.ifIndex))
		if spec.metric != 0 {
			encoder.Uint32(unix.RTA_PRIORITY, spec.metric)
		}
		return nil
	})
	require.NoError(t, err)
	return netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWROUTE)}, Data: data}
}

func testRemoveL3RouteAttribute(t *testing.T, data []byte, offset int, typeID uint16) []byte {
	t.Helper()
	attributes := testDecodeRouteAttributes(t, data[offset:])
	filtered := make([]netlink.Attribute, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Type&^uint16(netlink.Nested) != typeID {
			filtered = append(filtered, attribute)
		}
	}
	encoded, err := netlink.MarshalAttributes(filtered)
	require.NoError(t, err)
	return append(append([]byte(nil), data[:offset]...), encoded...)
}

func testReplaceL3RouteAttribute(
	t *testing.T,
	data []byte,
	offset int,
	typeID uint16,
	value []byte,
) []byte {
	t.Helper()
	attributes := testDecodeRouteAttributes(t, data[offset:])
	for index := range attributes {
		if attributes[index].Type&^uint16(netlink.Nested) == typeID {
			attributes[index].Data = value
		}
	}
	encoded, err := netlink.MarshalAttributes(attributes)
	require.NoError(t, err)
	return append(append([]byte(nil), data[:offset]...), encoded...)
}
