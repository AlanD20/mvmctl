package vsock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

// --- relayTTY ---
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

	// --- Step 1: stdin → vsock ---
	// Prove the relay is working in the user-input direction.
	_, err := stdinW.Write([]byte("echo hello\n"))
	require.NoError(t, err)
	_ = agentConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := agentConn.Read(buf)
	require.NoError(t, err, "stdin data must reach agent side within 5s")
	require.Equal(t, "echo hello\n", string(buf[:n]))

	// --- Step 2: vsock → stdout ---
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

	// --- Step 3: Close agent-side connection (guest disco ---
	// This simulates the guest closing the vsock connection (e.g., after
	// typing "exit" in the shell).  No data is written to stdin after this
	// point — no "keypress" to unblock relay.
	agentConn.Close()

	// --- Step 4: Verify relay exits WITHOUT a keypress ---
	// If the fix is working, relayTTY will close the dup'd stdin when the
	// vsock→stdout goroutine finishes (hostConn.Read returns EOF), which
	// unblocks io.Copy(conn, stdin) by making stdin.Read return EOF — no
	// keypress required.
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

// --- lockedWriter ---
// Rationale: lockedWriter serialises Write calls through a shared mutex so
// that concurrent writers (stdin relay and resize frames) cannot interleave
// bytes at the application framing level. Without this, a resize frame could
// be written in the middle of a stdin data chunk, corrupting the JSON framing
// or producing a mangled byte stream on the agent side.

// recordingConn is a minimal net.Conn stub that records every Write call's
// byte content. All other methods return safe defaults without side effects.
type recordingConn struct {
	mu     sync.Mutex
	writes [][]byte
}

func (c *recordingConn) Read(b []byte) (int, error) { return 0, io.EOF }
func (c *recordingConn) Write(b []byte) (int, error) {
	buf := make([]byte, len(b))
	copy(buf, b)
	c.mu.Lock()
	c.writes = append(c.writes, buf)
	c.mu.Unlock()
	return len(b), nil
}
func (c *recordingConn) Close() error { return nil }
func (c *recordingConn) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: "local", Net: "unix"}
}
func (c *recordingConn) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: "remote", Net: "unix"}
}
func (c *recordingConn) SetDeadline(t time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(t time.Time) error { return nil }

func TestLockedWriter_ConcurrentWritesNotInterleaved(t *testing.T) {
	rc := &recordingConn{}
	lc := &lockedWriteConn{Conn: rc}
	lw := lockedWriter{lc: lc}

	patternA := []byte("AAA")
	patternB := []byte("BBB")

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: writes patternA 100 times
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, err := lw.Write(patternA)
			assert.NoError(t, err)
		}
	}()

	// Goroutine 2: writes patternB 100 times concurrently
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, err := lw.Write(patternB)
			assert.NoError(t, err)
		}
	}()

	wg.Wait()

	// Lock to safely inspect recorded writes
	rc.mu.Lock()
	defer rc.mu.Unlock()

	require.Len(t, rc.writes, 200,
		"must have exactly 200 recorded writes (100 from each goroutine)")

	for i, w := range rc.writes {
		if !bytes.Equal(w, patternA) && !bytes.Equal(w, patternB) {
			t.Errorf("write %d contains interleaved content: %q (len=%d)", i, w, len(w))
		}
	}
}

// --- getTerminalSize ---
// Rationale: getTerminalSize tries stdin, stdout, stderr in order and returns
// the first non-zero size. This is the root cause of the "tiny terminal" bug:
// when stdin is not a terminal (e.g., piped or closed), term.GetSize(stdin)
// fails and returns (0,0). The fix is to fall back to stdout and stderr.

func TestGetTerminalSize_FallbackOrder(t *testing.T) {
	origTermGetSize := termGetSize
	t.Cleanup(func() { termGetSize = origTermGetSize })

	// Subtest: stdin fails, stdout succeeds — should return stdout size.
	t.Run("stdin_fails_stdout_succeeds", func(t *testing.T) {
		termGetSize = func(fd int) (int, int, error) {
			switch fd {
			case int(os.Stdin.Fd()):
				return 0, 0, errors.New("not a terminal")
			case int(os.Stdout.Fd()):
				return 80, 24, nil // width=80, height=24
			default:
				return 0, 0, errors.New("not a terminal")
			}
		}
		rows, cols, ok := getTerminalSize()
		assert.True(t, ok, "should succeed on stdout")
		assert.Equal(t, 24, rows, "rows should be height")
		assert.Equal(t, 80, cols, "cols should be width")
	})

	// Subtest: all three FDs fail — should return ok=false.
	t.Run("all_fds_fail", func(t *testing.T) {
		termGetSize = func(fd int) (int, int, error) {
			return 0, 0, errors.New("not a terminal")
		}
		rows, cols, ok := getTerminalSize()
		assert.False(t, ok, "should fail when no terminal is available")
		assert.Equal(t, 0, rows)
		assert.Equal(t, 0, cols)
	})
}

// --- Shell frame verification ---
// Rationale: Shell must send an exec-tty frame with the correct terminal
// dimensions and, after the TTY ack, send an explicit resize frame. These
// tests catch regressions where term.GetSize returns (0,0) (because stdin
// is piped) and the agent never sets a window size — causing the "tiny
// terminal" bug where interactive shells open with 0x0 dimensions.

// mockDialFn returns a function that returns one end of a net.Pipe as the
// vsock connection, bypassing the real UDS dial. Used by Shell tests to
// avoid requiring a running Firecracker VM.
func mockDialFn(t *testing.T, clientConn net.Conn) func(context.Context, string, int, int) (net.Conn, error) {
	t.Helper()
	return func(_ context.Context, _ string, _ int, _ int) (net.Conn, error) {
		return clientConn, nil
	}
}

func TestShell_SendsInitialTerminalSize(t *testing.T) {
	origTermGetSize := termGetSize
	t.Cleanup(func() { termGetSize = origTermGetSize })

	// Mock terminal size: width=80, height=24.
	termGetSize = func(fd int) (int, int, error) {
		return 80, 24, nil
	}

	clientConn, agentConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	t.Cleanup(func() { agentConn.Close() })

	c := &Client{
		item: &model.VsockConfigItem{
			VmID:    "test-vm",
			UDSPath: "/tmp/test.sock",
			Port:    1024,
			Token:   "test-token",
		},
		ProbeTimeout:     time.Minute,
		skipVersionCheck: true,
		dialFn:           mockDialFn(t, clientConn),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Shell(context.Background(), "root")
	}()

	// Read the exec-tty request from the agent side.
	var req execRequest
	require.NoError(t, readFrame(agentConn, &req), "should read exec-tty frame")
	assert.Equal(t, "exec-tty", req.Type)
	assert.Equal(t, 24, req.Rows, "Rows must be height (24), not width")
	assert.Equal(t, 80, req.Cols, "Cols must be width (80), not height")
	assert.Equal(t, "test-token", req.Token)
	assert.Equal(t, "root", req.User)

	// Close agent side to unblock Shell (it is blocked waiting for TTY ack).
	agentConn.Close()
	<-errCh // Shell error is expected (MakeRaw fails on non-TTY stdin)
}

func TestShell_SendsResizeFrameAfterAck(t *testing.T) {
	origTermGetSize := termGetSize
	t.Cleanup(func() { termGetSize = origTermGetSize })

	// Mock terminal size: width=100, height=42 (non-standard, easy to verify).
	termGetSize = func(fd int) (int, int, error) {
		return 100, 42, nil
	}

	clientConn, agentConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	t.Cleanup(func() { agentConn.Close() })

	c := &Client{
		item: &model.VsockConfigItem{
			VmID:    "test-vm",
			UDSPath: "/tmp/test.sock",
			Port:    1024,
			Token:   "test-token",
		},
		ProbeTimeout:     time.Minute,
		skipVersionCheck: true,
		dialFn:           mockDialFn(t, clientConn),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Shell(context.Background(), "root")
	}()

	// Step 1: Read exec-tty request.
	var req execRequest
	require.NoError(t, readFrame(agentConn, &req), "should read exec-tty frame")
	assert.Equal(t, "exec-tty", req.Type)
	assert.Equal(t, 42, req.Rows)
	assert.Equal(t, 100, req.Cols)

	// Step 2: Send TTY ack (as the agent would).
	require.NoError(t, sendFrame(agentConn, execResponse{Type: "tty"}))

	// Step 3: Read the resize frame that Shell sends after receiving the ack.
	var resizeReq execRequest
	require.NoError(t, readFrame(agentConn, &resizeReq), "should read resize frame after TTY ack")
	assert.Equal(t, "resize", resizeReq.Type,
		"frame after TTY ack must be a resize frame, not %q", resizeReq.Type)
	assert.Equal(t, 42, resizeReq.Rows, "resize rows must match terminal height")
	assert.Equal(t, 100, resizeReq.Cols, "resize cols must match terminal width")

	// Cleanup: close agent side to unblock Shell.
	agentConn.Close()
	<-errCh
}
