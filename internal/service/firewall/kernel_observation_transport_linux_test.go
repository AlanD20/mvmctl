package firewall

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	testKernelTransportSequence = uint32(41)
	testKernelTransportPID      = uint32(73)
	testKernelTransportType     = netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_NEWSET)
)

// Rationale: Raw transport must preserve multipart object messages that Conn.Receive would aggregate while admitting
// only an exact kernel-originated, sequence-bound series terminated by NLMSG_DONE.
func TestKernelObservationTransportReceivesMultipartFrames(t *testing.T) {
	first := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1, 2, 3})
	second := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{4})
	connection, recvmsg, receivedSizes := testKernelTransportConnection([]testKernelRecvmsgResult{
		{data: testKernelTransportDatagram(t, first)},
		{data: testKernelTransportDatagram(t, second, testKernelTransportDone())},
	})
	transport := newKernelObservationTransport(connection, recvmsg)

	got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
		kernelObservationReplySpec{
			flags: netlink.Multi,
			mode:  kernelObservationReplyMultipart,
		})
	require.NoError(t, err)
	if diff := cmp.Diff([]netlink.Message{first, second}, got); diff != "" {
		t.Errorf("multipart transport mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		[]string{"send", "syscall-conn", "read", "recvmsg", "read", "recvmsg"},
		connection.events,
	); diff != "" {
		t.Errorf("transport event order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		[]int{kernelObservationDatagramBytes, kernelObservationDatagramBytes},
		*receivedSizes,
	); diff != "" {
		t.Errorf("receive storage sizes mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, connection.receiveBuffers, 2)
	assert.True(t, connection.receiveBuffers[0] == connection.receiveBuffers[1])
	assert.Zero(t, connection.closeCalls)
}

// Rationale: An empty dump is an authoritative absence only when an exact DONE terminates the request.
func TestKernelObservationTransportAcceptsExactEmptyMultipartReply(t *testing.T) {
	connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
		data: testKernelTransportDatagram(t, testKernelTransportDone()),
	}})
	transport := newKernelObservationTransport(connection, recvmsg)

	got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
		kernelObservationReplySpec{flags: netlink.Multi, mode: kernelObservationReplyMultipart})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, connection.closeCalls)
}

// Rationale: A singular query has one response object and no control terminator. Any second object in the same kernel
// datagram makes the request boundary ambiguous and must fail closed.
func TestKernelObservationTransportAcceptsOneSingularReply(t *testing.T) {
	want := testKernelTransportMessage(testKernelTransportType, 0, []byte{9})
	connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
		data: testKernelTransportDatagram(t, want),
	}})
	transport := newKernelObservationTransport(connection, recvmsg)

	got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplySingular),
		kernelObservationReplySpec{
			mode: kernelObservationReplySingular,
		})
	require.NoError(t, err)
	require.Len(t, got, 1)
	if diff := cmp.Diff(want, got[0]); diff != "" {
		t.Errorf("singular transport mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, testKernelTransportPID, got[0].Header.PID)
	assert.Zero(t, connection.closeCalls)
}

// Rationale: Datagram and message framing are the allocation boundary. Every malformed length, alignment, source,
// truncation marker, or cap crossing must be rejected before a reply payload is retained.
func TestKernelObservationTransportRejectsMalformedFramesAndCaps(t *testing.T) {
	valid := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1})
	tests := map[string]func(*testing.T, *kernelObservationTransport, *fakeKernelObservationConnection){
		"datagram truncated": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid), flags: unix.MSG_TRUNC,
			}}, nil)
		},
		"out of band data": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid), oobBytes: 1,
			}}, nil)
		},
		"missing source": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid), source: testKernelSourceUnset,
			}}, nil)
		},
		"foreign source PID": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid), sourcePID: 1,
			}}, nil)
		},
		"truncated header": func(_ *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg(
				[]testKernelRecvmsgResult{{data: make([]byte, netlinkHeaderBytes-1)}},
				nil,
			)
		},
		"short declared length": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			frame := testKernelTransportDatagram(t, valid)
			binary.NativeEndian.PutUint32(frame[:4], netlinkHeaderBytes-1)
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{data: frame}}, nil)
		},
		"long declared length": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			frame := testKernelTransportDatagram(t, valid)
			binary.NativeEndian.PutUint32(frame[:4], uint32(len(frame)+4))
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{data: frame}}, nil)
		},
		"truncated alignment": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid)[:netlinkHeaderBytes+1],
			}}, nil)
		},
		"nonzero alignment": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			frame := testKernelTransportDatagram(t, valid)
			frame[len(frame)-1] = 1
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{data: frame}}, nil)
		},
		"message size": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.limits.messageBytes = netlinkHeaderBytes
			transport.recvmsg = testKernelRecvmsg(
				[]testKernelRecvmsgResult{{data: testKernelTransportDatagram(t, valid)}},
				nil,
			)
		},
		"message count": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.limits.messages = 1
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid, valid),
			}}, nil)
		},
		"aggregate bytes": func(t *testing.T, transport *kernelObservationTransport, _ *fakeKernelObservationConnection) {
			transport.limits.totalBytes = int(valid.Header.Length)
			transport.recvmsg = testKernelRecvmsg([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, valid, valid),
			}}, nil)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			connection, _, _ := testKernelTransportConnection(nil)
			transport := newKernelObservationTransport(connection, nil)
			mutate(t, transport, connection)

			_, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
				kernelObservationReplySpec{
					flags: netlink.Multi,
					mode:  kernelObservationReplyMultipart,
				})
			requireKernelTransportError(t, err, nil, true)
			assert.Equal(t, 1, connection.closeCalls)
		})
	}
}

// Rationale: Sequence, kernel PID, object type, flags, terminal order, loss markers, and errno are the complete raw
// transaction identity. Normalizing any drift could attach foreign or incomplete replies to managed state.
func TestKernelObservationTransportRejectsProtocolDrift(t *testing.T) {
	data := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1})
	unexpectedACK := testKernelTransportErrorReply(testKernelTransportSentRequest(
		testKernelTransportRequest(kernelObservationReplyMultipart),
	), 0, false)
	tests := map[string][]netlink.Message{
		"wrong sequence":      {testKernelTransportWithSequence(data, testKernelTransportSequence+1)},
		"zero reply PID":      {testKernelTransportWithPID(data, 0)},
		"foreign reply PID":   {testKernelTransportWithPID(data, testKernelTransportPID+1)},
		"wrong response type": {testKernelTransportMessage(testKernelTransportType+1, netlink.Multi, nil)},
		"wrong data flags":    {testKernelTransportMessage(testKernelTransportType, 0, nil)},
		"dump interrupted": {
			testKernelTransportMessage(testKernelTransportType, netlink.Multi|netlink.DumpInterrupted, nil),
		},
		"overrun":                         {{Header: testKernelTransportHeader(netlink.Overrun, 0), Data: nil}},
		"unexpected zero acknowledgement": {unexpectedACK},
		"duplicate zero acknowledgement":  {unexpectedACK, unexpectedACK},
		"duplicate done":                  {testKernelTransportDone(), testKernelTransportDone()},
		"data after done":                 {testKernelTransportDone(), data},
		"unknown control":                 {{Header: testKernelTransportHeader(netlink.Noop, 0)}},
	}
	for name, replies := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, replies...),
			}})
			transport := newKernelObservationTransport(connection, recvmsg)

			_, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
				kernelObservationReplySpec{
					flags: netlink.Multi,
					mode:  kernelObservationReplyMultipart,
				})
			requireKernelTransportError(t, err, nil, true)
			assert.Equal(t, 1, connection.closeCalls)
		})
	}
}

// Rationale: Each reply mode has exactly one terminal grammar. DONE cannot terminate a singular query, and no data or
// terminal message may follow a completed singular or multipart exchange.
func TestKernelObservationTransportRejectsTerminalShapeDrift(t *testing.T) {
	request := testKernelTransportRequest(kernelObservationReplySingular)
	rejected := testKernelTransportErrorReply(testKernelTransportSentRequest(request), -int32(unix.ENOENT), false)
	singularData := testKernelTransportMessage(testKernelTransportType, 0, []byte{1})
	tests := map[string]struct {
		spec    kernelObservationReplySpec
		replies []netlink.Message
	}{
		"DONE for singular": {
			spec:    kernelObservationReplySpec{mode: kernelObservationReplySingular},
			replies: []netlink.Message{testKernelTransportDone()},
		},
		"DONE missing errno": {
			spec:    kernelObservationReplySpec{flags: netlink.Multi, mode: kernelObservationReplyMultipart},
			replies: []netlink.Message{testKernelTransportControl(netlink.Done, netlink.Multi, nil)},
		},
		"DONE nonzero errno": {
			spec:    kernelObservationReplySpec{flags: netlink.Multi, mode: kernelObservationReplyMultipart},
			replies: []netlink.Message{testKernelTransportControl(netlink.Done, netlink.Multi, testKernelInt32(-1))},
		},
		"extra singular data": {
			spec:    kernelObservationReplySpec{mode: kernelObservationReplySingular},
			replies: []netlink.Message{singularData, singularData},
		},
		"error before sibling data": {
			spec:    kernelObservationReplySpec{mode: kernelObservationReplySingular},
			replies: []netlink.Message{rejected, singularData},
		},
		"error before sibling error": {
			spec:    kernelObservationReplySpec{mode: kernelObservationReplySingular},
			replies: []netlink.Message{rejected, rejected},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, tc.replies...),
			}})
			transport := newKernelObservationTransport(connection, recvmsg)

			_, err := transport.exchange(t.Context(), testKernelTransportRequest(tc.spec.mode), tc.spec)
			requireKernelTransportError(t, err, nil, true)
			assert.Equal(t, 1, connection.closeCalls)
		})
	}
}

// Rationale: Limits are inclusive. A transport must accept a complete reply exactly at every configured boundary so
// the fail-closed checks do not silently reduce the documented capacity.
func TestKernelObservationTransportAcceptsExactCaps(t *testing.T) {
	data := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1})
	done := testKernelTransportDone()
	connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
		data: testKernelTransportDatagram(t, data, done),
	}})
	transport := newKernelObservationTransport(connection, recvmsg)
	transport.limits = kernelObservationTransportLimits{
		messageBytes: int(done.Header.Length),
		messages:     2,
		totalBytes:   int(data.Header.Length + done.Header.Length),
	}

	got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
		kernelObservationReplySpec{
			flags: netlink.Multi,
			mode:  kernelObservationReplyMultipart,
		})
	require.NoError(t, err)
	if diff := cmp.Diff([]netlink.Message{data}, got); diff != "" {
		t.Errorf("boundary transport mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Message and byte budgets cover the complete exchange, not one Recvmsg datagram. Splitting an oversized
// dump across datagrams must not reset either counter or retain a partial result.
func TestKernelObservationTransportEnforcesCapsAcrossDatagrams(t *testing.T) {
	data := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1})
	tests := map[string]kernelObservationTransportLimits{
		"message count": {
			messageBytes: maxKernelObservationMessageBytes,
			messages:     1,
			totalBytes:   maxKernelObservationBytes,
		},
		"aggregate bytes": {
			messageBytes: maxKernelObservationMessageBytes,
			messages:     maxKernelObservationMessages,
			totalBytes:   int(data.Header.Length),
		},
	}
	for name, limits := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{
				{data: testKernelTransportDatagram(t, data)},
				{data: testKernelTransportDatagram(t, data)},
			})
			transport := newKernelObservationTransport(connection, recvmsg)
			transport.limits = limits

			got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
				kernelObservationReplySpec{flags: netlink.Multi, mode: kernelObservationReplyMultipart})
			assert.Nil(t, got)
			requireKernelTransportError(t, err, nil, true)
			assert.Equal(t, 1, connection.closeCalls)
		})
	}
}

// Rationale: This is a read-only seam, not a generic nfnetlink passthrough. Only the seven nftables inspection
// requests are admitted, their request flags determine singular versus multipart framing, and reply type is derived.
func TestKernelObservationTransportAdmitsOnlyClosedReadRequests(t *testing.T) {
	requests := map[uint16]struct {
		responseType   uint16
		multipartFlags netlink.HeaderFlags
	}{
		unix.NFT_MSG_GETGEN:   {responseType: unix.NFT_MSG_NEWGEN, multipartFlags: netlink.Multi},
		unix.NFT_MSG_GETTABLE: {responseType: unix.NFT_MSG_NEWTABLE, multipartFlags: netlink.Multi},
		unix.NFT_MSG_GETCHAIN: {responseType: unix.NFT_MSG_NEWCHAIN, multipartFlags: netlink.Multi},
		unix.NFT_MSG_GETRULE:  {responseType: unix.NFT_MSG_NEWRULE, multipartFlags: netlink.Multi | netlink.Append},
		unix.NFT_MSG_GETSET:   {responseType: unix.NFT_MSG_NEWSET, multipartFlags: netlink.Multi},
		unix.NFT_MSG_GETOBJ:   {responseType: unix.NFT_MSG_NEWOBJ, multipartFlags: netlink.Multi | netlink.Append},
		unix.NFT_MSG_GETFLOWTABLE: {
			responseType:   unix.NFT_MSG_NEWFLOWTABLE,
			multipartFlags: netlink.Multi | netlink.Append,
		},
	}
	for requestType, contract := range requests {
		for _, mode := range []kernelObservationReplyMode{
			kernelObservationReplySingular,
			kernelObservationReplyMultipart,
		} {
			name := fmt.Sprintf("%d/%d", requestType, mode)
			t.Run(name, func(t *testing.T) {
				request := testKernelTransportRequestForType(requestType, mode)
				responseFlags := netlink.HeaderFlags(0)
				replies := []netlink.Message{
					testKernelTransportMessage(
						netlink.HeaderType(
							kernelNetfilterSubsystemType|contract.responseType,
						),
						responseFlags,
						[]byte{1},
					),
				}
				if mode == kernelObservationReplyMultipart {
					responseFlags = contract.multipartFlags
					replies[0].Header.Flags = responseFlags
					replies = append(replies, testKernelTransportDone())
				}
				connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
					data: testKernelTransportDatagram(t, replies...),
				}})
				transport := newKernelObservationTransport(connection, recvmsg)

				got, err := transport.exchange(t.Context(), request, kernelObservationReplySpec{
					flags: responseFlags,
					mode:  mode,
				})
				require.NoError(t, err)
				require.Len(t, got, 1)
				assert.Equal(
					t,
					netlink.HeaderType(kernelNetfilterSubsystemType|contract.responseType),
					got[0].Header.Type,
				)
				assert.Zero(t, connection.closeCalls)
			})
		}
	}
}

// Rationale: Only a fresh request header may cross this seam. The connection, rather than a caller, assigns length,
// sequence, and port identity so subsequent reply and embedded-error validation is bound to the actual sent request.
func TestKernelObservationTransportRejectsRequestAuthorityDrift(t *testing.T) {
	singularDump := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplyMultipart)
	multipartPlain := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplySingular)
	acknowledgement := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplySingular)
	dumpWithoutRequest := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplyMultipart)
	presetLength := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplySingular)
	presetSequence := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplySingular)
	presetPID := testKernelTransportRequestForType(unix.NFT_MSG_GETTABLE, kernelObservationReplySingular)
	singularDump.Header.Flags = netlink.Request | netlink.Dump
	multipartPlain.Header.Flags = netlink.Request
	acknowledgement.Header.Flags = netlink.Request | netlink.Acknowledge
	dumpWithoutRequest.Header.Flags = netlink.Dump
	presetLength.Header.Length = uint32(netlinkHeaderBytes + len(presetLength.Data))
	presetSequence.Header.Sequence = testKernelTransportSequence
	presetPID.Header.PID = testKernelTransportPID
	tests := map[string]netlink.Message{
		"mutation":     testKernelTransportRequestForType(unix.NFT_MSG_NEWTABLE, kernelObservationReplySingular),
		"set element":  testKernelTransportRequestForType(unix.NFT_MSG_GETSETELEM, kernelObservationReplySingular),
		"object reset": testKernelTransportRequestForType(unix.NFT_MSG_GETOBJ_RESET, kernelObservationReplySingular),
		"rule reset": testKernelTransportRequestForType(
			unix.NFT_MSG_GETRULE_RESET,
			kernelObservationReplySingular,
		),
		"singular dump":   singularDump,
		"multipart plain": multipartPlain,
		"acknowledgement": acknowledgement,
		"dump only":       dumpWithoutRequest,
		"preset length":   presetLength,
		"preset sequence": presetSequence,
		"preset PID":      presetPID,
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			mode := kernelObservationReplySingular
			if name == "multipart plain" || name == "dump only" {
				mode = kernelObservationReplyMultipart
			}
			connection, recvmsg, _ := testKernelTransportConnection(nil)
			transport := newKernelObservationTransport(connection, recvmsg)
			spec := kernelObservationReplySpec{mode: mode}
			if mode == kernelObservationReplyMultipart {
				spec.flags = netlink.Multi
			}

			_, err := transport.exchange(t.Context(), request, spec)
			requireKernelTransportError(t, err, nil, false)
			assert.Zero(t, connection.sendCalls)
			assert.Zero(t, connection.closeCalls)
		})
	}
}

// Rationale: Response framing is closed alongside the request grammar. Extra response flags cannot become an implicit
// extension point that permits transport behavior outside exact singular and multipart observation replies.
func TestKernelObservationTransportRejectsReplyContractDrift(t *testing.T) {
	tests := map[string]struct {
		request netlink.Message
		spec    kernelObservationReplySpec
	}{
		"unknown mode": {
			request: testKernelTransportRequest(kernelObservationReplySingular),
			spec:    kernelObservationReplySpec{},
		},
		"singular flags": {
			request: testKernelTransportRequest(kernelObservationReplySingular),
			spec: kernelObservationReplySpec{
				flags: netlink.Multi,
				mode:  kernelObservationReplySingular,
			},
		},
		"multipart flags missing": {
			request: testKernelTransportRequest(kernelObservationReplyMultipart),
			spec: kernelObservationReplySpec{
				mode: kernelObservationReplyMultipart,
			},
		},
		"multipart flags extended": {
			request: testKernelTransportRequest(kernelObservationReplyMultipart),
			spec: kernelObservationReplySpec{
				flags: netlink.Multi | netlink.DumpFiltered,
				mode:  kernelObservationReplyMultipart,
			},
		},
		"rule flags omit append": {
			request: testKernelTransportRequestForType(unix.NFT_MSG_GETRULE, kernelObservationReplyMultipart),
			spec: kernelObservationReplySpec{
				flags: netlink.Multi,
				mode:  kernelObservationReplyMultipart,
			},
		},
		"set flags add append": {
			request: testKernelTransportRequestForType(unix.NFT_MSG_GETSET, kernelObservationReplyMultipart),
			spec: kernelObservationReplySpec{
				flags: netlink.Multi | netlink.Append,
				mode:  kernelObservationReplyMultipart,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection(nil)
			transport := newKernelObservationTransport(connection, recvmsg)

			_, err := transport.exchange(t.Context(), tc.request, tc.spec)
			requireKernelTransportError(t, err, nil, false)
			assert.Zero(t, connection.sendCalls)
			assert.Zero(t, connection.closeCalls)
		})
	}
}

// Rationale: Linux NLMSG_ERROR contains errno plus the original request header and, unless CAPPED, its payload.
// Strict sockets can append ACK_TLVS; a valid extended acknowledgement must preserve the exact errno, while malformed
// extensions must never turn a reply into trusted absence. Zero ACK is never expected.
func TestKernelObservationTransportValidatesLinuxErrorEnvelope(t *testing.T) {
	request := testKernelTransportRequest(kernelObservationReplySingular)
	request.Data = append(request.Data, 1)
	sent := testKernelTransportSentRequest(request)
	valid := testKernelTransportErrorReply(sent, -int32(unix.EPERM), false)
	// Linux netlink.h defines NLMSGERR_ATTR_OFFS as type 2 with a native-endian u32 offset.
	extAckOffset := make([]byte, 8)
	binary.NativeEndian.PutUint16(extAckOffset[0:2], 8)
	binary.NativeEndian.PutUint16(extAckOffset[2:4], 2)
	binary.NativeEndian.PutUint32(extAckOffset[4:8], 20)
	// NLMSGERR_ATTR_MSG may have a non-word-sized payload and zero alignment padding.
	extAckMessage := make([]byte, 8)
	binary.NativeEndian.PutUint16(extAckMessage[0:2], 5)
	binary.NativeEndian.PutUint16(extAckMessage[2:4], 1)
	extAckMessage[4] = 'x'
	extAckMessageBadPadding := append([]byte(nil), extAckMessage...)
	extAckMessageBadPadding[5] = 1
	tests := map[string]struct {
		message netlink.Message
		cause   error
	}{
		"non-capped kernel error": {message: valid, cause: unix.EPERM},
		"capped kernel error": {
			message: testKernelTransportErrorReply(sent, -int32(unix.EBUSY), true),
			cause:   unix.EBUSY,
		},
		"maximum errno": {
			message: testKernelTransportErrorReply(sent, -kernelObservationMaxErrno, false),
			cause:   syscall.Errno(kernelObservationMaxErrno),
		},
		"zero acknowledgement": {message: testKernelTransportErrorReply(sent, 0, false)},
		"short errno":          {message: testKernelTransportControl(netlink.Error, 0, []byte{0, 0, 0})},
		"short embedded header": {
			message: testKernelTransportControl(netlink.Error, 0, make([]byte, 4+netlinkHeaderBytes-1)),
		},
		"positive errno": {message: testKernelTransportErrorReply(sent, 1, false)},
		"errno out of range": {
			message: testKernelTransportErrorReply(sent, -4096, false),
		},
		"uncapped extended acknowledgement": {
			message: testKernelTransportWithFlags(
				testKernelTransportErrorReplyWithExtra(sent, -int32(unix.ENOENT), false, extAckOffset),
				netlink.AcknowledgeTLVs,
			),
			cause: unix.ENOENT,
		},
		"capped extended acknowledgement": {
			message: testKernelTransportWithFlags(
				testKernelTransportErrorReplyWithExtra(sent, -int32(unix.EBUSY), true, extAckOffset),
				netlink.Capped|netlink.AcknowledgeTLVs,
			),
			cause: unix.EBUSY,
		},
		"extended acknowledgement with aligned message": {
			message: testKernelTransportWithFlags(
				testKernelTransportErrorReplyWithExtra(sent, -int32(unix.ENOENT), false, extAckMessage),
				netlink.AcknowledgeTLVs,
			),
			cause: unix.ENOENT,
		},
		"extended acknowledgement with nonzero padding": {
			message: testKernelTransportWithFlags(
				testKernelTransportErrorReplyWithExtra(sent, -int32(unix.EPERM), false, extAckMessageBadPadding),
				netlink.AcknowledgeTLVs,
			),
		},
		"extended acknowledgement without attributes": {
			message: testKernelTransportWithFlags(valid, netlink.AcknowledgeTLVs),
		},
		"extended acknowledgement with short attribute": {
			message: testKernelTransportWithFlags(
				testKernelTransportErrorReplyWithExtra(sent, -int32(unix.EPERM), false, []byte{3, 0, 2, 0}),
				netlink.AcknowledgeTLVs,
			),
		},
		"extended acknowledgement with unflagged attribute": {
			message: testKernelTransportErrorReplyWithExtra(sent, -int32(unix.EPERM), false, extAckOffset),
		},
		"capped payload retained": {
			message: testKernelTransportErrorReplyWithExtra(sent, -int32(unix.EPERM), true, []byte{1}),
		},
		"non-capped payload missing": {
			message: testKernelTransportErrorReplyWithTruncatedPayload(sent, -int32(unix.EPERM)),
		},
		"embedded length": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				binary.NativeEndian.PutUint32(header[0:4], sent.Header.Length+4)
			}),
		},
		"embedded type": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				binary.NativeEndian.PutUint16(header[4:6], uint16(sent.Header.Type+1))
			}),
		},
		"embedded flags": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				binary.NativeEndian.PutUint16(header[6:8], uint16(sent.Header.Flags|netlink.Acknowledge))
			}),
		},
		"embedded sequence": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				binary.NativeEndian.PutUint32(header[8:12], sent.Header.Sequence+1)
			}),
		},
		"embedded PID": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				binary.NativeEndian.PutUint32(header[12:16], sent.Header.PID+1)
			}),
		},
		"embedded payload": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				header[netlinkHeaderBytes]++
			}),
		},
		"embedded alignment": {
			message: testKernelTransportMutateEmbeddedError(valid, func(header []byte) {
				header[len(header)-1]++
			}),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
				data: testKernelTransportDatagram(t, tc.message),
			}})
			transport := newKernelObservationTransport(connection, recvmsg)

			_, err := transport.exchange(t.Context(), request, kernelObservationReplySpec{
				mode: kernelObservationReplySingular,
			})
			requireKernelTransportError(t, err, tc.cause, true)
			assert.Equal(t, 1, connection.closeCalls)
		})
	}
}

// Rationale: Send and descriptor acquisition failures have distinct outcome evidence. No reply is authoritative after
// Send succeeds unless every populated request field is exact, and every later failure must poison the connection.
func TestKernelObservationTransportRejectsSendAndDescriptorFailures(t *testing.T) {
	sendErr := errors.New("send failed")
	rawErr := errors.New("raw connection failed")
	tests := map[string]struct {
		configure func(*fakeKernelObservationConnection)
		cause     error
		unknown   bool
	}{
		"send": {
			configure: func(connection *fakeKernelObservationConnection) { connection.sendErr = sendErr },
			cause:     sendErr,
		},
		"zero sent sequence": {
			configure: func(connection *fakeKernelObservationConnection) { connection.sentSequence = 0 },
			unknown:   true,
		},
		"zero sent PID": {
			configure: func(connection *fakeKernelObservationConnection) {
				connection.mutateSent = func(message netlink.Message) netlink.Message {
					message.Header.PID = 0
					return message
				}
			},
			unknown: true,
		},
		"sent type drift": {
			configure: func(connection *fakeKernelObservationConnection) {
				connection.mutateSent = func(message netlink.Message) netlink.Message {
					message.Header.Type++
					return message
				}
			},
			unknown: true,
		},
		"sent flags drift": {
			configure: func(connection *fakeKernelObservationConnection) {
				connection.mutateSent = func(message netlink.Message) netlink.Message {
					message.Header.Flags |= netlink.Acknowledge
					return message
				}
			},
			unknown: true,
		},
		"sent length drift": {
			configure: func(connection *fakeKernelObservationConnection) {
				connection.mutateSent = func(message netlink.Message) netlink.Message {
					message.Header.Length += 4
					return message
				}
			},
			unknown: true,
		},
		"sent payload drift": {
			configure: func(connection *fakeKernelObservationConnection) {
				connection.mutateSent = func(message netlink.Message) netlink.Message {
					message.Data = []byte{byte(unix.NFPROTO_BRIDGE), 0, 0, 0}
					return message
				}
			},
			unknown: true,
		},
		"syscall connection": {
			configure: func(connection *fakeKernelObservationConnection) { connection.syscallErr = rawErr },
			cause:     rawErr, unknown: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			connection, recvmsg, _ := testKernelTransportConnection(nil)
			tc.configure(connection)
			transport := newKernelObservationTransport(connection, recvmsg)

			_, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplySingular),
				kernelObservationReplySpec{
					mode: kernelObservationReplySingular,
				})
			requireKernelTransportError(t, err, tc.cause, tc.unknown)
			if tc.unknown {
				assert.Equal(t, 1, connection.closeCalls)
			} else {
				assert.Zero(t, connection.closeCalls)
			}
		})
	}
}

// Rationale: Every post-Send failure poisons the connection. A close failure is cleanup evidence joined to the primary
// DomainError without replacing the observation failure or its unknown-outcome marker.
func TestKernelObservationTransportPostSendFailureJoinsCloseError(t *testing.T) {
	rawErr := errors.New("raw connection failed")
	closeErr := errors.New("close failed")
	connection, recvmsg, _ := testKernelTransportConnection(nil)
	connection.syscallErr = rawErr
	connection.closeErr = closeErr
	transport := newKernelObservationTransport(connection, recvmsg)

	_, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplySingular),
		kernelObservationReplySpec{mode: kernelObservationReplySingular})
	requireKernelTransportError(t, err, rawErr, true)
	assert.ErrorIs(t, err, closeErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Missing DONE is indistinguishable from an interrupted dump. A receive failure after retained-looking data
// must discard it and retain the underlying cause plus unknown-outcome evidence.
func TestKernelObservationTransportRejectsMissingDone(t *testing.T) {
	receiveErr := io.ErrUnexpectedEOF
	data := testKernelTransportMessage(testKernelTransportType, netlink.Multi, []byte{1})
	connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{
		{data: testKernelTransportDatagram(t, data)},
		{err: receiveErr},
	})
	transport := newKernelObservationTransport(connection, recvmsg)

	got, err := transport.exchange(t.Context(), testKernelTransportRequest(kernelObservationReplyMultipart),
		kernelObservationReplySpec{
			flags: netlink.Multi,
			mode:  kernelObservationReplyMultipart,
		})
	assert.Nil(t, got)
	requireKernelTransportError(t, err, receiveErr, true)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Cancellation and deadline expiry revoke receive authority by closing the connection. A cancellation that
// races with a reply must never be converted into success, and a post-Send cancellation has unknown outcome evidence.
func TestKernelObservationTransportCancellationClosesConnection(t *testing.T) {
	t.Run("before send", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		connection, recvmsg, _ := testKernelTransportConnection(nil)
		transport := newKernelObservationTransport(connection, recvmsg)

		_, err := transport.exchange(ctx, testKernelTransportRequest(kernelObservationReplySingular),
			kernelObservationReplySpec{
				mode: kernelObservationReplySingular,
			})
		requireKernelTransportError(t, err, context.Canceled, false)
		assert.Zero(t, connection.sendCalls)
		assert.Equal(t, 1, connection.closeCalls)
	})

	t.Run("during receive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		connection, _, _ := testKernelTransportConnection(nil)
		closeErr := errors.New("close failed")
		connection.closeErr = closeErr
		entered := make(chan struct{})
		var once sync.Once
		recvmsg := func(_ int, _ []byte, _ []byte, _ int) (int, int, int, unix.Sockaddr, error) {
			once.Do(func() { close(entered) })
			<-connection.closed
			return 0, 0, 0, nil, unix.EBADF
		}
		transport := newKernelObservationTransport(connection, recvmsg)
		result := make(chan error, 1)
		go func() {
			_, err := transport.exchange(ctx, testKernelTransportRequest(kernelObservationReplySingular),
				kernelObservationReplySpec{
					mode: kernelObservationReplySingular,
				})
			result <- err
		}()
		<-entered
		cancel()

		err := <-result
		requireKernelTransportError(t, err, context.Canceled, true)
		assert.ErrorIs(t, err, closeErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
		assert.Equal(t, 1, connection.closeCalls)
	})

	t.Run("expired deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		connection, recvmsg, _ := testKernelTransportConnection(nil)
		transport := newKernelObservationTransport(connection, recvmsg)

		_, err := transport.exchange(ctx, testKernelTransportRequest(kernelObservationReplySingular),
			kernelObservationReplySpec{
				mode: kernelObservationReplySingular,
			})
		requireKernelTransportError(t, err, context.DeadlineExceeded, false)
		assert.Equal(t, 1, connection.closeCalls)
	})
}

// Rationale: Close is a checked authority release. Its failure must join the primary cause without replacing its
// identity or unknown-outcome evidence, and repeated cleanup must not close the descriptor twice.
func TestKernelObservationTransportCheckedClosePreservesPrimary(t *testing.T) {
	primaryCause := errors.New("receive failed")
	closeCause := errors.New("close failed")
	connection, recvmsg, _ := testKernelTransportConnection(nil)
	connection.closeErr = closeCause
	transport := newKernelObservationTransport(connection, recvmsg)
	primary := kernelObservationTransportError("receive kernel observation", primaryCause, true)

	err := transport.close(t.Context(), primary)
	requireKernelTransportError(t, err, primaryCause, true)
	assert.ErrorIs(t, err, closeCause)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
	assert.Contains(t, domainErr.Details[generationCleanupDiagnosticDetailKey], "close kernel observation connection")

	err = transport.close(t.Context(), err)
	assert.ErrorIs(t, err, primaryCause)
	assert.ErrorIs(t, err, closeCause)
	assert.Equal(t, 1, connection.closeCalls)
}

type testKernelRecvmsgResult struct {
	data      []byte
	oobBytes  int
	flags     int
	source    int
	sourcePID uint32
	err       error
}

const testKernelSourceUnset = 1

type fakeKernelObservationConnection struct {
	// THREAD-SAFE: mu guards every ordinary field mutated by the cancellation goroutine or raw receive callback;
	// closeOnce separately guards closing the synchronization channel. Tests inspect fields only after exchange returns
	// or after their explicit synchronization channel fires.
	mu             sync.Mutex
	events         []string
	receiveBuffers []*byte
	raw            syscall.RawConn
	mutateSent     func(netlink.Message) netlink.Message
	sentSequence   uint32
	sendErr        error
	syscallErr     error
	closeErr       error
	closed         chan struct{}
	closeOnce      sync.Once
	sendCalls      int
	closeCalls     int
}

func (connection *fakeKernelObservationConnection) Send(message netlink.Message) (netlink.Message, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.events = append(connection.events, "send")
	connection.sendCalls++
	if connection.sendErr != nil {
		return netlink.Message{}, connection.sendErr
	}
	message.Header.Length = uint32((netlinkHeaderBytes + len(message.Data) + 3) &^ 3)
	message.Header.Sequence = connection.sentSequence
	message.Header.PID = testKernelTransportPID
	if connection.mutateSent != nil {
		message = connection.mutateSent(message)
	}
	return message, nil
}

func (connection *fakeKernelObservationConnection) SyscallConn() (syscall.RawConn, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.events = append(connection.events, "syscall-conn")
	return connection.raw, connection.syscallErr
}

func (connection *fakeKernelObservationConnection) Close() error {
	connection.mu.Lock()
	connection.events = append(connection.events, "close")
	connection.closeCalls++
	connection.mu.Unlock()
	connection.closeOnce.Do(func() { close(connection.closed) })
	return connection.closeErr
}

type fakeKernelRawConn struct {
	connection *fakeKernelObservationConnection
}

func (raw *fakeKernelRawConn) Control(callback func(uintptr)) error {
	callback(1)
	return nil
}

func (raw *fakeKernelRawConn) Read(callback func(uintptr) bool) error {
	raw.connection.mu.Lock()
	raw.connection.events = append(raw.connection.events, "read")
	raw.connection.mu.Unlock()
	if !callback(1) {
		return errors.New("test raw read callback did not complete")
	}
	return nil
}

func (raw *fakeKernelRawConn) Write(callback func(uintptr) bool) error {
	if !callback(1) {
		return errors.New("test raw write callback did not complete")
	}
	return nil
}

func testKernelTransportConnection(
	results []testKernelRecvmsgResult,
) (*fakeKernelObservationConnection, kernelObservationRecvmsg, *[]int) {
	connection := &fakeKernelObservationConnection{
		sentSequence: testKernelTransportSequence,
		closed:       make(chan struct{}),
	}
	connection.raw = &fakeKernelRawConn{connection: connection}
	sizes := []int{}
	receive := testKernelRecvmsg(results, &sizes)
	return connection, func(
		fd int,
		payload []byte,
		outOfBand []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		connection.mu.Lock()
		connection.events = append(connection.events, "recvmsg")
		if len(payload) != 0 {
			connection.receiveBuffers = append(connection.receiveBuffers, &payload[0])
		}
		connection.mu.Unlock()
		return receive(fd, payload, outOfBand, flags)
	}, &sizes
}

func testKernelRecvmsg(results []testKernelRecvmsgResult, sizes *[]int) kernelObservationRecvmsg {
	index := 0
	return func(_ int, payload []byte, _ []byte, _ int) (int, int, int, unix.Sockaddr, error) {
		if sizes != nil {
			*sizes = append(*sizes, len(payload))
		}
		if index >= len(results) {
			return 0, 0, 0, nil, io.EOF
		}
		result := results[index]
		index++
		copy(payload, result.data)
		if result.err != nil {
			return 0, 0, 0, nil, result.err
		}
		if result.source == testKernelSourceUnset {
			return len(result.data), result.oobBytes, result.flags, nil, nil
		}
		return len(result.data), result.oobBytes, result.flags, &unix.SockaddrNetlink{
			Family: unix.AF_NETLINK,
			Pid:    result.sourcePID,
		}, nil
	}
}

func testKernelTransportMessage(
	typeID netlink.HeaderType,
	flags netlink.HeaderFlags,
	data []byte,
) netlink.Message {
	return netlink.Message{
		Header: func() netlink.Header {
			header := testKernelTransportHeader(typeID, flags)
			header.Length = uint32(netlinkHeaderBytes + len(data))
			return header
		}(),
		Data: slices.Clone(data),
	}
}

func testKernelTransportControl(
	typeID netlink.HeaderType,
	flags netlink.HeaderFlags,
	data []byte,
) netlink.Message {
	return testKernelTransportMessage(typeID, flags, data)
}

func testKernelTransportDone() netlink.Message {
	return testKernelTransportControl(netlink.Done, netlink.Multi, testKernelInt32(0))
}

func testKernelTransportHeader(typeID netlink.HeaderType, flags netlink.HeaderFlags) netlink.Header {
	return netlink.Header{
		Type:     typeID,
		Flags:    flags,
		Sequence: testKernelTransportSequence,
		PID:      testKernelTransportPID,
	}
}

func testKernelTransportWithSequence(message netlink.Message, sequence uint32) netlink.Message {
	message.Header.Sequence = sequence
	return message
}

func testKernelTransportWithPID(message netlink.Message, pid uint32) netlink.Message {
	message.Header.PID = pid
	return message
}

func testKernelTransportWithFlags(message netlink.Message, flags netlink.HeaderFlags) netlink.Message {
	message.Header.Flags = flags
	return message
}

func testKernelTransportRequest(mode kernelObservationReplyMode) netlink.Message {
	return testKernelTransportRequestForType(unix.NFT_MSG_GETSET, mode)
}

func testKernelTransportRequestForType(requestType uint16, mode kernelObservationReplyMode) netlink.Message {
	flags := netlink.Request
	if mode == kernelObservationReplyMultipart {
		flags |= netlink.Dump
	}
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(kernelNetfilterSubsystemType | requestType),
			Flags: flags,
		},
		Data: []byte{byte(unix.NFPROTO_INET), 0, 0, 0},
	}
}

func testKernelTransportSentRequest(request netlink.Message) netlink.Message {
	request.Header.Length = uint32((netlinkHeaderBytes + len(request.Data) + 3) &^ 3)
	request.Header.Sequence = testKernelTransportSequence
	request.Header.PID = testKernelTransportPID
	request.Data = slices.Clone(request.Data)
	return request
}

// testKernelTransportErrorReply independently encodes Linux struct nlmsgerr: native-endian errno, the exact original
// nlmsghdr, and the original payload only when NLM_F_CAPPED is absent.
func testKernelTransportErrorReply(request netlink.Message, errno int32, capped bool) netlink.Message {
	data := append(testKernelInt32(errno), testKernelTransportRequestBytes(request)...)
	flags := netlink.HeaderFlags(0)
	if capped {
		flags = netlink.Capped
	} else {
		data = append(data, request.Data...)
		payloadBytes := int(request.Header.Length) - netlinkHeaderBytes
		data = append(data, make([]byte, payloadBytes-len(request.Data))...)
	}
	return testKernelTransportControl(netlink.Error, flags, data)
}

func testKernelTransportErrorReplyWithExtra(
	request netlink.Message,
	errno int32,
	capped bool,
	extra []byte,
) netlink.Message {
	message := testKernelTransportErrorReply(request, errno, capped)
	message.Data = append(message.Data, extra...)
	message.Header.Length = uint32(netlinkHeaderBytes + len(message.Data))
	return message
}

func testKernelTransportErrorReplyWithTruncatedPayload(request netlink.Message, errno int32) netlink.Message {
	message := testKernelTransportErrorReply(request, errno, false)
	message.Data = message.Data[:len(message.Data)-1]
	message.Header.Length--
	return message
}

func testKernelTransportMutateEmbeddedError(
	message netlink.Message,
	mutate func(headerAndPayload []byte),
) netlink.Message {
	message.Data = slices.Clone(message.Data)
	mutate(message.Data[4:])
	return message
}

func testKernelTransportRequestBytes(request netlink.Message) []byte {
	result := make([]byte, netlinkHeaderBytes)
	binary.NativeEndian.PutUint32(result[0:4], request.Header.Length)
	binary.NativeEndian.PutUint16(result[4:6], uint16(request.Header.Type))
	binary.NativeEndian.PutUint16(result[6:8], uint16(request.Header.Flags))
	binary.NativeEndian.PutUint32(result[8:12], request.Header.Sequence)
	binary.NativeEndian.PutUint32(result[12:16], request.Header.PID)
	return result
}

func testKernelTransportDatagram(t *testing.T, messages ...netlink.Message) []byte {
	t.Helper()
	var result []byte
	for _, message := range messages {
		length := netlinkHeaderBytes + len(message.Data)
		aligned := (length + 3) &^ 3
		frame := make([]byte, aligned)
		binary.NativeEndian.PutUint32(frame[0:4], uint32(length))
		binary.NativeEndian.PutUint16(frame[4:6], uint16(message.Header.Type))
		binary.NativeEndian.PutUint16(frame[6:8], uint16(message.Header.Flags))
		binary.NativeEndian.PutUint32(frame[8:12], message.Header.Sequence)
		binary.NativeEndian.PutUint32(frame[12:16], message.Header.PID)
		copy(frame[16:length], message.Data)
		message.Header.Length = uint32(length)
		result = append(result, frame...)
	}
	return result
}

func testKernelInt32(value int32) []byte {
	result := make([]byte, 4)
	binary.NativeEndian.PutUint32(result, uint32(value))
	return result
}

func requireKernelTransportError(t *testing.T, err error, cause error, unknown bool) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkFirewallFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Empty(t, domainErr.Entity)
	if cause == nil {
		assert.NoError(t, domainErr.Err)
	} else {
		assert.ErrorIs(t, err, cause)
	}
	if unknown {
		assert.Equal(t, true, domainErr.Details[kernelObservationOutcomeUnknownDetailKey])
	} else {
		assert.NotContains(t, domainErr.Details, kernelObservationOutcomeUnknownDetailKey)
	}
}
