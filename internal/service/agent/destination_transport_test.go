package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecwire"
)

type destinationDeadlineRecordingConn struct {
	net.Conn
	mu            sync.Mutex
	writeDeadline time.Time
}

func (conn *destinationDeadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	conn.mu.Lock()
	conn.writeDeadline = deadline
	conn.mu.Unlock()
	return conn.Conn.SetWriteDeadline(deadline)
}

func (conn *destinationDeadlineRecordingConn) recordedWriteDeadline() time.Time {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.writeDeadline
}

func TestVSockConnectionReadDeadlineInterruptsBlockedIO(t *testing.T) {
	fds, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK,
		0,
	)
	require.NoError(t, err)
	connection := newVSockConn(fds[0], 2, 1024)
	peer := os.NewFile(uintptr(fds[1]), "vsock-test-peer")
	t.Cleanup(func() {
		_ = connection.Close()
		_ = peer.Close()
	})
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(25*time.Millisecond)))

	readResult := make(chan error, 1)
	go func() {
		var value [1]byte
		_, readErr := connection.Read(value[:])
		readResult <- readErr
	}()
	select {
	case err = <-readResult:
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrDeadlineExceeded), "unexpected deadline error: %v", err)
	case <-time.After(time.Second):
		_ = connection.Close()
		t.Fatal("vsock read remained blocked after its deadline")
	}
}

func TestDestinationUnauthorizedResponseUsesTerminalWriteDeadline(t *testing.T) {
	client, rawServer := net.Pipe()
	server := &destinationDeadlineRecordingConn{Conn: rawServer}
	require.NoError(t, client.SetDeadline(time.Now().Add(destinationTestTimeout)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(9999, destinationTestToken, "").handleConnection(t.Context(), server)
	}()

	startedAt := time.Now()
	request := vmexecwire.DestinationRequest{
		RequestID:  destinationTestRequestID,
		AgentToken: "fedcba98-7654-4cba-8765-43210fedcba9",
		TargetUser: "builder",
		Command:    "printf secret",
	}
	require.NoError(t, vmexecwire.EncodeDestinationRequest(t.Context(), client, request))
	reader, err := vmexecwire.NewResponseReader(client, destinationTestRequestID)
	require.NoError(t, err)
	frame, err := reader.Read(t.Context())
	require.NoError(t, err)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationUnauthorizedMessage, terminal.Message)
	_, err = reader.Read(t.Context())
	require.ErrorIs(t, err, io.EOF)
	waitForDestinationHandler(t, done)

	deadline := server.recordedWriteDeadline()
	require.False(t, deadline.IsZero())
	assert.GreaterOrEqual(t, deadline.Sub(startedAt), destinationTerminalWriteTimeout-time.Second)
	assert.LessOrEqual(t, deadline.Sub(startedAt), destinationTerminalWriteTimeout+time.Second)
}

func TestVSockConnectionCloseIsConcurrentAndIdempotent(t *testing.T) {
	fds, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK,
		0,
	)
	require.NoError(t, err)
	connection := newVSockConn(fds[0], 2, 1024)
	peer := os.NewFile(uintptr(fds[1]), "vsock-test-peer")
	t.Cleanup(func() { _ = peer.Close() })

	var wait sync.WaitGroup
	errorsByCaller := make(chan error, 8)
	for range 8 {
		wait.Go(func() { errorsByCaller <- connection.Close() })
	}
	waitDone := make(chan struct{})
	go func() {
		wait.Wait()
		close(errorsByCaller)
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		_ = peer.Close()
		t.Fatal("concurrent vsock close calls did not complete")
	}
	for closeErr := range errorsByCaller {
		require.NoError(t, closeErr)
	}
}

func TestHandleConnectionCancellationUnblocksPartialProtocolPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(9999, destinationTestToken, "").handleConnection(ctx, server)
	}()

	_, err := client.Write([]byte("MVM"))
	require.NoError(t, err)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial protocol prefix remained blocked after context cancellation")
	}
}

func TestHandleLocalConnectionCancellationUnblocksPartialRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(9999, destinationTestToken, "").handleLocalConnection(ctx, server)
	}()

	_, err := client.Write([]byte("MVMEXE"))
	require.NoError(t, err)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial local request remained blocked after context cancellation")
	}
}
