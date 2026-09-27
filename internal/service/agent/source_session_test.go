package agent

import (
	"context"
	"io"
	"io/fs"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

const agentSourceTestToken = "123e4567-e89b-42d3-a456-426614174000"

type wrappedDeadlineSourceConn struct {
	net.Conn
	secondRead chan struct{}
	reads      int
}

func (conn *wrappedDeadlineSourceConn) Read(buf []byte) (int, error) {
	conn.reads++
	if conn.reads == 1 {
		return 0, &fs.PathError{Op: "read", Path: "vsock", Err: os.ErrDeadlineExceeded}
	}
	if conn.reads == 2 {
		close(conn.secondRead)
	}
	return conn.Conn.Read(buf)
}

func TestSourceDisconnectWatchContinuesAfterWrappedFileDeadline(t *testing.T) {
	host, guest := net.Pipe()
	t.Cleanup(func() { _ = host.Close() })
	t.Cleanup(func() { _ = guest.Close() })
	conn := &wrappedDeadlineSourceConn{Conn: guest, secondRead: make(chan struct{})}
	transport := newSourceTransport(conn)
	result := make(chan error, 1)
	go func() { result <- transport.watchHostDisconnect(t.Context()) }()

	select {
	case <-conn.secondRead:
	case err := <-result:
		t.Fatalf("deadline was treated as disconnect: %v", err)
	case <-time.After(time.Second):
		t.Fatal("disconnect watch did not continue after deadline")
	}
	require.NoError(t, host.Close())
	select {
	case err := <-result:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(time.Second):
		t.Fatal("disconnect watch did not observe host close")
	}
}

func TestSourceOpenAuthenticatesBeforeRegistration(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		agent.handleConnection(t.Context(), guest)
		close(done)
	}()

	require.NoError(t, vmexecwire.EncodeSourceBootstrap(t.Context(), host, vmexecwire.SourceBootstrap{
		AgentToken:      "123e4567-e89b-42d3-a456-426614174001",
		SourceSessionID: "00112233445566778899aabbccddeeff",
	}))
	_ = host.SetReadDeadline(time.Now().Add(time.Second))
	_, err := vmexecwire.DecodeSourceReady(t.Context(), host, "00112233445566778899aabbccddeeff")
	require.Error(t, err)
	assert.ErrorIs(t, err, io.EOF)
	<-done

	agent.sourceMu.Lock()
	assert.Empty(t, agent.pendingSources)
	agent.sourceMu.Unlock()
}

func TestSourceOpenRegistersOnePendingSessionAndCleansUpOnShutdown(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	ctx, cancel := context.WithCancel(context.Background())
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		agent.handleConnection(ctx, guest)
		close(done)
	}()

	sessionID := "00112233445566778899aabbccddeeff"
	require.NoError(t, vmexecwire.EncodeSourceBootstrap(ctx, host, vmexecwire.SourceBootstrap{
		AgentToken: agentSourceTestToken, SourceSessionID: sessionID,
	}))
	ready, err := vmexecwire.DecodeSourceReady(ctx, host, sessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionID, ready.SourceSessionID)

	agent.sourceMu.Lock()
	_, pending := agent.pendingSources[sessionID]
	agent.sourceMu.Unlock()
	assert.True(t, pending)
	cancel()
	<-done

	agent.sourceMu.Lock()
	assert.Empty(t, agent.pendingSources)
	agent.sourceMu.Unlock()
}

func TestSourceHostCloseAfterReadyImmediatelyCleansPendingSession(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		agent.handleConnection(t.Context(), guest)
		close(done)
	}()

	sessionID := "00112233445566778899aabbccddeeff"
	require.NoError(t, vmexecwire.EncodeSourceBootstrap(t.Context(), host, vmexecwire.SourceBootstrap{
		AgentToken: agentSourceTestToken, SourceSessionID: sessionID,
	}))
	_, err := vmexecwire.DecodeSourceReady(t.Context(), host, sessionID)
	require.NoError(t, err)
	require.NoError(t, host.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host EOF did not stop the source handler")
	}

	agent.sourceMu.Lock()
	defer agent.sourceMu.Unlock()
	assert.Empty(t, agent.pendingSources)
	assert.Empty(t, agent.sourceBindings)
}

func TestSourceHostCloseAfterReservationCleansBinding(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	agent.sourceProcesses = &fakeSourceProcessEffects{account: sourceAccount{UID: 0, GID: 0}}
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		agent.handleConnection(t.Context(), guest)
		close(done)
	}()

	sessionID := "00112233445566778899aabbccddeeff"
	require.NoError(t, vmexecwire.EncodeSourceBootstrap(t.Context(), host, vmexecwire.SourceBootstrap{
		AgentToken: agentSourceTestToken, SourceSessionID: sessionID,
	}))
	_, err := vmexecwire.DecodeSourceReady(t.Context(), host, sessionID)
	require.NoError(t, err)
	binding, err := agent.reserveSourceSession(t.Context(), sessionID, "")
	require.NoError(t, err)
	require.NoError(t, host.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host EOF did not stop a reserved source handler")
	}

	agent.sourceMu.Lock()
	defer agent.sourceMu.Unlock()
	assert.Empty(t, agent.pendingSources)
	assert.Empty(t, agent.sourceBindings)
	select {
	case <-binding.transport.done:
	default:
		t.Fatal("reserved transport remained open after host EOF")
	}
}

func TestDuplicateSourceSessionIsRejected(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	firstHost, firstAgent := net.Pipe()
	secondHost, secondAgent := net.Pipe()
	t.Cleanup(func() { _ = firstHost.Close() })
	t.Cleanup(func() { _ = secondHost.Close() })

	first := newSourceTransport(firstAgent)
	second := newSourceTransport(secondAgent)
	assert.True(t, agent.registerPendingSource("00112233445566778899aabbccddeeff", first))
	assert.False(t, agent.registerPendingSource("00112233445566778899aabbccddeeff", second))
	agent.closeAllSourceSessions()
}

func TestCloseAllSourceSessionsReleasesPendingBindingsAndPins(t *testing.T) {
	agent := New(9999, agentSourceTestToken, "/tmp/not-used.sock")
	pendingHost, pendingAgent := net.Pipe()
	boundHost, boundAgent := net.Pipe()
	t.Cleanup(func() { _ = pendingHost.Close() })
	t.Cleanup(func() { _ = boundHost.Close() })
	pending := newSourceTransport(pendingAgent)
	bound := newSourceTransport(boundAgent)
	rootPin := &fakePinnedProcess{}
	peerPin := &fakePinnedProcess{}
	binding := &sourceBinding{
		sessionID: "ffeeddccbbaa99887766554433221100",
		transport: bound,
		published: closedTestChannel(),
		rootPin:   rootPin,
		peerPin:   peerPin,
	}
	agent.sourceMu.Lock()
	agent.pendingSources["00112233445566778899aabbccddeeff"] = pending
	agent.sourceBindings[binding.sessionID] = binding
	agent.sourceMu.Unlock()

	agent.closeAllSourceSessions()
	agent.sourceMu.Lock()
	assert.Empty(t, agent.pendingSources)
	assert.Empty(t, agent.sourceBindings)
	agent.sourceMu.Unlock()
	assert.True(t, rootPin.closed)
	assert.True(t, peerPin.closed)
	select {
	case <-pending.done:
	default:
		t.Fatal("pending transport was not closed")
	}
	select {
	case <-bound.done:
	default:
		t.Fatal("bound transport was not closed")
	}
}

func closedTestChannel() chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}
