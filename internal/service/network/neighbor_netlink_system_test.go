package network

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Rationale: The host bridge may retain the old VM's IP-to-MAC mapping. A new VM may clear only its exact
// dynamic entry, must prove absence, and must leave static/foreign neighbor authority untouched.
func TestDirectRouteNetlinkInvalidatesExactDynamicNeighbor(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("10.44.0.2")
	present := true
	connection := &fakeRouteNetlinkConnection{}
	connection.execute = func(message netlink.Message) ([]netlink.Message, error) {
		switch message.Header.Type {
		case netlink.HeaderType(unix.RTM_GETNEIGH):
			if !present {
				return nil, unix.ENOENT
			}
			return []netlink.Message{testIPv4NeighborReply(t, 20, address, unix.NUD_STALE, 0)}, nil
		case netlink.HeaderType(unix.RTM_DELNEIGH):
			present = false
			return []netlink.Message{testRouteACK()}, nil
		default:
			t.Fatalf("unexpected neighbor request type %d", message.Header.Type)
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

	require.NoError(t, adapter.invalidateIPv4Neighbor(t.Context(), 41, 20, address))
	assert.False(t, present)
	require.Len(t, connection.requests, 3)
	wantTypes := []netlink.HeaderType{
		netlink.HeaderType(unix.RTM_GETNEIGH), netlink.HeaderType(unix.RTM_DELNEIGH),
		netlink.HeaderType(unix.RTM_GETNEIGH),
	}
	gotTypes := make([]netlink.HeaderType, 0, len(connection.requests))
	for _, request := range connection.requests {
		gotTypes = append(gotTypes, request.Header.Type)
		assert.Equal(t, byte(unix.AF_INET), request.Data[0])
		assert.Equal(t, uint32(20), binary.NativeEndian.Uint32(request.Data[4:8]))
		attributes := testDecodeRouteAttributes(t, request.Data[unix.SizeofNdMsg:])
		require.Len(t, attributes, 1)
		assert.Equal(t, uint16(unix.NDA_DST), attributes[0].Type)
		if diff := cmp.Diff(address.AsSlice(), attributes[0].Data); diff != "" {
			t.Errorf("neighbor destination mismatch (-want +got):\n%s", diff)
		}
	}
	if diff := cmp.Diff(wantTypes, gotTypes); diff != "" {
		t.Errorf("neighbor request sequence mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, netlink.Request|netlink.Acknowledge, connection.requests[1].Header.Flags)
}

func TestDirectRouteNetlinkLeavesAbsentNeighborUntouched(t *testing.T) {
	t.Parallel()
	connection := &fakeRouteNetlinkConnection{execute: func(netlink.Message) ([]netlink.Message, error) {
		return nil, unix.ENOENT
	}}
	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		return connection, nil
	}}

	require.NoError(t, adapter.invalidateIPv4Neighbor(
		t.Context(), 41, 20, netip.MustParseAddr("10.44.0.2"),
	))
	require.Len(t, connection.requests, 1)
	assert.Equal(t, netlink.HeaderType(unix.RTM_GETNEIGH), connection.requests[0].Header.Type)
}

// Rationale: Kernel neighbor GC may win the race after the first lookup. Only proven absence with a lone
// ENOENT delete result is safe to accept; a joined transport failure cannot be mistaken for that race.
func TestDirectRouteNetlinkAcceptsOnlyProvenConcurrentNeighborRemoval(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("10.44.0.2")
	tests := map[string]struct {
		deleteErr error
		wantErr   bool
	}{
		"kernel collected entry": {deleteErr: unix.ENOENT},
		"delete also lost connection": {
			deleteErr: errors.Join(unix.ENOENT, errors.New("connection close failed")), wantErr: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			present := true
			connection := &fakeRouteNetlinkConnection{execute: func(message netlink.Message) ([]netlink.Message, error) {
				switch message.Header.Type {
				case netlink.HeaderType(unix.RTM_GETNEIGH):
					if present {
						return []netlink.Message{testIPv4NeighborReply(t, 20, address, unix.NUD_STALE, 0)}, nil
					}
					return nil, unix.ENOENT
				case netlink.HeaderType(unix.RTM_DELNEIGH):
					present = false
					return nil, test.deleteErr
				default:
					t.Fatalf("unexpected neighbor request type %d", message.Header.Type)
					return nil, nil
				}
			}}
			adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
				return connection, nil
			}}

			err := adapter.invalidateIPv4Neighbor(t.Context(), 41, 20, address)

			if test.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, "connection close failed")
			} else {
				require.NoError(t, err)
			}
			require.Len(t, connection.requests, 3)
			assert.Equal(t, netlink.HeaderType(unix.RTM_GETNEIGH), connection.requests[2].Header.Type)
		})
	}
}

func TestDirectRouteNetlinkRejectsForeignNeighborWithoutDeletion(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("10.44.0.2")
	tests := map[string]struct {
		ifIndex int
		ip      netip.Addr
		state   uint16
		flags   uint8
	}{
		"permanent administrator entry": {20, address, unix.NUD_PERMANENT, 0},
		"proxy entry":                   {20, address, unix.NUD_STALE, unix.NTF_PROXY},
		"different bridge":              {21, address, unix.NUD_STALE, 0},
		"different guest address":       {20, netip.MustParseAddr("10.44.0.3"), unix.NUD_STALE, 0},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			connection := &fakeRouteNetlinkConnection{execute: func(netlink.Message) ([]netlink.Message, error) {
				return []netlink.Message{testIPv4NeighborReply(
					t, test.ifIndex, test.ip, test.state, test.flags,
				)}, nil
			}}
			adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
				return connection, nil
			}}

			err := adapter.invalidateIPv4Neighbor(t.Context(), 41, 20, address)

			require.Error(t, err)
			require.Len(t, connection.requests, 1)
			assert.Equal(t, netlink.HeaderType(unix.RTM_GETNEIGH), connection.requests[0].Header.Type)
		})
	}
}

func TestDirectRouteNetlinkRejectsUnprovenNeighborDeletion(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("10.44.0.2")
	connection := &fakeRouteNetlinkConnection{execute: func(message netlink.Message) ([]netlink.Message, error) {
		if message.Header.Type == netlink.HeaderType(unix.RTM_DELNEIGH) {
			return []netlink.Message{testRouteACK()}, nil
		}
		return []netlink.Message{testIPv4NeighborReply(t, 20, address, unix.NUD_STALE, 0)}, nil
	}}
	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		return connection, nil
	}}

	err := adapter.invalidateIPv4Neighbor(t.Context(), 41, 20, address)

	assert.ErrorContains(t, err, "remains after invalidation")
	require.Len(t, connection.requests, 3)
}

func TestDirectRouteNetlinkRejectsMalformedNeighborReply(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("10.44.0.2")
	connection := &fakeRouteNetlinkConnection{execute: func(netlink.Message) ([]netlink.Message, error) {
		return []netlink.Message{{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWNEIGH)}}}, nil
	}}
	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		return connection, nil
	}}

	err := adapter.invalidateIPv4Neighbor(t.Context(), 41, 20, address)

	assert.ErrorContains(t, err, "invalid type or size")
	require.Len(t, connection.requests, 1)
}

func testIPv4NeighborReply(t *testing.T, ifIndex int, address netip.Addr, state uint16, flags uint8) netlink.Message {
	t.Helper()
	data := make([]byte, unix.SizeofNdMsg)
	data[0] = unix.AF_INET
	binary.NativeEndian.PutUint32(data[4:8], uint32(ifIndex))
	binary.NativeEndian.PutUint16(data[8:10], state)
	data[10] = flags
	data[11] = unix.RTN_UNICAST
	attributes, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: unix.NDA_DST, Data: address.AsSlice()},
		{Type: unix.NDA_LLADDR, Data: []byte{0x02, 0xfc, 0, 0, 0, 1}},
	})
	require.NoError(t, err)
	return netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWNEIGH)},
		Data:   append(data, attributes...),
	}
}
