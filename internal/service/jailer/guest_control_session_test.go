package jailer

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/pkg/errs"
)

// Real TLS and stream fixtures exercise transport ownership only; no fake VMM/process admission
// is manufactured. A successful protected Firecracker/root-backing bootstrap remains a live gate.
func guestControlSessionFixture(t *testing.T) (*GuestControlSession, *tls.Conn) {
	return guestControlSessionConnFixture(t, nil)
}

func guestControlSessionConnFixture(t *testing.T, wrap func(net.Conn) net.Conn) (*GuestControlSession, *tls.Conn) {
	t.Helper()
	hostMaterial, agentMaterial, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	hostRaw, agentRaw := net.Pipe()
	if wrap != nil {
		hostRaw = wrap(hostRaw)
	}
	t.Cleanup(func() { _ = hostRaw.Close(); _ = agentRaw.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	type result struct {
		connection *tls.Conn
		err        error
	}
	ready := make(chan result, 1)
	go func() {
		conn, err := guestcontrolauth.AuthenticateHost(ctx, agentRaw, agentMaterial)
		ready <- result{conn, err}
	}()
	host, err := guestcontrolauth.AuthenticateAgent(ctx, hostRaw, hostMaterial)
	require.NoError(t, err)
	agent := <-ready
	require.NoError(t, agent.err)
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	require.NoError(t, err)
	session := &GuestControlSession{state: &guestControlSessionState{conn: host, pidfd: pidfd,
		principal: GuestControlPrincipal{OwnerUID: 1000, OwnerGID: 1000, VMID: strings.Repeat("a", 32),
			BootID: "11111111-1111-4111-8111-111111111111", LaunchGeneration: 7, ExecutionUID: 1001}}}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, agent.connection
}

func guestControlSessionRequest() guestcontrolwire.Request {
	return guestcontrolwire.Request{RequestID: "11111111-1111-4111-8111-111111111111",
		Operation: guestcontrolwire.Inspect,
		Input:     guestcontrolwire.FloatingIPInput{ResourceID: strings.Repeat("b", 32)}}
}

func TestGuestControlConnectUsesFixedPortWithoutConsumingTLSBytes(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var request [14]byte
		_, err := io.ReadFull(right, request[:])
		if err == nil && string(request[:]) != "CONNECT 10790\n" {
			err = errors.New("wrong CONNECT port")
		}
		if err == nil {
			_, err = io.WriteString(right, "OK 49152\nTLS")
		}
		done <- err
	}()
	require.NoError(t, connectGuestControl(ctx, left))
	var retained [3]byte
	_, err := io.ReadFull(left, retained[:])
	require.NoError(t, err)
	require.Equal(t, "TLS", string(retained[:]))
	require.NoError(t, <-done)
}

func TestGuestControlConnectRejectsMalformedAndCancelledBootstrap(t *testing.T) {
	for _, line := range []string{"OK 0\n", "OK 01\n", "OK -1\n", "OK 4294967296\n",
		"OK 1 extra\n", "NO 1\n", strings.Repeat("x", 64)} {
		t.Run(strings.TrimSpace(line), func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				var request [14]byte
				_, _ = io.ReadFull(right, request[:])
				_, _ = io.WriteString(right, line)
			}()
			require.Error(t, connectGuestControl(ctx, left))
			<-done
		})
	}
	t.Run("cancel blocked acknowledgement", func(t *testing.T) {
		left, right := net.Pipe()
		defer left.Close()
		defer right.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- connectGuestControl(ctx, left) }()
		var request [14]byte
		_, err := io.ReadFull(right, request[:])
		require.NoError(t, err)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
}

func TestGuestControlSessionCorrelatesOneOutstandingRequest(t *testing.T) {
	session, agent := guestControlSessionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request := guestControlSessionRequest()
	written := make(chan error, 1)
	go func() { written <- guestcontrolwire.EncodeRequest(ctx, agent, request) }()
	got, err := session.ReceiveRequest(ctx)
	require.NoError(t, err)
	require.NoError(t, <-written)
	if diff := cmp.Diff(request, got); diff != "" {
		t.Errorf("received request (-want +got):\n%s", diff)
	}
	_, err = session.ReceiveRequest(ctx)
	require.Error(t, err, "a second request must not be consumed before response")
	response := guestcontrolwire.Response{RequestID: request.RequestID, Operation: request.Operation,
		Failure: &guestcontrolwire.Failure{Code: guestcontrolwire.FailureDenied}}
	foreign := response
	foreign.RequestID = "22222222-2222-4222-8222-222222222222"
	require.Error(t, session.SendResponse(ctx, foreign))
	type received struct {
		response guestcontrolwire.Response
		err      error
	}
	read := make(chan received, 1)
	go func() {
		value, err := guestcontrolwire.DecodeResponse(ctx, agent, request)
		read <- received{value, err}
	}()
	require.NoError(t, session.SendResponse(ctx, response))
	observed := <-read
	require.NoError(t, observed.err)
	if diff := cmp.Diff(response, observed.response); diff != "" {
		t.Errorf("response (-want +got):\n%s", diff)
	}
	require.Error(t, session.SendResponse(ctx, response), "a completed response must not replay")
}

func TestGuestControlSessionCancellationClosesOwnedStreamAndCopies(t *testing.T) {
	session, agent := guestControlSessionFixture(t)
	copy := *session
	principal, err := session.Principal()
	require.NoError(t, err)
	principal.OwnerUID = 9999
	actual, err := copy.Principal()
	require.NoError(t, err)
	require.Equal(t, uint32(1000), actual.OwnerUID)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := session.ReceiveRequest(ctx); done <- err }()
	// A partial encrypted frame proves the receive is blocked inside framed I/O before cancellation.
	_, err = agent.Write([]byte{'M'})
	require.NoError(t, err)
	cancel()
	err = <-done
	require.ErrorIs(t, err, context.Canceled)
	_, err = session.Principal()
	require.Error(t, err)
	deadlineErr := agent.SetReadDeadline(time.Now().Add(time.Second))
	// net.Pipe refuses deadline changes after the peer transport has already closed.
	require.True(t, deadlineErr == nil || errors.Is(deadlineErr, io.ErrClosedPipe))
	var b [1]byte
	_, err = agent.Read(b[:])
	require.Error(t, err)
	var timeout net.Error
	require.False(t, errors.As(err, &timeout) && timeout.Timeout(), "closure must be observed, not a read timeout")
}

func TestGuestControlSessionCloseInterruptsBlockedReceive(t *testing.T) {
	session, agent := guestControlSessionFixture(t)
	done := make(chan error, 1)
	go func() { _, err := session.ReceiveRequest(t.Context()); done <- err }()
	_, err := agent.Write([]byte{'M'})
	require.NoError(t, err)
	require.NoError(t, session.Close(t.Context()))
	require.Error(t, <-done)
	require.NoError(t, session.Close(t.Context()))
}

func TestGuestControlSessionExitWaitOwnsIndependentDescriptor(t *testing.T) {
	session, _ := guestControlSessionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- session.WaitVMMExit(ctx) }()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, session.Close(t.Context()))
	require.Error(t, session.WaitVMMExit(t.Context()))
}

func TestGuestControlSessionPublicMethodsRejectMissingAuthority(t *testing.T) {
	var session *GuestControlSession
	_, err := session.Principal()
	require.Error(t, err)
	_, err = session.ReceiveRequest(t.Context())
	require.Error(t, err)
	require.Error(t, session.SendResponse(t.Context(), guestcontrolwire.Response{}))
	require.Error(t, session.WaitVMMExit(t.Context()))
	require.NoError(t, session.Close(t.Context()))
	_, err = NewHostVsockServer().OpenGuestControlSession(nil, 1000, 1000,
		HostVsockOpenRequest{VMID: strings.Repeat("a", 32)})
	require.Error(t, err)
}

// responseWriteBarrierConn exposes the full second application write to its peer before returning it.
// The wire encoder writes prefix then body; this barrier controls scheduling without sleeps or production hooks.
type responseWriteBarrierConn struct {
	net.Conn
	mu        sync.Mutex
	remaining int
	visible   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (connection *responseWriteBarrierConn) Write(value []byte) (int, error) {
	count, err := connection.Conn.Write(value)
	if err != nil {
		return count, err
	}
	connection.mu.Lock()
	blocked := false
	if connection.remaining > 0 {
		connection.remaining--
		blocked = connection.remaining == 0
	}
	connection.mu.Unlock()
	if blocked {
		close(connection.visible)
		<-connection.release
	}
	return count, err
}
func (connection *responseWriteBarrierConn) unblock() {
	connection.once.Do(func() { close(connection.release) })
}

func TestGuestControlSessionAcceptsImmediateSequentialRequestBeforeResponseWriteReturns(t *testing.T) {
	var barrier *responseWriteBarrierConn
	session, agent := guestControlSessionConnFixture(t, func(raw net.Conn) net.Conn {
		barrier = &responseWriteBarrierConn{Conn: raw, visible: make(chan struct{}), release: make(chan struct{})}
		return barrier
	})
	defer barrier.unblock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	first := guestControlSessionRequest()
	sent := make(chan error, 1)
	go func() { sent <- guestcontrolwire.EncodeRequest(ctx, agent, first) }()
	_, err := session.ReceiveRequest(ctx)
	require.NoError(t, err)
	require.NoError(t, <-sent)
	barrier.mu.Lock()
	barrier.remaining = 2
	barrier.mu.Unlock()
	response := guestcontrolwire.Response{
		RequestID: first.RequestID,
		Operation: first.Operation,
		Failure:   &guestcontrolwire.Failure{Code: guestcontrolwire.FailureDenied},
	}
	delivered := make(chan error, 1)
	go func() { delivered <- session.SendResponse(ctx, response) }()
	_, err = guestcontrolwire.DecodeResponse(ctx, agent, first)
	require.NoError(t, err)
	select {
	case <-barrier.visible:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	next := first
	next.RequestID = "22222222-2222-4222-8222-222222222222"
	// The first response is fully visible while its final host Write is still blocked.
	require.NoError(t, guestcontrolwire.EncodeRequest(ctx, agent, next))
	observed, err := session.ReceiveRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, next, observed)
	select {
	case err := <-delivered:
		t.Fatalf("response Write returned before barrier release: %v", err)
	default:
	}
	barrier.unblock()
	require.NoError(t, <-delivered)
	_, err = session.Principal()
	require.NoError(t, err, "sequential handoff must keep this authenticated stream alive")
}

// Rationale: cold startup must wait only for an explicit transport refusal before
// authentication; truncated or malformed peer input must not gain retry semantics.
func TestGuestControlConnectClassifiesOnlyUnavailableTransport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ack       string
		wantRetry bool
	}{
		{"not listening", "", true},
		{"partial acknowledgement", "OK ", false},
		{"malformed acknowledgement", "ERR unavailable\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer right.Close()
				var request [14]byte
				_, _ = io.ReadFull(right, request[:])
				_, _ = io.WriteString(right, tc.ack)
			}()
			err := connectGuestControl(ctx, left)
			require.Error(t, err)
			de := errs.AsDomainError(err)
			if tc.wantRetry {
				require.NotNil(t, de)
				require.Equal(t, errs.CodeAgentUnreachable, de.Code)
				require.Equal(t, errs.ClassRetryable, de.Class)
			} else if de != nil {
				require.NotEqual(t, errs.CodeAgentUnreachable, de.Code)
			}
			<-done
		})
	}
	for _, cause := range []error{unix.ECONNRESET, unix.EPIPE, unix.ECONNREFUSED} {
		t.Run(cause.Error(), func(t *testing.T) {
			err := guestControlConnectTransportError(t.Context(), cause, true)
			require.Equal(t, errs.CodeAgentUnreachable, errs.AsDomainError(err).Code)
			require.ErrorIs(t, err, cause)
			require.Equal(t, cause, guestControlConnectTransportError(t.Context(), cause, false))
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, guestControlConnectTransportError(ctx, io.EOF, true), io.EOF)
	require.Nil(t, errs.AsDomainError(guestControlConnectTransportError(ctx, io.EOF, true)))
}
