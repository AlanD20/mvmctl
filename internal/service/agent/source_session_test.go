package agent

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

const agentSourceTestToken = "123e4567-e89b-42d3-a456-426614174000"

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
