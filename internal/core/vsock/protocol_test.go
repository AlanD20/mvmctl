package vsock_test

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
)

// Exported frame primitives use newline-delimited JSON on an already admitted connection.
// net.Pipe has no buffering, so each test reads or writes concurrently with its peer.

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
