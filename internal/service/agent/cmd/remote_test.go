// Package main tests runRemoteSubcommand, which connects to the guest agent
// daemon's local Unix socket, sends a RemoteVMRequest, and relays response
// frames to stdout/stderr.
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/agent"
)

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// startMockDaemonListener starts a Unix socket that mimics the guest agent
// daemon. It reads a RemoteVMRequest, then sends the given response frames
// encoded as newline-delimited JSON. Returns the socket path.
func startMockDaemonListener(t *testing.T, respFrames []remoteFrame) string {
	t.Helper()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "mock-daemon.sock")

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the RemoteVMRequest (one JSON line)
		var req agent.RemoteVMRequest
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		_ = req

		// Send response frames
		enc := json.NewEncoder(conn)
		for _, f := range respFrames {
			if err := enc.Encode(f); err != nil {
				return
			}
		}
	}()

	time.Sleep(5 * time.Millisecond)
	return sockPath
}

// --- runRemoteSubcommand ---
// Rationale: runRemoteSubcommand connects to the daemon socket, sends a
// RemoteVMRequest, reads response frames, and returns the exit code.

func TestRunRemoteSubcommand_SuccessExitZero(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "hello\n"},
		{Type: "remote_vm", Status: 0},
	})

	// Capture stdout/stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "echo hello"})

	// Restore stdout/stderr
	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "hello\n", stdoutBuf.String())
	assert.Empty(t, stderrBuf.String())
}

// Rationale: Non-zero exit code from remote_vm response must be returned.

func TestRunRemoteSubcommand_NonZeroExitCode(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "output\n"},
		{Type: "remote_vm", Status: 42},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "my-command"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 42, exitCode)
	assert.Equal(t, "output\n", stdoutBuf.String())
}

// Rationale: Remote_vm response with error must print the error to stderr
// and return the status code.

func TestRunRemoteSubcommand_ErrorInResponse(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "remote_vm", Status: 1, Error: "target VM not found"},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "bad-command"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Empty(t, stdoutBuf.String())
	assert.Contains(t, stderrBuf.String(), "target VM not found")
}

// Rationale: Connection failure to the daemon socket must print error to
// stderr and return exit code 1.

func TestRunRemoteSubcommand_ConnectionFailure(t *testing.T) {
	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand("/nonexistent/socket.sock", []string{"target-vm", "ls"})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderrBuf.String(), "failed to connect")
}

// Rationale: Insufficient args must print usage and return exit code 1.

func TestRunRemoteSubcommand_NotEnoughArgs(t *testing.T) {
	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand("/tmp/test.sock", []string{})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderrBuf.String(), "usage:")
}

func TestRunRemoteSubcommand_NotEnoughArgs_OnlyDestination(t *testing.T) {
	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand("/tmp/test.sock", []string{"target-vm"})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderrBuf.String(), "usage:")
}

// Rationale: Empty command (after "--" with no args) must print error.

func TestRunRemoteSubcommand_EmptyCommand(t *testing.T) {
	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	// Args: destination="tgt", then "--" with nothing after
	exitCode := runRemoteSubcommand("/tmp/test.sock", []string{"tgt", "--"})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderrBuf.String(), "no command specified")
}

// Rationale: The "--" separator must be handled correctly, treating
// everything after "--" as a single command string.

func TestRunRemoteSubcommand_DashDashSeparator(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "done\n"},
		{Type: "remote_vm", Status: 0},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	// Test with "--" separator: everything after is the command
	exitCode := runRemoteSubcommand(sockPath,
		[]string{"target-vm", "--", "echo", "hello", "world"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "done\n", stdoutBuf.String())
}

// Rationale: Stderr frames must be written to os.Stderr.

func TestRunRemoteSubcommand_StderrFrames(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "out\n"},
		{Type: "stderr", Data: "err msg\n"},
		{Type: "remote_vm", Status: 0},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "cmd"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "out\n", stdoutBuf.String())
	assert.Equal(t, "err msg\n", stderrBuf.String())
}

// Rationale: Malformed JSON from the daemon must print error and return 1.

func TestRunRemoteSubcommand_MalformedResponse(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "malformed.sock")

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Send malformed JSON
		_, _ = conn.Write([]byte("not valid json\n"))
	}()

	time.Sleep(5 * time.Millisecond)

	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "ls"})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderrBuf.String(), "failed to parse")
}

// Rationale: Multiple stdout frames before remote_vm must be accumulated and
// printed in order.

func TestRunRemoteSubcommand_MultipleStdoutFrames(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "line1\n"},
		{Type: "stdout", Data: "line2\n"},
		{Type: "stdout", Data: "line3\n"},
		{Type: "remote_vm", Status: 0},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "seq 1 3"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "line1\nline2\nline3\n", stdoutBuf.String())
}

// Rationale: Unknown frame types must be silently ignored (only stdout,
// stderr, and remote_vm are processed).

func TestRunRemoteSubcommand_UnknownFramesIgnored(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "heartbeat", Data: "ping"},
		{Type: "stdout", Data: "visible\n"},
		{Type: "metrics", Data: "cpu=42"},
		{Type: "remote_vm", Status: 0},
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "test"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "visible\n", stdoutBuf.String())
	assert.Empty(t, stderrBuf.String())
}

// Rationale: EOF before remote_vm must return 0 (default).

func TestRunRemoteSubcommand_EOFBeforeFinalFrame(t *testing.T) {
	// Mock daemon that sends only stdout then closes
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "partial\n"},
		// no remote_vm — daemon closes connection
	})

	var stdoutBuf, stderrBuf bytes.Buffer
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "unstable"})

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 0, exitCode, "EOF before remote_vm must return 0")
	assert.Equal(t, "partial\n", stdoutBuf.String())
}

// Rationale: Destination and command parsing from args must work correctly.

func TestRunRemoteSubcommand_MultiWordCommand(t *testing.T) {
	sockPath := startMockDaemonListener(t, []remoteFrame{
		{Type: "stdout", Data: "result\n"},
		{Type: "remote_vm", Status: 0},
	})

	var stdoutBuf bytes.Buffer
	oldStdout := os.Stdout
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stdoutBuf.ReadFrom(rOut)
	}()

	// Multi-word command without "--"
	exitCode := runRemoteSubcommand(sockPath,
		[]string{"target-vm", "sh", "-c", "echo hello"})

	wOut.Close()
	os.Stdout = oldStdout
	wg.Wait()

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "result\n", stdoutBuf.String())
}

// Test that args parse: first arg is destination, rest is command.
// This test uses a listener that captures the request to verify.
func TestRunRemoteSubcommand_ParsesArgsCorrectly(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "capture.sock")

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var capturedReq agent.RemoteVMRequest
	var reqMu sync.Mutex

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var req agent.RemoteVMRequest
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		reqMu.Lock()
		capturedReq = req
		reqMu.Unlock()

		_ = json.NewEncoder(conn).Encode(remoteFrame{Type: "remote_vm", Status: 0})
	}()

	time.Sleep(5 * time.Millisecond)

	exitCode := runRemoteSubcommand(sockPath,
		[]string{"my-vm", "--", "docker", "ps", "-a"})

	require.Equal(t, 0, exitCode)

	reqMu.Lock()
	assert.Equal(t, "my-vm", capturedReq.Destination)
	assert.Equal(t, "docker ps -a", capturedReq.Command)
	reqMu.Unlock()
}

// Test error from writeFrame to daemon (send request fails)
func TestRunRemoteSubcommand_SendRequestFails(t *testing.T) {
	// Create a socket that accepts but then closes immediately
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "fail-send.sock")

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Close immediately — send will fail or be lost
		conn.Close()
	}()

	time.Sleep(5 * time.Millisecond)

	var stderrBuf bytes.Buffer
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stderrBuf.ReadFrom(rErr)
	}()

	exitCode := runRemoteSubcommand(sockPath, []string{"target-vm", "ls"})

	wErr.Close()
	os.Stderr = oldStderr
	wg.Wait()

	assert.Equal(t, 1, exitCode)
	// If the request was sent but no response, we expect either "failed to
	// send request" or "failed to parse response" depending on timing.
	// Either way, exit code is 1.
	output := stderrBuf.String()
	assert.True(t, strings.Contains(output, "failed to send") ||
		strings.Contains(output, "failed to parse") ||
		strings.Contains(output, "error"),
		"expected error message in stderr, got: %q", output)
}
