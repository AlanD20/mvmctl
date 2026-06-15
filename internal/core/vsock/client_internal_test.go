package vsock

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── relayTTY ──────────────────────────────────────────────────────────────
// Rationale: relayTTY is the bidirectional relay loop at the heart of Shell().
// Two goroutines relay stdin→vsock and vsock→stdout. When one direction ends
// (EOF), the connection is closed to unblock the other. Must prove data flows
// in both directions simultaneously through the full host-side relay chain.

func TestRelayTTY_BidirectionalRelay(t *testing.T) {
	// Create a pipe pair simulating the vsock connection.
	hostConn, agentConn := net.Pipe()
	defer hostConn.Close()
	defer agentConn.Close()

	// Create mock stdin/stdout.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	// Start relay in a goroutine.
	relayErr := make(chan error, 1)
	go func() {
		relayErr <- relayTTY(hostConn, stdinR, stdoutW)
	}()

	// Direction 1: stdin → vsock (user input reaches guest agent)
	_, err := stdinW.Write([]byte("echo hello\n"))
	require.NoError(t, err)

	// Verify the data appears on the agent side.
	buf := make([]byte, 1024)
	_ = agentConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := agentConn.Read(buf)
	require.NoError(t, err, "agent must receive stdin data within 5s")
	assert.Equal(t, "echo hello\n", string(buf[:n]))

	// Direction 2: vsock → stdout (agent output reaches user terminal)
	_, err = agentConn.Write([]byte("hello\n"))
	require.NoError(t, err)

	// Use goroutine + channel for timeout (io.PipeReader has no SetReadDeadline).
	stdoutCh := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		var rn int
		rn, err = stdoutR.Read(buf)
		stdoutCh <- struct {
			n   int
			err error
		}{rn, err}
	}()
	select {
	case res := <-stdoutCh:
		require.NoError(t, res.err, "stdout must receive agent data within 5s")
		assert.Equal(t, "hello\n", string(buf[:res.n]))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stdout data")
	}

	// Close stdin to signal EOF → relay should stop.
	stdinW.Close()
	// Wait for relay to finish.
	relayErrVal := <-relayErr
	// relayTTY always returns nil (errors from io.Copy are discarded).
	assert.NoError(t, relayErrVal)

	// Verify that closing stdin caused the relay to stop
	// and the agent connection was closed.
	_, err = agentConn.Read(buf)
	assert.Error(t, err, "agent connection must be closed after relay stops")
	assert.True(t, strings.Contains(err.Error(), "closed") ||
		strings.Contains(err.Error(), "EOF") ||
		strings.Contains(err.Error(), "use of closed"),
		"error must indicate closed connection, got: %v", err)
}

func TestRelayTTY_ExitWithoutKeypress(t *testing.T) {
	// Create a pipe pair simulating the vsock connection.
	hostConn, agentConn := net.Pipe()

	// Create mock stdin/stdout.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	// Start relay in a goroutine.
	relayErr := make(chan error, 1)
	go func() {
		relayErr <- relayTTY(hostConn, stdinR, stdoutW)
	}()

	buf := make([]byte, 1024)

	// ── Step 1: stdin → vsock ──────────────────────────────────────────
	// Prove the relay is working in the user-input direction.
	_, err := stdinW.Write([]byte("echo hello\n"))
	require.NoError(t, err)
	_ = agentConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := agentConn.Read(buf)
	require.NoError(t, err, "stdin data must reach agent side within 5s")
	require.Equal(t, "echo hello\n", string(buf[:n]))

	// ── Step 2: vsock → stdout ─────────────────────────────────────────
	// Prove the relay is working in the output direction.
	_, err = agentConn.Write([]byte("hello\n"))
	require.NoError(t, err)

	stdoutCh := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		var rn int
		rn, err = stdoutR.Read(buf)
		stdoutCh <- struct {
			n   int
			err error
		}{rn, err}
	}()
	select {
	case res := <-stdoutCh:
		require.NoError(t, res.err, "stdout must receive agent data within 5s")
		require.Equal(t, "hello\n", string(buf[:res.n]))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stdout data")
	}

	// ── Step 3: Close agent-side connection (guest disconnects) ────────
	// This simulates the guest closing the vsock connection (e.g., after
	// typing "exit" in the shell).  No data is written to stdin after this
	// point — no "keypress" to unblock relay.
	agentConn.Close()

	// ── Step 4: Verify relay exits WITHOUT a keypress ──────────────────
	// If the fix is working, relayTTY will close the dup'd stdin when the
	// vsock→stdout goroutine finishes (hostConn.Read returns EOF), which
	// unblocks io.Copy(conn, stdin) by making stdin.Read return EOF — no
	// keypress required.
	//
	// If the fix is absent, relayTTY blocks here forever because
	// io.Copy(conn, stdin) is stuck on stdin.Read().
	select {
	case relayErrVal := <-relayErr:
		assert.NoError(t, relayErrVal, "relayTTY must return nil on clean close")
	case <-time.After(1 * time.Second):
		t.Fatal("relayTTY did not return within 1s — this is the " +
			"'2 keypresses to exit' bug: relay is blocked on stdin.Read()")
	}

	// Cleanup: close remaining pipes.  The relay owns stdinR (ReadCloser)
	// and has already closed it; hostConn has been closed by relayTTY via
	// conn.Close().  We close our write ends so the pipe layer doesn't leak.
	stdinW.Close()
	stdoutW.Close()
	agentConn.Close() // idempotent
	hostConn.Close()  // idempotent
}
