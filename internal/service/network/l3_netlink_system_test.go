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

func TestDirectRouteNetlinkListsIPv4AddressAndRouteInExplicitNamespace(t *testing.T) {
	t.Parallel()

	connection := &fakeRouteNetlinkConnection{}
	connection.execute = func(message netlink.Message) ([]netlink.Message, error) {
		switch message.Header.Type {
		case netlink.HeaderType(unix.RTM_GETADDR):
			return []netlink.Message{
				testRouteAddressMessage(t, 10, "10.44.0.1/24", unix.RT_SCOPE_UNIVERSE),
				testRouteAddressMessage(t, 11, "10.45.0.1/24", unix.RT_SCOPE_UNIVERSE),
			}, nil
		case netlink.HeaderType(unix.RTM_GETROUTE):
			return []netlink.Message{
				testRouteIPv4Message(t, testRouteMessageSpec{
					destination: "10.44.0.0/24", source: "10.44.0.1", ifIndex: 10,
					table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_KERNEL,
					scope: unix.RT_SCOPE_LINK, kind: unix.RTN_UNICAST,
				}),
				testRouteIPv4Message(t, testRouteMessageSpec{
					destination: "10.45.0.0/24", source: "10.45.0.1", ifIndex: 11,
					table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_KERNEL,
					scope: unix.RT_SCOPE_LINK, kind: unix.RTN_UNICAST,
				}),
			}, nil
		default:
			t.Fatalf("unexpected rtnetlink request type %d", message.Header.Type)
			return nil, nil
		}
	}
	adapter := &directRouteNetlink{dial: func(family int, config *netlink.Config) (routeNetlinkConnection, error) {
		assert.Equal(t, unix.NETLINK_ROUTE, family)
		require.NotNil(t, config)
		assert.Equal(t, 41, config.NetNS)
		assert.True(t, config.Strict)
		return connection, nil
	}}

	addresses, err := adapter.listIPv4Addresses(t.Context(), 41, 10)
	require.NoError(t, err)
	routes, err := adapter.listIPv4Routes(t.Context(), 41, 10)
	require.NoError(t, err)

	wantAddresses := []routeAddressObservation{{
		ifIndex: 10, prefix: netip.MustParsePrefix("10.44.0.1/24"), scope: unix.RT_SCOPE_UNIVERSE,
	}}
	if diff := cmp.Diff(wantAddresses, addresses,
		cmp.AllowUnexported(routeAddressObservation{}),
		cmpopts.EquateComparable(netip.Prefix{}),
	); diff != "" {
		t.Errorf("listed IPv4 addresses mismatch (-want +got):\n%s", diff)
	}
	wantRoutes := []routeIPv4Observation{{
		destination: netip.MustParsePrefix("10.44.0.0/24"),
		source:      netip.MustParseAddr("10.44.0.1"),
		hasSource:   true,
		ifIndex:     10,
		table:       unix.RT_TABLE_MAIN,
		protocol:    unix.RTPROT_KERNEL,
		scope:       unix.RT_SCOPE_LINK,
		kind:        unix.RTN_UNICAST,
	}}
	if diff := cmp.Diff(wantRoutes, routes,
		cmp.AllowUnexported(routeIPv4Observation{}),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("listed IPv4 routes mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, connection.requests, 2)
	assert.Equal(t, netlink.Request|netlink.Dump, connection.requests[0].Header.Flags)
	assert.Equal(t, netlink.Request|netlink.Dump, connection.requests[1].Header.Flags)
	assert.Equal(t, 2, connection.closeCount())
}

func TestDirectRouteNetlinkAddsOnlyTypedIPv4AddressAndSetsOnlyIFFUp(t *testing.T) {
	t.Parallel()

	connection := &fakeRouteNetlinkConnection{replies: []netlink.Message{testRouteACK()}}
	adapter := &directRouteNetlink{dial: func(_ int, config *netlink.Config) (routeNetlinkConnection, error) {
		assert.Equal(t, 41, config.NetNS)
		return connection, nil
	}}
	address := addressExpectation{
		prefix: netip.MustParsePrefix("10.44.0.1/24"), scope: addressScopeUniverse,
	}

	require.NoError(t, adapter.addIPv4Address(t.Context(), 41, 10, address))
	require.NoError(t, adapter.setLinkUp(t.Context(), 41, 10, true))
	require.NoError(t, adapter.setLinkUp(t.Context(), 41, 10, false))
	require.Len(t, connection.requests, 3)

	add := connection.requests[0]
	assert.Equal(t, netlink.HeaderType(unix.RTM_NEWADDR), add.Header.Type)
	assert.Equal(t, netlink.Request|netlink.Acknowledge|netlink.Create|netlink.Excl, add.Header.Flags)
	assert.Equal(t, byte(unix.AF_INET), add.Data[0])
	assert.Equal(t, byte(24), add.Data[1])
	assert.Equal(t, byte(unix.RT_SCOPE_UNIVERSE), add.Data[3])
	assert.Equal(t, uint32(10), binary.NativeEndian.Uint32(add.Data[4:8]))
	attributes := testDecodeRouteAttributes(t, add.Data[unix.SizeofIfAddrmsg:])
	require.Len(t, attributes, 2)
	wantAddressBytes := netip.MustParseAddr("10.44.0.1").AsSlice()
	if diff := cmp.Diff(wantAddressBytes, attributes[0].Data); diff != "" {
		t.Errorf("IFA_LOCAL bytes mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantAddressBytes, attributes[1].Data); diff != "" {
		t.Errorf("IFA_ADDRESS bytes mismatch (-want +got):\n%s", diff)
	}

	raise := connection.requests[1]
	assert.Equal(t, netlink.HeaderType(unix.RTM_NEWLINK), raise.Header.Type)
	assert.Equal(t, uint32(unix.IFF_UP), binary.NativeEndian.Uint32(raise.Data[8:12]))
	assert.Equal(t, uint32(unix.IFF_UP), binary.NativeEndian.Uint32(raise.Data[12:16]))
	assert.Empty(t, raise.Data[unix.SizeofIfInfomsg:])

	lower := connection.requests[2]
	assert.Zero(t, binary.NativeEndian.Uint32(lower.Data[8:12]))
	assert.Equal(t, uint32(unix.IFF_UP), binary.NativeEndian.Uint32(lower.Data[12:16]))
}

func TestDirectRouteNetlinkRejectsInvalidTypedL3Mutation(t *testing.T) {
	t.Parallel()

	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		t.Fatal("invalid typed mutation must fail before opening netlink")
		return nil, nil
	}}
	assert.Error(t, adapter.addIPv4Address(t.Context(), 41, 10, addressExpectation{}))
	assert.Error(t, adapter.setLinkUp(t.Context(), 41, 0, true))
	_, err := adapter.listIPv4Routes(t.Context(), 41, 0)
	assert.Error(t, err)
}
