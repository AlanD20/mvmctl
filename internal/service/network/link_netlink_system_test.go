package network

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDirectRouteNetlinkRequiresExplicitNamespaceAndExactACK(t *testing.T) {
	t.Parallel()

	connection := &fakeRouteNetlinkConnection{replies: []netlink.Message{testRouteACK()}}
	adapter := &directRouteNetlink{dial: func(family int, config *netlink.Config) (routeNetlinkConnection, error) {
		assert.Equal(t, unix.NETLINK_ROUTE, family)
		require.NotNil(t, config)
		assert.Equal(t, 41, config.NetNS)
		assert.True(t, config.Strict)
		return connection, nil
	}}
	require.NoError(t, adapter.createBridgeDown(t.Context(), 41, "mvl012345678901", "owned"))
	assert.Equal(t, 1, connection.closeCount())
	require.Len(t, connection.requests, 1)
	assert.Equal(t, netlink.HeaderType(unix.RTM_NEWLINK), connection.requests[0].Header.Type)
	assert.Equal(t,
		netlink.Request|netlink.Acknowledge|netlink.Create|netlink.Excl,
		connection.requests[0].Header.Flags,
	)

	_, err := adapter.inspectLink(t.Context(), 0, "mvl012345678901")
	assert.ErrorContains(t, err, "not explicit")
}

func TestDirectRouteNetlinkRejectsMalformedACKAndSurfacesClose(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		replies  []netlink.Message
		closeErr error
	}{
		"missing acknowledgement": {},
		"unexpected payload": {replies: []netlink.Message{{
			Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWLINK)},
		}}},
		"nonzero errno": {replies: []netlink.Message{{
			Header: netlink.Header{Type: netlink.Error}, Data: []byte{1, 0, 0, 0},
		}}},
		"close failure": {replies: []netlink.Message{testRouteACK()}, closeErr: errors.New("close failed")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			connection := &fakeRouteNetlinkConnection{replies: test.replies, closeErr: test.closeErr}
			adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
				return connection, nil
			}}
			err := adapter.createBridgeDown(t.Context(), 41, "mvl012345678901", "owned")
			assert.Error(t, err)
			assert.Equal(t, 1, connection.closeCount())
		})
	}
}

func TestDirectRouteNetlinkSetsOnlyMasterAndEmbedsExplicitVethPeerNamespace(t *testing.T) {
	t.Parallel()

	connection := &fakeRouteNetlinkConnection{replies: []netlink.Message{testRouteACK()}}
	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		return connection, nil
	}}
	require.NoError(t, adapter.setLinkMaster(t.Context(), 41, 22, 20))
	require.NoError(t, adapter.createVethPairDown(
		t.Context(), 41, 42, "mvh012345678901", "host-owned", "mvn012345678901", "peer-owned",
	))
	require.Len(t, connection.requests, 2)

	masterRequest := connection.requests[0]
	assert.Equal(t, netlink.Request|netlink.Acknowledge, masterRequest.Header.Flags)
	masterAttributes := testDecodeRouteAttributes(t, masterRequest.Data[unix.SizeofIfInfomsg:])
	require.Len(t, masterAttributes, 1)
	assert.Equal(t, uint16(unix.IFLA_MASTER), masterAttributes[0].Type&^uint16(netlink.Nested))
	assert.Equal(t, uint32(20), binary.NativeEndian.Uint32(masterAttributes[0].Data))

	vethRequest := connection.requests[1]
	assert.Equal(t,
		netlink.Request|netlink.Acknowledge|netlink.Create|netlink.Excl,
		vethRequest.Header.Flags,
	)
	outer := testDecodeRouteAttributes(t, vethRequest.Data[unix.SizeofIfInfomsg:])
	linkInfo := testFindRouteAttribute(t, outer, unix.IFLA_LINKINFO)
	info := testDecodeRouteAttributes(t, linkInfo.Data)
	data := testFindRouteAttribute(t, info, unix.IFLA_INFO_DATA)
	veth := testDecodeRouteAttributes(t, data.Data)
	peer := testFindRouteAttribute(t, veth, vethInfoPeer)
	peerAttributes := testDecodeRouteAttributes(t, peer.Data[unix.SizeofIfInfomsg:])
	namespaceFD := testFindRouteAttribute(t, peerAttributes, unix.IFLA_NET_NS_FD)
	assert.Equal(t, uint32(42), binary.NativeEndian.Uint32(namespaceFD.Data))
}

// Rationale: cancellation must close a blocked socket exactly once so the mutation caller receives an uncertain error.
func TestDirectRouteNetlinkCancellationClosesBlockedConnectionOnce(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	connection := &fakeRouteNetlinkConnection{}
	connection.execute = func(netlink.Message) ([]netlink.Message, error) {
		close(started)
		for connection.closeCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		return nil, errors.New("socket closed")
	}
	adapter := &directRouteNetlink{dial: func(int, *netlink.Config) (routeNetlinkConnection, error) {
		return connection, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- adapter.createBridgeDown(ctx, 41, "mvl012345678901", "owned")
	}()
	<-started
	cancel()
	err := <-result
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, connection.closeCount())
}

func testRouteACK() netlink.Message {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, 0)
	return netlink.Message{Header: netlink.Header{Type: netlink.Error}, Data: data}
}

func testDecodeRouteAttributes(t *testing.T, data []byte) []netlink.Attribute {
	t.Helper()
	decoder, err := netlink.NewAttributeDecoder(data)
	require.NoError(t, err)
	attributes := make([]netlink.Attribute, 0, decoder.Len())
	for decoder.Next() {
		attributes = append(attributes, netlink.Attribute{
			Type: decoder.Type() | decoder.TypeFlags(), Data: append([]byte(nil), decoder.Bytes()...),
		})
	}
	require.NoError(t, decoder.Err())
	return attributes
}

func testFindRouteAttribute(t *testing.T, attributes []netlink.Attribute, typeID uint16) netlink.Attribute {
	t.Helper()
	for _, attribute := range attributes {
		if attribute.Type&^uint16(netlink.Nested) == typeID {
			return attribute
		}
	}
	t.Fatalf("route attribute %d not found", typeID)
	return netlink.Attribute{}
}

type fakeRouteNetlinkConnection struct {
	mu sync.Mutex

	replies  []netlink.Message
	execute  func(netlink.Message) ([]netlink.Message, error)
	closeErr error
	requests []netlink.Message
	closes   int
}

func (connection *fakeRouteNetlinkConnection) Execute(message netlink.Message) ([]netlink.Message, error) {
	connection.mu.Lock()
	connection.requests = append(connection.requests, message)
	execute := connection.execute
	replies := connection.replies
	connection.mu.Unlock()
	if execute != nil {
		return execute(message)
	}
	return replies, nil
}

func (*fakeRouteNetlinkConnection) SetDeadline(time.Time) error { return nil }

func (connection *fakeRouteNetlinkConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closes++
	return connection.closeErr
}

func (connection *fakeRouteNetlinkConnection) closeCount() int {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.closes
}
