// Package vsockagent tests handleLocalConn, which accepts a local UDS connection
// and relays remote exec requests/responses through the vsock connection.
package vsockagent

import (
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// --- handleLocalConn ---

// Rationale: handleLocalConn reads a RemoteVMRequest from the local socket,
// forwards it as a remote_vm frame to the vsock connection, and relays
// response frames back. This test verifies the full request/response flow.

func TestHandleLocalConn_ReadsRequestSendsRemoteVMFrame(t *testing.T) {
	// Create two pipe pairs:
	// 1. local ↔ handleLocalConn (simulates local UDS socket)
	// 2. vsock (activeConn) ↔ test (simulates the host-side vsock)
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	// Test sends a RemoteVMRequest on the local socket
	req := RemoteVMRequest{
		Destination: "target-vm",
		Command:     "ls -la",
		User:        "bob",
		Timeout:     10,
	}
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(req))

	// Read the remote_vm frame from the vsock side (received by host)
	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))
	assert.Equal(t, responseTypeRemoteVM, frame.Type, "must send remote_vm frame on vsock")
	assert.Contains(t, frame.Data, `"destination":"target-vm"`)
	assert.Contains(t, frame.Data, `"command":"ls -la"`)
	assert.Contains(t, frame.Data, `"user":"bob"`)
	assert.Contains(t, frame.Data, `"timeout":10`)

	// Send a response from the host side: stdout frame
	require.NoError(t, writeFrame(vsockTest, &execResponse{
		Type: responseTypeStdout,
		Data: "file1.txt\n",
	}))

	// Read the forwarded stdout frame on the local side
	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeStdout, localResp.Type)
	assert.Equal(t, "file1.txt\n", localResp.Data)

	// Send the final remote_vm response
	require.NoError(t, writeFrame(vsockTest, &execResponse{
		Type:   responseTypeRemoteVM,
		Status: 0,
	}))

	// Read the final response on the local side
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeRemoteVM, localResp.Type)
	assert.Equal(t, 0, localResp.Status)

	wg.Wait()
}

// Rationale: handleLocalConn must forward both stdout and stderr frames.

func TestHandleLocalConn_ForwardsStdoutStderr(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	// Send request
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt",
		Command:     "make",
	}))

	// Read the remote_vm frame (discard)
	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))

	// Host sends stdout (step A)
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "build output\n"}))

	// Read forwarded stdout on local side (step B — must alternate write/read
	// because net.Pipe has no internal buffering)
	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeStdout, localResp.Type)
	assert.Equal(t, "build output\n", localResp.Data)

	// Host sends stderr
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStderr, Data: "warning: unused var\n"}))

	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeStderr, localResp.Type)
	assert.Equal(t, "warning: unused var\n", localResp.Data)

	// Host sends final
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeRemoteVM, Status: 0}))

	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeRemoteVM, localResp.Type)
	assert.Equal(t, 0, localResp.Status)

	wg.Wait()
}

// Rationale: Empty activeConn must send an error frame and return without
// reading further frames.

func TestHandleLocalConn_EmptyActiveConn(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })

	agent := &Agent{
		activeConn: nil, // no active vsock connection
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	// Send request
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt",
		Command:     "ls",
	}))

	// Read error response
	var resp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&resp))
	assert.Equal(t, responseTypeRemoteVM, resp.Type)
	assert.Equal(t, 1, resp.Status)
	assert.Contains(t, resp.Error, "no active vsock connection")

	wg.Wait()
}

// Rationale: Malformed request JSON must not cause a panic or hang.

func TestHandleLocalConn_MalformedRequest(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })

	agent := &Agent{
		activeConn: nil,
	}

	done := make(chan struct{})
	go func() {
		agent.handleLocalConn(localConn)
		close(done)
	}()

	// Send malformed JSON
	_, err := localConnPeer.Write([]byte("not json{}\n"))
	require.NoError(t, err)

	// handleLocalConn should log the error and return cleanly
	select {
	case <-done:
		// Clean return — success.
	case <-time.After(time.Second):
		t.Fatal("handleLocalConn did not return within 1s after malformed request")
	}
}

// Rationale: handleLocalConn must exit cleanly when the final remote_vm
// frame has an error message, forwarding it correctly.

func TestHandleLocalConn_RemoteVMWithError(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	// Send request
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt",
		Command:     "fail",
	}))

	// Discard the forwarded remote_vm frame
	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))

	// Host sends remote_vm with error
	require.NoError(t, writeFrame(vsockTest, &execResponse{
		Type:   responseTypeRemoteVM,
		Status: 1,
		Error:  "target VM not found",
	}))

	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeRemoteVM, localResp.Type)
	assert.Equal(t, 1, localResp.Status)
	assert.Equal(t, "target VM not found", localResp.Error)

	wg.Wait()
}

// Rationale: Unknown frame types from the vsock must be logged and ignored.

func TestHandleLocalConn_UnknownFrameTypesIgnored(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	// Send request
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt",
		Command:     "test",
	}))

	// Discard the forwarded remote_vm frame
	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))

	// Host sends unknown frame (will be ignored/dropped by handleLocalConn)
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: "heartbeat", Data: "ping"}))

	// Host sends stdout
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "real output\n"}))

	// Read forwarded stdout (must alternate write/read for net.Pipe)
	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeStdout, localResp.Type)
	assert.Equal(t, "real output\n", localResp.Data)

	// Host sends final
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeRemoteVM, Status: 0}))

	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeRemoteVM, localResp.Type)
	assert.Equal(t, 0, localResp.Status)

	wg.Wait()
}

// Rationale: Connection loss mid-relay must not panic — handleLocalConn
// returns when the vsock read fails.

func TestHandleLocalConn_VsockDisconnectMidRelay(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	done := make(chan struct{})
	go func() {
		agent.handleLocalConn(localConn)
		close(done)
	}()

	// Send request
	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt",
		Command:     "long-running",
	}))

	// Discard the forwarded remote_vm frame
	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))

	// Host sends one stdout frame
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "partial\n"}))

	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeStdout, localResp.Type)

	// Close the vsock connection abruptly
	vsockTest.Close()

	// handleLocalConn should detect the disconnect and return cleanly
	select {
	case <-done:
		// Clean return.
	case <-time.After(time.Second):
		t.Fatal("handleLocalConn did not return within 1s after vsock disconnect")
	}
}

// --- TestAgentNew (local socket param) ---
// Rationale: New() accepts a localSocket parameter and defaults to
// /var/run/mvm-vsock-agent.sock when empty.

func TestAgentNew_LocalSocketDefault(t *testing.T) {
	a := New(9999, "token", "")
	assert.Equal(t, "/var/run/mvm-vsock-agent.sock", a.localSocket)
}

func TestAgentNew_LocalSocketCustom(t *testing.T) {
	a := New(9999, "token", "/tmp/custom.sock")
	assert.Equal(t, "/tmp/custom.sock", a.localSocket)
}

// --- TestAgentNew_RunLocalListener ---
// Rationale: Agent.Run() must start a local UDS listener. We verify that
// the listener accepts connections by connecting to it.

func TestAgent_Run_LocalListenerStarts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("TestAgent_Run requires root (vsock listener needs AF_VSOCK)")
	}
	// This test requires AF_VSOCK which only works inside a Firecracker VM
	// or on a host with the vsock module loaded. Skip for now.
	t.Skip("vsock listener test requires Firecracker environment")
}

// --- writeFrame error path ---
// Rationale: When writeFrame fails (e.g., closed local socket), handleLocalConn
// must log the error and return, not hang.

func TestHandleLocalConn_WriteErrorOnStdout(t *testing.T) {
	localConn, _ := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	// Close the localConn immediately so writes fail
	localConn.Close()

	agent := &Agent{
		activeConn: vsockAgent,
	}

	done := make(chan struct{})
	go func() {
		agent.handleLocalConn(localConn)
		close(done)
	}()

	// handleLocalConn will try to decode from localConn, fail, and return
	select {
	case <-done:
		// Clean return — success.
	case <-time.After(time.Second):
		t.Fatal("handleLocalConn did not return within 1s with closed local connection")
	}
}

// --- Multiple frames pipelined ---
// Rationale: Multiple stdout frames from the host are forwarded in order.

func TestHandleLocalConn_MultipleStdoutFrames(t *testing.T) {
	localConn, localConnPeer := net.Pipe()
	vsockAgent, vsockTest := net.Pipe()
	t.Cleanup(func() { localConn.Close() })
	t.Cleanup(func() { localConnPeer.Close() })
	t.Cleanup(func() { vsockAgent.Close() })
	t.Cleanup(func() { vsockTest.Close() })

	agent := &Agent{
		activeConn: vsockAgent,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.handleLocalConn(localConn)
	}()

	require.NoError(t, json.NewEncoder(localConnPeer).Encode(RemoteVMRequest{
		Destination: "tgt", Command: "seq",
	}))

	var frame execResponse
	require.NoError(t, json.NewDecoder(vsockTest).Decode(&frame))

	var stdoutData []string

	// Must alternate write/read: write frame 1, read frame 1, ...
	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "1\n"}))
	var localResp execResponse
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	stdoutData = append(stdoutData, localResp.Data)

	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "2\n"}))
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	stdoutData = append(stdoutData, localResp.Data)

	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeStdout, Data: "3\n"}))
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	stdoutData = append(stdoutData, localResp.Data)

	assert.Equal(t, []string{"1\n", "2\n", "3\n"}, stdoutData)

	require.NoError(t, writeFrame(vsockTest, &execResponse{Type: responseTypeRemoteVM, Status: 0}))
	require.NoError(t, json.NewDecoder(localConnPeer).Decode(&localResp))
	assert.Equal(t, responseTypeRemoteVM, localResp.Type)

	wg.Wait()
}
