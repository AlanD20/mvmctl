package vsock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestConnectGuestHandshake(t *testing.T) {
	// The root broker owns transport admission; this test owns only the guest CONNECT exchange.
	t.Run("accepts dynamic host port and leaves connection open", func(t *testing.T) {
		host, guest := net.Pipe()
		t.Cleanup(func() { _ = host.Close() })
		t.Cleanup(func() { _ = guest.Close() })
		require.NoError(t, guest.SetDeadline(time.Now().Add(5*time.Second)))
		peerDone := make(chan error, 1)
		go func() {
			line, err := bufio.NewReader(guest).ReadString('\n')
			if err == nil && line != "CONNECT 1024\n" {
				err = fmt.Errorf("unexpected CONNECT request %q", line)
			}
			if err == nil {
				_, err = io.WriteString(guest, "OK 1073741824\nY")
			}
			if err == nil {
				var probe [1]byte
				_, err = io.ReadFull(guest, probe[:])
				if err == nil && probe[0] != 'x' {
					err = fmt.Errorf("unexpected post-handshake byte %q", probe[0])
				}
			}
			peerDone <- err
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, err := connectGuest(ctx, host, "00112233445566778899aabbccddeeff", 1024, 1)
		require.NoError(t, err)
		require.NotNil(t, conn)
		require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
		var immediate [1]byte
		_, err = io.ReadFull(conn, immediate[:])
		require.NoError(t, err)
		assert.Equal(t, byte('Y'), immediate[0])
		_, err = conn.Write([]byte{'x'})
		require.NoError(t, err)
		assert.NoError(t, <-peerDone)
	})

	for _, tc := range []struct {
		name     string
		response string
	}{
		{name: "error", response: "ERR\n"},
		{name: "missing host port", response: "OK \n"},
		{name: "nonnumeric host port", response: "OK nonsense\n"},
		{name: "out of range host port", response: "OK 4294967296\n"},
		{name: "signed host port", response: "OK +1\n"},
		{name: "overlong response", response: "OK 12345678901"},
	} {
		t.Run("rejects "+tc.name+" and closes transport", func(t *testing.T) {
			host, guest := net.Pipe()
			t.Cleanup(func() { _ = host.Close() })
			t.Cleanup(func() { _ = guest.Close() })
			require.NoError(t, guest.SetDeadline(time.Now().Add(5*time.Second)))
			peerDone := make(chan error, 1)
			go func() {
				line, err := bufio.NewReader(guest).ReadString('\n')
				if err == nil && line != "CONNECT 1024\n" {
					err = fmt.Errorf("unexpected CONNECT request %q", line)
				}
				if err == nil {
					_, err = io.WriteString(guest, tc.response)
				}
				if err == nil {
					var data [1]byte
					_, err = guest.Read(data[:])
					if errors.Is(err, io.EOF) {
						err = nil
					}
				}
				peerDone <- err
			}()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := connectGuest(ctx, host, "00112233445566778899aabbccddeeff", 1024, 1)
			require.Error(t, err)
			assert.Nil(t, conn)
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			assert.Equal(t, errs.CodeVsockHandshakeFailed, domainErr.Code)
			assert.NoError(t, <-peerDone)
		})
	}

	t.Run("cancellation closes a silent transport", func(t *testing.T) {
		host, guest := net.Pipe()
		t.Cleanup(func() { _ = host.Close() })
		t.Cleanup(func() { _ = guest.Close() })
		require.NoError(t, guest.SetDeadline(time.Now().Add(5*time.Second)))
		ready := make(chan struct{})
		peerDone := make(chan error, 1)
		go func() {
			line, err := bufio.NewReader(guest).ReadString('\n')
			if err == nil && line != "CONNECT 1024\n" {
				err = fmt.Errorf("unexpected CONNECT request %q", line)
			}
			close(ready)
			if err == nil {
				var data [1]byte
				_, err = guest.Read(data[:])
				if errors.Is(err, io.EOF) {
					err = nil
				}
			}
			peerDone <- err
		}()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			conn net.Conn
			err  error
		}
		connected := make(chan result, 1)
		go func() {
			conn, err := connectGuest(ctx, host, "00112233445566778899aabbccddeeff", 1024, 1)
			connected <- result{conn: conn, err: err}
		}()
		<-ready
		cancel()
		got := <-connected
		require.Error(t, got.err)
		assert.Nil(t, got.conn)
		assert.ErrorIs(t, got.err, context.Canceled)
		assert.NoError(t, <-peerDone)
	})
}
