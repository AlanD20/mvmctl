package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

func TestRemoteSubcommandRequiresExplicitNonRootUser(t *testing.T) {
	const wantUsage = "usage: mvm-agent remote --user <non-root> <destination> -- <command>\n"
	tests := map[string][]string{
		"missing user option": {"target", "--", "true"},
		"missing username":    {"--user"},
		"root user":           {"--user", "root", "target", "--", "true"},
		"missing separator":   {"--user", "alice", "target", "true"},
		"missing command":     {"--user", "alice", "target", "--"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			status := -1
			_, stderr := captureRemoteOutput(t, func() int {
				status = runRemoteSubcommand("/absent/agent.sock", args)
				return status
			})
			assert.Equal(t, 1, status)
			assert.Equal(t, wantUsage, stderr)
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
	assert.Empty(t, cmp.Diff(wantStdout, []byte(stdout)))
	assert.Empty(t, cmp.Diff(wantStderr, []byte(stderr)))

	request := <-requests
	assert.Len(t, request.RequestID, 32)
	assert.Empty(t, cmp.Diff(vmexecwire.SourceRequest{
		RequestID: request.RequestID, Destination: "target-vm", TargetUser: "alice", Command: "printf binary",
	}, request))
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
			status := -1
			_, stderr := captureRemoteOutput(t, func() int {
				status = runRemoteExchange(client, vmexecwire.SourceRequest{
					RequestID:   "00112233445566778899aabbccddeeff",
					Destination: "target",
					TargetUser:  "alice",
					Command:     "true",
				})
				return status
			})
			assert.Equal(t, 1, status)
			assert.Equal(t, "error: invalid source response\n", stderr)
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

func TestRemoteExchangeRedactsTerminalErrorAndRequiresEOF(t *testing.T) {
	terminalWritten := make(chan struct{})
	releaseEOF := make(chan struct{})
	var releaseEOFOnce sync.Once
	releaseServer := func() { releaseEOFOnce.Do(func() { close(releaseEOF) }) }
	t.Cleanup(releaseServer)
	client := startSourceDaemon(t, func(conn net.Conn) {
		request, err := vmexecwire.DecodeSourceRequest(context.Background(), conn)
		if err != nil {
			return
		}
		writer, err := vmexecwire.NewResponseWriter(conn, request.RequestID)
		if err == nil {
			_ = writer.WriteError(context.Background(), "echo command-secret-7c25")
			close(terminalWritten)
			<-releaseEOF
		}
	})

	finishCapture := beginRemoteOutputCapture(t)
	status := make(chan int, 1)
	go func() {
		status <- runRemoteExchange(client, vmexecwire.SourceRequest{
			RequestID:   "00112233445566778899aabbccddeeff",
			Destination: "target",
			TargetUser:  "alice",
			Command:     "echo command-secret-7c25",
		})
	}()
	select {
	case <-terminalWritten:
	case <-time.After(time.Second):
		t.Fatal("source daemon did not write its terminal error")
	}
	select {
	case earlyStatus := <-status:
		t.Fatalf("remote exchange returned %d before physical EOF", earlyStatus)
	default:
	}
	releaseServer()
	var gotStatus int
	select {
	case gotStatus = <-status:
	case <-time.After(time.Second):
		t.Fatal("remote exchange did not finish after physical EOF")
	}
	_, stderr := finishCapture()
	assert.Equal(t, 1, gotStatus)
	assert.Equal(t, "error: source execution failed\n", stderr)
	assert.NotContains(t, stderr, "command-secret-7c25")
}

func TestRunRemoteSubcommandUsesUnixBrokerContractAndDistinctRequestIDs(t *testing.T) {
	socketDir, err := os.MkdirTemp(os.TempDir(), "mvm-remote-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(socketDir)) })
	socketPath := filepath.Join(socketDir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("sandbox does not permit creating an AF_UNIX listener")
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan vmexecwire.SourceRequest, 2)
	serverDone := make(chan error, 1)
	go func() {
		for index := range 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverDone <- acceptErr
				return
			}
			request, decodeErr := vmexecwire.DecodeSourceRequest(context.Background(), conn)
			if decodeErr == nil {
				requests <- request
				var writer *vmexecwire.ResponseWriter
				writer, decodeErr = vmexecwire.NewResponseWriter(conn, request.RequestID)
				if decodeErr == nil && index == 0 {
					decodeErr = writer.WriteResult(context.Background(), 23, time.Millisecond)
				}
				if decodeErr == nil && index == 1 {
					decodeErr = writer.WriteError(context.Background(), "echo request-secret")
				}
			}
			closeErr := conn.Close()
			if decodeErr != nil {
				serverDone <- decodeErr
				return
			}
			if closeErr != nil {
				serverDone <- closeErr
				return
			}
		}
		serverDone <- nil
	}()

	firstStatus := -1
	_, firstStderr := captureRemoteOutput(t, func() int {
		firstStatus = runRemoteSubcommand(
			socketPath, []string{"--user", "alice", "target-one", "--", "printf", "first"},
		)
		return firstStatus
	})
	assert.Equal(t, 23, firstStatus)
	assert.Empty(t, firstStderr)
	secondStatus := -1
	_, secondStderr := captureRemoteOutput(t, func() int {
		secondStatus = runRemoteSubcommand(
			socketPath, []string{"--user", "bob", "target-two", "--", "printf", "second"},
		)
		return secondStatus
	})
	assert.Equal(t, 1, secondStatus)
	assert.Contains(t, secondStderr, "source execution failed")
	assert.NotContains(t, secondStderr, "request-secret")
	require.NoError(t, <-serverDone)

	first := <-requests
	second := <-requests
	assert.Empty(t, cmp.Diff(vmexecwire.SourceRequest{
		RequestID: first.RequestID, Destination: "target-one", TargetUser: "alice", Command: "printf first",
	}, first))
	assert.Empty(t, cmp.Diff(vmexecwire.SourceRequest{
		RequestID: second.RequestID, Destination: "target-two", TargetUser: "bob", Command: "printf second",
	}, second))
	assert.NotEqual(t, first.RequestID, second.RequestID)
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
	finish := beginRemoteOutputCapture(t)

	done := make(chan int, 1)
	go func() { done <- run() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("remote subcommand did not finish")
	}
	return finish()
}

func beginRemoteOutputCapture(t *testing.T) func() (string, string) {
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

	return func() (string, string) {
		t.Helper()
		require.NoError(t, stdoutWriter.Close())
		require.NoError(t, stderrWriter.Close())
		stdout, readErr := io.ReadAll(stdoutReader)
		require.NoError(t, readErr)
		stderr, readErr := io.ReadAll(stderrReader)
		require.NoError(t, readErr)
		require.NoError(t, stdoutReader.Close())
		require.NoError(t, stderrReader.Close())
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		return string(stdout), string(stderr)
	}
}
