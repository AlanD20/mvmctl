package vsock_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
)

// NOTE: readFrame and dialAndHandshake are unexported in the vsock
// package, so they cannot be tested directly from an external test package.
// They are also thin wrappers around stdlib json.Encode/Decode — testing them
// with bytes.Buffer would exercise stdlib, not our custom logic.
// Instead, these tests exercise the full protocol through Client.Exec, which
// internally calls dialAndHandshake → sendFrame → readFrame. A local mock
// UDS server simulates the guest agent's CONNECT handshake and JSON framing.

// startMockAgent starts a Unix socket server that mimics the vsock
// guest agent for the CONNECT handshake and optionally responds to exec
// requests with the given result.
func startMockAgent(t *testing.T, handshakeOK bool, execResult *vsock.ExecResult) (string, int) {
	t.Helper()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "mock-vsock.sock")
	port := 1024

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()

				reader := bufio.NewReader(conn)

				// Read CONNECT handshake line
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				_ = line // "CONNECT 1024\n"

				if handshakeOK {
					_, _ = fmt.Fprintf(conn, "OK %d\n", port)
				} else {
					_, _ = fmt.Fprintf(conn, "ERR\n")
				}

				// If an exec response is configured, handle the version probe
				// followed by the exec request.
				if execResult != nil {
					// Read first JSON frame — might be version probe or exec request
					firstLine, _ := reader.ReadString('\n')
					var firstReq struct {
						Type string `json:"type"`
					}
					if json.Unmarshal([]byte(firstLine), &firstReq) == nil && firstReq.Type == "version" {
						// Respond to version probe
						versionData, _ := json.Marshal(map[string]string{"agent_version": "0.0.0"})
						versionResp := map[string]any{
							"id":   "v:1",
							"type": "version",
							"data": string(versionData),
						}
						data, _ := json.Marshal(versionResp)
						_, _ = conn.Write(data)
						_, _ = conn.Write([]byte("\n"))
						// Read the next frame — the actual exec request
						_, _ = reader.ReadString('\n')
					}
					// If first frame was not a version probe, it was already the
					// exec request — nothing more to read.

					// Send stdout frame if there is stdout data.
					if execResult.Stdout != "" {
						stdoutFrame := map[string]any{
							"id":   "1",
							"type": "stdout",
							"data": execResult.Stdout,
						}
						data, _ := json.Marshal(stdoutFrame)
						_, _ = conn.Write(data)
						_, _ = conn.Write([]byte("\n"))
					}

					// Send stderr frame if there is stderr data.
					if execResult.Stderr != "" {
						stderrFrame := map[string]any{
							"id":   "1",
							"type": "stderr",
							"data": execResult.Stderr,
						}
						data, _ := json.Marshal(stderrFrame)
						_, _ = conn.Write(data)
						_, _ = conn.Write([]byte("\n"))
					}

					// Send result frame.
					resultFrame := map[string]any{
						"id":     "1",
						"type":   "result",
						"status": execResult.ExitCode,
					}
					data, _ := json.Marshal(resultFrame)
					_, _ = conn.Write(data)
					_, _ = conn.Write([]byte("\n"))
				}
			}()
		}
	}()

	// Allow the goroutine to start and the listener to be ready.
	time.Sleep(5 * time.Millisecond)

	return sockPath, port
}

// --- DialAndHandshake: Success ---
// Rationale: The CONNECT handshake is the entry point for all vsock
// communication. A failure here makes all Exec/Shell calls fail.

func TestClient_DialAndHandshake_Success(t *testing.T) {
	sockPath, port := startMockAgent(t, true, &vsock.ExecResult{
		Stdout:   "hello\n",
		Stderr:   "",
		ExitCode: 0,
	})

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: sockPath,
		Port:    port,
		Token:   "test-token",
	}, time.Second)

	result, err := client.Exec(ctx, "echo hello", "root", 5, nil, false)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.Equal(t, "", result.Stderr)
	assert.Equal(t, 0, result.ExitCode)
}

// --- DialAndHandshake: Bad response ---
// Rationale: If the agent sends an unexpected handshake response, the client
// must fail with an appropriate handshake error.

func TestClient_DialAndHandshake_BadResponse(t *testing.T) {
	sockPath, port := startMockAgent(t, false, nil)

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: sockPath,
		Port:    port,
		Token:   "test-token",
	}, time.Second)

	_, err := client.Exec(ctx, "echo hello", "root", 5, nil, false)
	require.Error(t, err)
	// waitForAgent retries until the probe timeout, then returns its own
	// error wrapping the underlying handshake failure.
	assert.Contains(t, err.Error(), "reachable")
}

// --- DialAndHandshake: Context cancellation ---
// Rationale: Context cancellation must abort the dial before it connects.
// The function takes ctx context.Context and must respect ctx.Done().

func TestClient_DialAndHandshake_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: "/nonexistent/vsock.sock",
		Port:    1024,
		Token:   "test-token",
	}, time.Millisecond)

	_, err := client.Exec(ctx, "echo hello", "root", 5, nil, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- Exported protocol primitives ---
// Rationale: SendFrame and ReadFrame are the exported building blocks for
// external consumers. They must write/read newline-delimited JSON correctly.
// NOTE: net.Pipe has no internal buffering — reads and writes must be
// concurrent. Each test uses a goroutine for either the read or write side.

func TestSendFrame(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	payload := map[string]string{"type": "exec", "command": "ls"}

	// Read must happen concurrently with Write (net.Pipe is synchronous)
	type readResult struct {
		n   int
		err error
	}
	rr := make(chan readResult, 1)
	buf := make([]byte, 1024)
	go func() {
		n, err := server.Read(buf)
		rr <- readResult{n, err}
	}()

	err := vsock.SendFrame(client, payload)
	require.NoError(t, err)

	res := <-rr
	require.NoError(t, res.err)

	var decoded map[string]string
	err = json.Unmarshal(buf[:res.n], &decoded)
	require.NoError(t, err)
	assert.Equal(t, "exec", decoded["type"])
	assert.Equal(t, "ls", decoded["command"])
	// Must end with newline
	assert.Equal(t, byte('\n'), buf[res.n-1], "SendFrame must append newline")
}

func TestSendFrame_ClosedConnection(t *testing.T) {
	server, client := net.Pipe()
	server.Close()

	err := vsock.SendFrame(client, map[string]string{"type": "test"})
	require.Error(t, err)
}

func TestReadFrame_ReturnsStdout(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	frame := map[string]any{"type": "stdout", "data": "hello world\n"}
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	data = append(data, '\n')

	// Write in goroutine, read in main (net.Pipe is synchronous)
	go func() {
		_, wErr := server.Write(data)
		assert.NoError(t, wErr)
	}()

	frameType, frameData, err := vsock.ReadFrame(client)
	require.NoError(t, err)
	assert.Equal(t, "stdout", frameType)
	assert.Equal(t, "hello world\n", string(frameData))
}

func TestReadFrame_ReturnsStderr(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	frame := map[string]any{"type": "stderr", "data": "error message"}
	go func() {
		assert.NoError(t, json.NewEncoder(server).Encode(frame))
	}()

	frameType, frameData, err := vsock.ReadFrame(client)
	require.NoError(t, err)
	assert.Equal(t, "stderr", frameType)
	assert.Equal(t, "error message", string(frameData))
}

func TestReadFrame_ReturnsResult(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	frame := map[string]any{"type": "result", "status": 0}
	go func() {
		assert.NoError(t, json.NewEncoder(server).Encode(frame))
	}()

	frameType, frameData, err := vsock.ReadFrame(client)
	require.NoError(t, err)
	assert.Equal(t, "result", frameType)
	assert.Empty(t, frameData, "result frames should have empty data")
}

func TestReadFrame_ClosedConnection(t *testing.T) {
	server, client := net.Pipe()
	server.Close()
	client.Close()

	_, _, err := vsock.ReadFrame(client)
	require.Error(t, err)
}

func TestReadFrame_ReturnsErrorOnMalformedJSON(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		_, wErr := server.Write([]byte("{invalid json}\n"))
		assert.NoError(t, wErr)
	}()

	_, _, err := vsock.ReadFrame(client)
	require.Error(t, err)
}

// --- DialVM (via mock UDS server) ---
// Rationale: DialVM must perform the CONNECT handshake and return an open
// connection on success. Tests use a mock UDS server.

func TestDialVM_Success(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "dialvm-test.sock")
	port := 1024

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		assert.Contains(t, line, "CONNECT")
		_, _ = fmt.Fprintf(conn, "OK %d\n", port)
	}()

	time.Sleep(5 * time.Millisecond)

	conn, err := vsock.DialVM(context.Background(), sockPath, port)
	require.NoError(t, err)
	assert.NotNil(t, conn)
	conn.Close()
}

func TestDialVM_HandshakeFailure(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "dialvm-bad.sock")
	port := 1024

	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
		// Send bad handshake response
		_, _ = fmt.Fprintf(conn, "ERR\n")
	}()

	time.Sleep(5 * time.Millisecond)

	_, err = vsock.DialVM(context.Background(), sockPath, port)
	require.Error(t, err)
}

func TestDialVM_NonExistentSocket(t *testing.T) {
	_, err := vsock.DialVM(context.Background(), "/nonexistent/vsock-test.sock", 1024)
	require.Error(t, err)
}

func TestDialVM_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := vsock.DialVM(ctx, "/nonexistent/vsock-test.sock", 1024)
	require.Error(t, err)
}
