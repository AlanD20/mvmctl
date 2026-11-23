package firewall

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// conntrackWireTuple models Linux UAPI, including port, ICMP, and generic-protocol tuple forms.
func conntrackWireTuple(t *testing.T, src, dst string, protocol byte) []byte {
	t.Helper()
	source := netip.MustParseAddr(src).As4()
	destination := netip.MustParseAddr(dst).As4()
	ips := mustMarshalKernelAttributes(
		t,
		[]netlink.Attribute{{Type: 1, Data: source[:]}, {Type: 2, Data: destination[:]}},
	)
	proto := []netlink.Attribute{{Type: 1, Data: []byte{protocol}}}
	switch protocol {
	case 1:
		proto = append(proto,
			netlink.Attribute{Type: 4,
				Data: []byte{0x12,
					0x34}},
			netlink.Attribute{Type: 5,
				Data: []byte{8}},
			netlink.Attribute{Type: 6,
				Data: []byte{0}})
	case 6, 17, 33, 47, 132, 136:
		proto = append(proto,
			netlink.Attribute{Type: 2,
				Data: []byte{0x12,
					0x34}},
			netlink.Attribute{Type: 3,
				Data: []byte{0,
					80}})
	}
	return mustMarshalKernelAttributes(t,
		[]netlink.Attribute{{Type: netlink.Nested | 1,
			Data: ips},
			{Type: netlink.Nested | 2,
				Data: mustMarshalKernelAttributes(t,
					proto)}})
}

func conntrackWireFlow(t *testing.T, original, reply []byte, zone, id []byte) []byte {
	t.Helper()
	attrs := []netlink.Attribute{{Type: netlink.Nested | 1,
		Data: original},
		{Type: netlink.Nested | 2,
			Data: reply},
		{Type: 12,
			Data: id}}
	if zone != nil {
		attrs = append(attrs, netlink.Attribute{Type: 18, Data: zone})
	}
	return append([]byte{unix.AF_INET, 0, 0, 0}, mustMarshalKernelAttributes(t, attrs)...)
}

func TestConntrackExactSelectorsCoverEveryIPv4TupleForm(t *testing.T) {
	for _, protocol := range []byte{1, 6, 17, 33, 47, 132, 136, 50, 51, 112, 253} {
		t.Run(string([]byte{'p', protocol}), func(t *testing.T) {
			original := conntrackWireTuple(t, "10.0.0.2", "198.51.100.2", protocol)
			reply := conntrackWireTuple(t, "198.51.100.2", "10.0.0.100", protocol)
			flow, err := decodeConntrackFlow(conntrackWireFlow(t, original, reply, []byte{0, 37}, []byte{1, 2, 3, 4}))
			require.NoError(t, err)
			assert.True(
				t,
				flow.matches(netip.MustParseAddr("10.0.0.100")),
				"reply-only NAT address must select the flow",
			)
			assert.False(t, flow.matches(netip.MustParseAddr("10.0.0.101")))
			request, err := flow.deleteRequest()
			require.NoError(t, err)
			assert.Equal(t, conntrackDelete, request.Header.Type)
			assert.Equal(t, netlink.Request|netlink.Acknowledge, request.Header.Flags)
			attrs, err := netlink.UnmarshalAttributes(request.Data[4:])
			require.NoError(t, err)
			require.Len(t, attrs, 3)
			assert.Equal(t,
				[]netlink.Attribute{{Length: uint16(4 + len(original)),
					Type: netlink.Nested | 1,
					Data: original},
					{Length: 6,
						Type: 18,
						Data: []byte{0,
							37}},
					{Length: 8,
						Type: 12,
						Data: []byte{1,
							2,
							3,
							4}}},
				attrs)
		})
	}
}

func TestConntrackRejectsIncompleteOrAmbiguousDeleteIdentity(t *testing.T) {
	tuple := conntrackWireTuple(t, "10.0.0.2", "10.0.0.100", 6)
	valid := conntrackWireFlow(t, tuple, tuple, nil, []byte{1, 2, 3, 4})
	cases := map[string][]byte{"truncated": valid[:len(valid)-1],
		"missing id": conntrackWireFlow(t,
			tuple,
			tuple,
			nil,
			nil),
		"missing original": conntrackWireFlow(t,
			nil,
			tuple,
			nil,
			[]byte{1,
				2,
				3,
				4}),
		"short zone": conntrackWireFlow(t,
			tuple,
			tuple,
			[]byte{1},
			[]byte{1,
				2,
				3,
				4}),
		"duplicate tuple": append(bytes.Clone(valid),
			mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: netlink.Nested | 1,
					Data: tuple}})...)}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) { _, err := decodeConntrackFlow(data); require.Error(t, err) })
	}
	_, err := (conntrackFlow{}).deleteRequest()
	require.Error(t, err)
	flow, err := decodeConntrackFlow(valid)
	require.NoError(t, err)
	assert.Equal(t, []byte{0, 0}, flow.zone)
}

func TestConntrackInvalidationPreservesUnrelatedAndVerifiesAbsence(t *testing.T) {
	selected := conntrackWireFlow(t,
		conntrackWireTuple(t,
			"10.0.0.2",
			"198.51.100.1",
			1),
		conntrackWireTuple(t,
			"198.51.100.1",
			"10.0.0.100",
			1),
		[]byte{0,
			7},
		[]byte{1,
			2,
			3,
			4})
	other := conntrackWireFlow(t,
		conntrackWireTuple(t,
			"10.0.0.3",
			"198.51.100.1",
			17),
		conntrackWireTuple(t,
			"198.51.100.1",
			"10.0.0.3",
			17),
		nil,
		[]byte{5,
			6,
			7,
			8})
	flow, err := decodeConntrackFlow(selected)
	require.NoError(t, err)
	request, err := flow.deleteRequest()
	require.NoError(t, err)
	for name, errno := range map[string]int32{"delete acknowledged": 0,
		"expired before delete":  -int32(unix.ENOENT),
		"replacement rejects ID": -int32(unix.ENOENT)} {
		t.Run(name, func(t *testing.T) {
			replies := []testKernelRecvmsgResult{{data: testKernelTransportDatagram(t,
				testKernelTransportMessage(conntrackNew,
					netlink.Multi,
					selected),
				testKernelTransportMessage(conntrackNew,
					netlink.Multi,
					other),
				testKernelTransportDone())},
				{data: testKernelTransportDatagram(t,
					testKernelTransportErrorReply(testKernelTransportSentRequest(request),
						errno,
						false))},
				{data: testKernelTransportDatagram(t,
					testKernelTransportMessage(conntrackNew,
						netlink.Multi,
						other),
					testKernelTransportDone())}}
			connection, recv, _ := testKernelTransportConnection(replies)
			sent := []netlink.Message{}
			connection.mutateSent = func(message netlink.Message) netlink.Message {
				sent = append(sent,
					message)
				return message
			}
			transport := newKernelObservationTransport(connection, recv)
			require.NoError(t, invalidateConntrackFlows(t.Context(), transport, netip.MustParseAddr("10.0.0.100")))
			require.Len(t, sent, 3)
			assert.Equal(t, request.Data, sent[1].Data)
			assert.Equal(t, conntrackGet, sent[0].Header.Type)
			assert.Equal(t, conntrackGet, sent[2].Header.Type)
		})
	}
}

func TestConntrackInvalidationFailsClosedOnPartialOrRacingState(t *testing.T) {
	data := conntrackWireFlow(t,
		conntrackWireTuple(t,
			"10.0.0.100",
			"10.0.0.2",
			6),
		conntrackWireTuple(t,
			"10.0.0.2",
			"10.0.0.100",
			6),
		nil,
		[]byte{1,
			2,
			3,
			4})
	flow, err := decodeConntrackFlow(data)
	require.NoError(t, err)
	request, err := flow.deleteRequest()
	require.NoError(t, err)
	first := testKernelRecvmsgResult{data: testKernelTransportDatagram(t,
		testKernelTransportMessage(conntrackNew,
			netlink.Multi,
			data),
		testKernelTransportDone())}
	for name, tail := range map[string][]testKernelRecvmsgResult{
		"unknown outcome": {{err: unix.EIO}},
		"permission failure": {{data: testKernelTransportDatagram(t,
			testKernelTransportErrorReply(testKernelTransportSentRequest(request),
				-int32(unix.EPERM),
				false))}},

		"flow remains": {{data: testKernelTransportDatagram(t,
			testKernelTransportErrorReply(testKernelTransportSentRequest(request),
				0,
				false))},
			first},

		"interrupted verification": {{data: testKernelTransportDatagram(t,
			testKernelTransportErrorReply(testKernelTransportSentRequest(request),
				0,
				false))},
			{data: testKernelTransportDatagram(t,
				testKernelTransportWithFlags(testKernelTransportDone(),
					netlink.Multi|netlink.DumpInterrupted))}},
	} {
		t.Run(name, func(t *testing.T) {
			connection, recv, _ := testKernelTransportConnection(append([]testKernelRecvmsgResult{first}, tail...))
			err := invalidateConntrackFlows(t.Context(),
				newKernelObservationTransport(connection,
					recv),
				netip.MustParseAddr("10.0.0.100"))
			require.Error(t, err)
		})
	}
}

func TestConntrackTransportRejectsForeignFramesAndReadOnlyMutation(t *testing.T) {
	request := netlink.Message{Header: netlink.Header{Type: conntrackGet,
		Flags: netlink.Request | netlink.Dump},
		Data: []byte{2,
			0,
			0,
			0}}
	for name, result := range map[string]testKernelRecvmsgResult{
		"foreign peer": {data: testKernelTransportDatagram(t, testKernelTransportDone()), sourcePID: 123},
		"foreign sequence": {data: testKernelTransportDatagram(t,
			testKernelTransportWithSequence(testKernelTransportDone(),
				99))},

		"truncated": {data: []byte{1, 2, 3}},
		"unexpected ack": {data: testKernelTransportDatagram(t,
			testKernelTransportErrorReply(testKernelTransportSentRequest(request),
				0,
				false))},

		"data after done": {data: testKernelTransportDatagram(t,
			testKernelTransportDone(),
			testKernelTransportMessage(conntrackNew,
				netlink.Multi,
				nil))},
	} {
		t.Run(name, func(t *testing.T) {
			connection, recv, _ := testKernelTransportConnection([]testKernelRecvmsgResult{result})
			_, err := conntrackExchange(t.Context(), newKernelObservationTransport(connection, recv), request)
			require.Error(t, err)
		})
	}
	connection, recv, _ := testKernelTransportConnection(nil)
	_,
		err := newKernelObservationTransport(connection,
		recv).exchange(t.Context(),
		netlink.Message{Header: netlink.Header{Type: conntrackDelete,
			Flags: netlink.Request | netlink.Acknowledge}},
		kernelObservationReplySpec{mode: kernelObservationReplySingular})
	require.Error(t, err)
	assert.Zero(t, connection.sendCalls)
}

func TestConntrackCancellationRevokesSocketAndPreservesCleanupError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	connection, _, _ := testKernelTransportConnection(nil)
	connection.closeErr = errors.New("close failed")
	entered := make(chan struct{})
	recv := func(_ int, _ []byte, _ []byte, _ int) (int, int, int, unix.Sockaddr, error) {
		close(entered)
		<-connection.closed
		return 0, 0, 0, nil, unix.EBADF
	}
	transport := newKernelObservationTransport(connection, recv)
	result := make(chan error, 1)
	go func() {
		_,
			err := conntrackExchange(ctx,
			transport,
			netlink.Message{Header: netlink.Header{Type: conntrackGet,
				Flags: netlink.Request | netlink.Dump},
				Data: []byte{2,
					0,
					0,
					0}})
		result <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, connection.closeErr)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt raw receive")
	}
	require.ErrorIs(t, transport.close(context.Background(), nil), connection.closeErr)
	assert.Equal(t, 1, connection.closeCalls)
}

// This IPv4 IGMP record was captured from the Linux 7.0.11 qualification host using a read-only AF_INET dump.
// Family filtering sets DUMP_FILTERED on flow messages; the terminal DONE remains MULTI with errno zero.
func TestConntrackIPv4FamilyFilteredDumpPreservesFlowsAndProvesAbsence(t *testing.T) {
	data, err := hex.DecodeString(
		"020000002400018014000180080001000a62e40108000200e00000160c0002800500010002000000" +
			"240002801400018008000100e0000016080002000a62e4010c000280050001000200000008000300" +
			"00000188080008000000000008000c005807b99408000b00000000010800070000000156",
	)
	require.NoError(t, err)
	for name, flags := range map[string]netlink.HeaderFlags{
		"ordinary multipart":   netlink.Multi,
		"IPv4 family filtered": netlink.Multi | netlink.DumpFiltered,
	} {
		t.Run(name, func(t *testing.T) {
			connection, recv, _ := testKernelTransportConnection([]testKernelRecvmsgResult{
				{data: testKernelTransportDatagram(t,
					testKernelTransportMessage(conntrackNew, flags, data), testKernelTransportDone())},
				{data: testKernelTransportDatagram(t, testKernelTransportDone())},
			})
			var sent []netlink.Message
			connection.mutateSent = func(message netlink.Message) netlink.Message {
				sent = append(sent, message)
				return message
			}
			transport := newKernelObservationTransport(connection, recv)
			require.NoError(t, invalidateConntrackFlows(t.Context(), transport, netip.MustParseAddr("10.98.228.100")))
			require.Len(t, sent, 2, "unrelated captured flow must survive; absence needs a second fresh dump")
			for _, request := range sent {
				require.Equal(t, conntrackGet, request.Header.Type)
				require.Equal(t, netlink.Request|netlink.Dump, request.Header.Flags)
				require.Equal(t, []byte{unix.AF_INET, 0, 0, 0}, request.Data)
			}
		})
	}
}

func TestConntrackDumpStillRefusesUnexpectedOrInterruptedFlowFlags(t *testing.T) {
	request := netlink.Message{Header: netlink.Header{Type: conntrackGet, Flags: netlink.Request | netlink.Dump},
		Data: []byte{unix.AF_INET, 0, 0, 0}}
	for name, flags := range map[string]netlink.HeaderFlags{
		"filtered without multipart": netlink.DumpFiltered,
		"unexpected ack":             netlink.Multi | netlink.Acknowledge,
		"filtered unexpected ack":    netlink.Multi | netlink.DumpFiltered | netlink.Acknowledge,
		"interrupted":                netlink.Multi | netlink.DumpInterrupted,
		"filtered interrupted":       netlink.Multi | netlink.DumpFiltered | netlink.DumpInterrupted,
	} {
		t.Run(name, func(t *testing.T) {
			connection, recv, _ := testKernelTransportConnection([]testKernelRecvmsgResult{
				{data: testKernelTransportDatagram(t,
					testKernelTransportMessage(conntrackNew, flags, nil), testKernelTransportDone())},
			})
			_, err := conntrackExchange(t.Context(), newKernelObservationTransport(connection, recv), request)
			require.Error(t, err)
		})
	}
}

func TestConntrackDumpPreservesKernelErrors(t *testing.T) {
	request := netlink.Message{Header: netlink.Header{Type: conntrackGet, Flags: netlink.Request | netlink.Dump},
		Data: []byte{unix.AF_INET, 0, 0, 0}}
	for name, errno := range map[string]unix.Errno{
		"permission denied": unix.EPERM,
		"not found":         unix.ENOENT,
		"unsupported":       unix.EOPNOTSUPP,
	} {
		t.Run(name, func(t *testing.T) {
			connection, recv, _ := testKernelTransportConnection([]testKernelRecvmsgResult{
				{data: testKernelTransportDatagram(t, testKernelTransportErrorReply(
					testKernelTransportSentRequest(request), -int32(errno), false))},
			})
			_, err := conntrackExchange(t.Context(), newKernelObservationTransport(connection, recv), request)
			require.ErrorIs(t, err, errno)
		})
	}
}
