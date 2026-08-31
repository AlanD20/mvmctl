package agent

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

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

	var value [1]byte
	_, err = connection.Read(value[:])

	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrDeadlineExceeded), "unexpected deadline error: %v", err)
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
	wait.Wait()
	close(errorsByCaller)
	for closeErr := range errorsByCaller {
		require.NoError(t, closeErr)
	}
}

func TestHandleConnectionCancellationUnblocksPartialProtocolPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(9999, destinationTestToken, "").handleLocalConnection(ctx, server)
	}()

	_, err := client.Write([]byte(`{"destination":"partial`))
	require.NoError(t, err)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial local request remained blocked after context cancellation")
	}
}
