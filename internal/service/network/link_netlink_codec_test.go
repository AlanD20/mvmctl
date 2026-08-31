package network

import (
	"encoding/binary"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDecodeRouteLinkMessageAcceptsOwnedBridgeVethAndTAP(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		message netlink.Message
		want    routeLinkObservation
	}{
		"bridge": {
			message: testRouteLinkMessage(t, "mvl012345678901", managedBridgeAlias("mvl012345678901"), 21,
				unix.IFF_MULTICAST, 0, 0, linkKindBridge, nil),
			want: routeLinkObservation{
				name: "mvl012345678901", alias: "mvmctl:v1:bridge:mvl012345678901", kind: linkKindBridge,
				ifIndex: 21, flags: unix.IFF_MULTICAST,
			},
		},
		"veth": {
			message: testRouteLinkMessage(t, "mvh012345678901", managedVethAlias(
				"veth-host", "mvh012345678901", "mvn012345678901",
			), 22, unix.IFF_BROADCAST, 10, 23, linkKindVeth, nil),
			want: routeLinkObservation{
				name: "mvh012345678901", alias: "mvmctl:v1:veth-host:mvh012345678901:mvn012345678901",
				kind: linkKindVeth, ifIndex: 22, flags: unix.IFF_BROADCAST, master: 10, peer: 23,
			},
		},
		"tap": {
			message: testRouteLinkMessage(t, "mvt012345678901", managedTAPAlias("mvt012345678901", 1000), 24,
				0, 20, 0, linkKindTAP, func(encoder *netlink.AttributeEncoder) {
					encoder.Uint32(iflaTunOwner, 1000)
					encoder.Uint8(iflaTunType, unix.IFF_TAP)
					encoder.Uint8(iflaTunPI, 0)
					encoder.Uint8(iflaTunPersist, 1)
				}),
			want: routeLinkObservation{
				name: "mvt012345678901", alias: "mvmctl:v1:tap:mvt012345678901:1000", kind: linkKindTAP,
				ifIndex: 24, master: 20, tunOwner: 1000, tunType: unix.IFF_TAP, tunPersist: 1,
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := decodeRouteLinkMessage(test.message)
			require.NoError(t, err)
			if diff := cmp.Diff(test.want, got, cmp.AllowUnexported(routeLinkObservation{})); diff != "" {
				t.Errorf("decoded route link mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: kernel replies cross the root trust boundary; malformed or ambiguous ownership fields must fail closed.
func TestDecodeRouteLinkMessageRejectsMalformedAndAmbiguousReplies(t *testing.T) {
	t.Parallel()

	valid := testRouteLinkMessage(t, "mvl012345678901", managedBridgeAlias("mvl012345678901"), 21,
		0, 0, 0, linkKindBridge, nil)
	tests := map[string]struct {
		message netlink.Message
		mutate  func(*netlink.Message)
	}{
		"wrong message type": {message: valid, mutate: func(message *netlink.Message) {
			message.Header.Type = netlink.HeaderType(unix.RTM_DELLINK)
		}},
		"short ifinfomsg": {message: valid, mutate: func(message *netlink.Message) {
			message.Data = message.Data[:unix.SizeofIfInfomsg-1]
		}},
		"zero ifindex": {message: valid, mutate: func(message *netlink.Message) {
			binary.NativeEndian.PutUint32(message.Data[4:8], 0)
		}},
		"truncated attribute": {message: valid, mutate: func(message *netlink.Message) {
			message.Data = append(message.Data, 1)
		}},
		"duplicate name": {message: valid, mutate: func(message *netlink.Message) {
			message.Data = appendTestRouteAttribute(t, message.Data, netlink.Attribute{
				Type: unix.IFLA_IFNAME, Data: []byte("mvl012345678901\x00"),
			})
		}},
		"unterminated alias": {message: valid, mutate: func(message *netlink.Message) {
			message.Data = testReplaceRouteAttribute(t, message.Data, unix.IFLA_IFALIAS, []byte("unterminated"))
		}},
		"embedded NUL kind": {message: valid, mutate: func(message *netlink.Message) {
			kind, err := netlink.MarshalAttributes([]netlink.Attribute{
				{Type: unix.IFLA_INFO_KIND, Data: []byte("bridge\x00extra\x00")},
			})
			require.NoError(t, err)
			message.Data = testReplaceRouteAttribute(t, message.Data, unix.IFLA_LINKINFO, kind)
		}},
		"malformed linkinfo": {message: valid, mutate: func(message *netlink.Message) {
			message.Data = testReplaceRouteAttribute(t, message.Data, unix.IFLA_LINKINFO, []byte{1})
		}},
		"tap missing persistence": {message: testRouteLinkMessage(t, "mvt012345678901",
			managedTAPAlias("mvt012345678901", 1000), 24, 0, 0, 0, linkKindTAP,
			func(encoder *netlink.AttributeEncoder) {
				encoder.Uint32(iflaTunOwner, 1000)
				encoder.Uint8(iflaTunType, unix.IFF_TAP)
				encoder.Uint8(iflaTunPI, 0)
			})},
		"tap duplicate owner": {message: testRouteLinkMessage(t, "mvt012345678901",
			managedTAPAlias("mvt012345678901", 1000), 24, 0, 0, 0, linkKindTAP,
			func(encoder *netlink.AttributeEncoder) {
				encoder.Uint32(iflaTunOwner, 1000)
				encoder.Uint32(iflaTunOwner, 1000)
				encoder.Uint8(iflaTunType, unix.IFF_TAP)
				encoder.Uint8(iflaTunPI, 0)
				encoder.Uint8(iflaTunPersist, 1)
			})},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			message := netlink.Message{Header: test.message.Header, Data: append([]byte(nil), test.message.Data...)}
			if test.mutate != nil {
				test.mutate(&message)
			}
			_, err := decodeRouteLinkMessage(message)
			assert.Error(t, err)
		})
	}
}

func TestDecodeRouteLinkMessageAcceptsForeignLinksAndKernelNestedEncoding(t *testing.T) {
	t.Parallel()

	minimal := testRouteLinkMessageWithout(t, unix.IFLA_IFALIAS, unix.IFLA_LINKINFO)
	observation, err := decodeRouteLinkMessage(minimal)
	require.NoError(t, err)
	assert.Empty(t, observation.alias)
	assert.Empty(t, observation.kind)

	kernelStyle := testRouteLinkMessage(t, "mvt012345678901", managedTAPAlias("mvt012345678901", 1000), 24,
		0, 0, 0, linkKindTAP, func(encoder *netlink.AttributeEncoder) {
			encoder.Uint32(iflaTunOwner, 1000)
			encoder.Uint8(iflaTunType, unix.IFF_TAP)
			encoder.Uint8(iflaTunPI, 0)
			encoder.Uint8(iflaTunPersist, 1)
		})
	kernelStyle.Data = testReplaceRouteAttributeType(
		t, kernelStyle.Data, unix.IFLA_LINKINFO, unix.IFLA_LINKINFO,
	)
	linkInfo := testRouteAttributeData(t, kernelStyle.Data, unix.IFLA_LINKINFO)
	linkInfo = testReplaceNestedRouteAttributeType(
		t, linkInfo, unix.IFLA_INFO_DATA, unix.IFLA_INFO_DATA,
	)
	kernelStyle.Data = testReplaceRouteAttribute(t, kernelStyle.Data, unix.IFLA_LINKINFO, linkInfo)
	_, err = decodeRouteLinkMessage(kernelStyle)
	require.NoError(t, err)
}

func testRouteLinkMessage(
	t *testing.T,
	name linkName,
	alias string,
	ifIndex int,
	flags uint32,
	master int,
	peer int,
	kind string,
	tunData func(*netlink.AttributeEncoder),
) netlink.Message {
	t.Helper()
	data, err := marshalIfInfoMessage(ifIndex, flags, 0)
	require.NoError(t, err)
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.String(unix.IFLA_IFNAME, name.String())
		encoder.String(unix.IFLA_IFALIAS, alias)
		if master != 0 {
			encoder.Uint32(unix.IFLA_MASTER, uint32(master))
		}
		if peer != 0 {
			encoder.Uint32(unix.IFLA_LINK, uint32(peer))
		}
		encoder.Nested(unix.IFLA_LINKINFO, func(nested *netlink.AttributeEncoder) error {
			nested.String(unix.IFLA_INFO_KIND, kind)
			if tunData != nil {
				nested.Nested(unix.IFLA_INFO_DATA, func(dataEncoder *netlink.AttributeEncoder) error {
					tunData(dataEncoder)
					return nil
				})
			}
			return nil
		})
		return nil
	})
	require.NoError(t, err)
	return netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWLINK)}, Data: data}
}

func testRouteLinkMessageWithout(t *testing.T, omitted ...uint16) netlink.Message {
	t.Helper()
	message := testRouteLinkMessage(t, "mvl012345678901", managedBridgeAlias("mvl012345678901"), 21,
		0, 0, 0, linkKindBridge, nil)
	decoder, err := netlink.NewAttributeDecoder(message.Data[unix.SizeofIfInfomsg:])
	require.NoError(t, err)
	attributes := make([]netlink.Attribute, 0, decoder.Len())
	for decoder.Next() {
		if !containsRouteAttributeType(omitted, decoder.Type()) {
			attributes = append(
				attributes,
				netlink.Attribute{Type: decoder.Type() | decoder.TypeFlags(), Data: decoder.Bytes()},
			)
		}
	}
	require.NoError(t, decoder.Err())
	encoded, err := netlink.MarshalAttributes(attributes)
	require.NoError(t, err)
	message.Data = append(message.Data[:unix.SizeofIfInfomsg], encoded...)
	return message
}

func containsRouteAttributeType(types []uint16, candidate uint16) bool {
	for _, typeID := range types {
		if typeID == candidate {
			return true
		}
	}
	return false
}

func testRouteAttributeData(t *testing.T, data []byte, typeID uint16) []byte {
	t.Helper()
	decoder, err := netlink.NewAttributeDecoder(data[unix.SizeofIfInfomsg:])
	require.NoError(t, err)
	for decoder.Next() {
		if decoder.Type() == typeID {
			return append([]byte(nil), decoder.Bytes()...)
		}
	}
	require.NoError(t, decoder.Err())
	t.Fatalf("route attribute %d not found", typeID)
	return nil
}

func testReplaceNestedRouteAttributeType(t *testing.T, data []byte, typeID, replacement uint16) []byte {
	t.Helper()
	decoder, err := netlink.NewAttributeDecoder(data)
	require.NoError(t, err)
	attributes := make([]netlink.Attribute, 0, decoder.Len())
	for decoder.Next() {
		attribute := netlink.Attribute{Type: decoder.Type() | decoder.TypeFlags(), Data: decoder.Bytes()}
		if decoder.Type() == typeID {
			attribute.Type = replacement
		}
		attributes = append(attributes, attribute)
	}
	require.NoError(t, decoder.Err())
	encoded, err := netlink.MarshalAttributes(attributes)
	require.NoError(t, err)
	return encoded
}

func appendTestRouteAttribute(t *testing.T, data []byte, attribute netlink.Attribute) []byte {
	t.Helper()
	encoded, err := netlink.MarshalAttributes([]netlink.Attribute{attribute})
	require.NoError(t, err)
	return append(data, encoded...)
}

func testReplaceRouteAttribute(t *testing.T, data []byte, typeID uint16, replacement []byte) []byte {
	t.Helper()
	return testRewriteRouteAttribute(t, data, typeID, func(attribute netlink.Attribute) netlink.Attribute {
		attribute.Data = replacement
		return attribute
	})
}

func testReplaceRouteAttributeType(t *testing.T, data []byte, typeID, replacement uint16) []byte {
	t.Helper()
	return testRewriteRouteAttribute(t, data, typeID, func(attribute netlink.Attribute) netlink.Attribute {
		attribute.Type = replacement
		return attribute
	})
}

func testRewriteRouteAttribute(
	t *testing.T,
	data []byte,
	typeID uint16,
	rewrite func(netlink.Attribute) netlink.Attribute,
) []byte {
	t.Helper()
	decoder, err := netlink.NewAttributeDecoder(data[unix.SizeofIfInfomsg:])
	require.NoError(t, err)
	attributes := make([]netlink.Attribute, 0, decoder.Len())
	for decoder.Next() {
		attribute := netlink.Attribute{Type: decoder.Type() | decoder.TypeFlags(), Data: decoder.Bytes()}
		if decoder.Type() == typeID {
			attribute = rewrite(attribute)
		}
		attributes = append(attributes, attribute)
	}
	require.NoError(t, decoder.Err())
	encoded, err := netlink.MarshalAttributes(attributes)
	require.NoError(t, err)
	return append(data[:unix.SizeofIfInfomsg], encoded...)
}
