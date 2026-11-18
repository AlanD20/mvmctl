package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolwire"
)

func TestParseControlRequestRequiresExactTypedOperationAndGeneration(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{
		{"floating-ip", "inspect", id},
		{"floating-ip", "inspect", "--generation", "0", id},
		{"floating-ip", "associate", "--generation", "1", id},
		{"floating-ip", "renew", "--generation", "18446744073709551615", id},
		{"floating-ip", "release", "--generation", "9", id},
	} {
		request, err := parseControlRequest(args)
		require.NoError(t, err)
		require.Equal(t, id, request.Input.ResourceID)
	}
	for _, args := range [][]string{
		nil, {"floating-ip"}, {"exec", "inspect", id}, {"floating-ip", "run", id},
		{"floating-ip", "associate", id}, {"floating-ip", "release", "--generation", "0", id},
		{"floating-ip", "renew", "--generation", "18446744073709551616", id},
		{"floating-ip", "renew", "--generation", "-1", id},
		{"floating-ip", "renew", "--generation", "01", id},
		{"floating-ip", "inspect", "--host-uid", "0", id},
		{"floating-ip", "inspect", strings.Repeat("A", 32)},
		{"floating-ip", "inspect", id, "extra"},
	} {
		_, err := parseControlRequest(args)
		require.Error(t, err)
	}
}

func TestControlExchangeEmitsOnlyCompactTypedResult(t *testing.T) {
	request, err := parseControlRequest([]string{"floating-ip", "inspect", strings.Repeat("a", 32)})
	require.NoError(t, err)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: "@mvm-agent-control-" + uuid.NewString(), Net: "unix"})
	require.NoError(t, err)
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		forwarded, err := guestcontrolwire.DecodeRequest(ctx, conn)
		if err == nil {
			response := guestcontrolwire.Response{RequestID: forwarded.RequestID, Operation: forwarded.Operation,
				Result: &guestcontrolwire.FloatingIPResult{
					ResourceID: forwarded.Input.ResourceID, IPv4: "10.0.0.100", Generation: 7,
					Ownership: guestcontrolwire.Unassociated}}
			err = guestcontrolwire.EncodeResponse(ctx, conn, forwarded, response)
		}
		done <- err
	}()
	conn, err := net.Dial("unix", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	var output bytes.Buffer
	require.NoError(t, runControlExchange(ctx, conn, request, &output))
	require.NoError(t, <-done)
	require.JSONEq(t, `{"resource_id":"`+strings.Repeat("a", 32)+`","ipv4":"10.0.0.100",`+
		`"generation":7,"ownership":"unassociated","lease_remaining_ms":0}`, output.String())
	require.NotContains(t, output.String(), "request_id")
}

func TestControlExchangeCancellationInterruptsPartialResponse(t *testing.T) {
	request, err := parseControlRequest([]string{"floating-ip", "inspect", strings.Repeat("a", 32)})
	require.NoError(t, err)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: "@mvm-agent-control-" + uuid.NewString(), Net: "unix"})
	require.NoError(t, err)
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	received := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := guestcontrolwire.DecodeRequest(ctx, conn); err != nil {
			return
		}
		_, _ = conn.Write([]byte("MVM"))
		close(received)
		var remaining [1]byte
		_, _ = conn.Read(remaining[:])
	}()
	conn, err := net.Dial("unix", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	exchange, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { var output bytes.Buffer; result <- runControlExchange(exchange, conn, request, &output) }()
	<-received
	stop()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancelled control exchange remained blocked")
	}
	<-serverDone
}
