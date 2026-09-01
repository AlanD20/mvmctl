package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

func TestRemoteSubcommandRequiresExplicitNonRootUser(t *testing.T) {
	tests := map[string][]string{
		"missing user option": {"target", "--", "true"},
		"missing username":    {"--user"},
		"root user":           {"--user", "root", "target", "--", "true"},
		"missing separator":   {"--user", "alice", "target", "true"},
		"missing command":     {"--user", "alice", "target", "--"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, stderr := captureRemoteOutput(t, func() int {
				return runRemoteSubcommand("/absent/agent.sock", args)
			})
			assert.NotEmpty(t, stderr)
		})
	}
}

func TestRemoteSubcommandUsesVMExecWireAndPreservesBinaryOutput(t *testing.T) {
	wantStdout := []byte{0x00, 0xff, 'a', '\n'}
	wantStderr := []byte{0x01, 0xfe, 'b', '\n'}
	requests := make(chan vmexecwire.SourceRequest, 1)
	client := startSourceDaemon(t, func(conn net.Conn) {
		request, err := vmexecwire.DecodeSourceRequest(context.Background(), conn)
		if err != nil {
			return
		}
		requests <- request
		writer, err := vmexecwire.NewResponseWriter(conn, request.RequestID)
		if err != nil {
			return
		}
		_ = writer.WriteStdout(context.Background(), wantStdout)
		_ = writer.WriteStderr(context.Background(), wantStderr)
		_ = writer.WriteResult(context.Background(), 42, 10*time.Millisecond)
	})

	var exitCode int
	stdout, stderr := captureRemoteOutput(t, func() int {
		exitCode = runRemoteExchange(client, vmexecwire.SourceRequest{
			RequestID:   "00112233445566778899aabbccddeeff",
			Destination: "target-vm",
			TargetUser:  "alice",
			Command:     "printf binary",
		})
		return exitCode
	})
	assert.Equal(t, 42, exitCode)
	assert.Equal(t, wantStdout, []byte(stdout))
	assert.Equal(t, wantStderr, []byte(stderr))

	request := <-requests
	assert.Len(t, request.RequestID, 32)
	assert.Equal(t, "target-vm", request.Destination)
	assert.Equal(t, "alice", request.TargetUser)
	assert.Equal(t, "printf binary", request.Command)
}

func TestRemoteSubcommandRejectsPrematureTrailingAndMalformedResponses(t *testing.T) {
	tests := map[string]func(net.Conn){
		"premature EOF": func(conn net.Conn) {
			_, _ = vmexecwire.DecodeSourceRequest(context.Background(), conn)
		},
		"trailing bytes": func(conn net.Conn) {
			request, err := vmexecwire.DecodeSourceRequest(context.Background(), conn)
			if err != nil {
				return
			}
			writer, err := vmexecwire.NewResponseWriter(conn, request.RequestID)
			if err != nil {
				return
			}
			_ = writer.WriteResult(context.Background(), 0, 0)
			_, _ = conn.Write([]byte{0x01})
		},
		"malformed frame": func(conn net.Conn) {
			_, _ = vmexecwire.DecodeSourceRequest(context.Background(), conn)
			prefix := make([]byte, len(vmexecwire.FrameMagic)+2+4)
			copy(prefix, vmexecwire.FrameMagic)
			binary.BigEndian.PutUint16(prefix[len(vmexecwire.FrameMagic):], 1)
			binary.BigEndian.PutUint32(prefix[len(vmexecwire.FrameMagic)+2:], 1)
			_, _ = conn.Write(append(prefix, '{'))
		},
	}

	for name, respond := range tests {
		t.Run(name, func(t *testing.T) {
			client := startSourceDaemon(t, respond)
			_, stderr := captureRemoteOutput(t, func() int {
				return runRemoteExchange(client, vmexecwire.SourceRequest{
					RequestID:   "00112233445566778899aabbccddeeff",
					Destination: "target",
					TargetUser:  "alice",
					Command:     "true",
				})
			})
			assert.Contains(t, stderr, "invalid source response")
		})
	}
}

func TestRemoteSubcommandRedactsRequestDataFromFailures(t *testing.T) {
	const secretCommand = "echo command-secret-7c25"
	requests := make(chan vmexecwire.SourceRequest, 1)
	client := startSourceDaemon(t, func(conn net.Conn) {
		request, err := vmexecwire.DecodeSourceRequest(context.Background(), conn)
		if err != nil {
			return
		}
		requests <- request
	})

	_, stderr := captureRemoteOutput(t, func() int {
		return runRemoteExchange(client, vmexecwire.SourceRequest{
			RequestID:   "00112233445566778899aabbccddeeff",
			Destination: "target",
			TargetUser:  "alice",
			Command:     secretCommand,
		})
	})
	request := <-requests
	assert.NotContains(t, stderr, secretCommand)
	assert.NotContains(t, stderr, request.RequestID)
	assert.NotContains(t, stderr, "source_session_id")
}

func startSourceDaemon(t *testing.T, respond func(net.Conn)) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		defer server.Close()
		respond(server)
	}()
	return client
}

func captureRemoteOutput(t *testing.T, run func() int) (string, string) {
	t.Helper()
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)
	stderrReader, stderrWriter, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	})

	done := make(chan int, 1)
	go func() { done <- run() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("remote subcommand did not finish")
	}
	require.NoError(t, stdoutWriter.Close())
	require.NoError(t, stderrWriter.Close())
	stdout, err := io.ReadAll(stdoutReader)
	require.NoError(t, err)
	stderr, err := io.ReadAll(stderrReader)
	require.NoError(t, err)
	require.NoError(t, stdoutReader.Close())
	require.NoError(t, stderrReader.Close())
	os.Stdout = originalStdout
	os.Stderr = originalStderr
	return string(stdout), string(stderr)
}
