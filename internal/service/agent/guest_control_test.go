package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
)

type controlGatewayFixture struct {
	gateway *GuestControlGateway
	host    guestcontrolauth.RootHostMaterial
	local   string
}

func newControlGatewayFixture(t *testing.T) controlGatewayFixture {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root ownership and Unix credentials require the approved root QA environment")
	}
	host, agent, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	require.NoError(t, os.Chown(directory, 0, 0))
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	local, err := listenGuestControlLocalAt(fd)
	require.NoError(t, err)
	gateway, err := NewGuestControlGateway(listener, local, agent)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- gateway.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done); require.NoError(t, unix.Close(fd)) })
	return controlGatewayFixture{
		gateway: gateway, host: host,
		local: fmt.Sprintf("/proc/self/fd/%d/%s", fd, infra.GuestControlLocalSocketFilename),
	}
}
func (fixture controlGatewayFixture) connect(t *testing.T) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", fixture.gateway.host.Addr().String())
	require.NoError(t, err)
	secured, err := guestcontrolauth.AuthenticateAgent(ctx, conn, fixture.host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secured.Close() })
	require.Eventually(t, func() bool {
		fixture.gateway.mu.Lock()
		defer fixture.gateway.mu.Unlock()
		return fixture.gateway.session != nil && !fixture.gateway.session.closed()
	}, time.Second, time.Millisecond)
	return secured
}
func controlTestRequest(operation guestcontrolwire.Operation) guestcontrolwire.Request {
	return guestcontrolwire.Request{RequestID: uuid.NewString(), Operation: operation,
		Input: guestcontrolwire.FloatingIPInput{ResourceID: strings.Repeat("a", 32), ExpectedGeneration: 8}}
}
func controlTestResponse(request guestcontrolwire.Request) guestcontrolwire.Response {
	result := &guestcontrolwire.FloatingIPResult{ResourceID: request.Input.ResourceID, IPv4: "10.0.0.100",
		Generation: request.Input.ExpectedGeneration, Ownership: guestcontrolwire.Unassociated}
	if request.Operation == guestcontrolwire.Associate || request.Operation == guestcontrolwire.Release {
		result.Generation++
	}
	if request.Operation == guestcontrolwire.Associate || request.Operation == guestcontrolwire.Renew {
		result.Ownership = guestcontrolwire.OwnedBySelf
		result.LeaseRemainingMillis = 5000
	}
	return guestcontrolwire.Response{RequestID: request.RequestID, Operation: request.Operation, Result: result}
}
func controlLocalExchange(
	ctx context.Context, path string, request guestcontrolwire.Request,
) (guestcontrolwire.Response, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return guestcontrolwire.Response{}, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if err := guestcontrolwire.EncodeRequest(ctx, conn, request); err != nil {
		return guestcontrolwire.Response{}, err
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return guestcontrolwire.Response{}, err
	}
	response, err := guestcontrolwire.DecodeResponse(ctx, conn, request)
	if err != nil {
		return response, err
	}
	var trailing [1]byte
	if n, err := conn.Read(trailing[:]); n != 0 || !errors.Is(err, io.EOF) {
		return response, errors.New("missing physical response EOF")
	}
	return response, nil
}

func TestGuestControlGatewayAuthenticatedConcurrentRequestsAndPrivateSocket(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	info, err := os.Stat(fixture.local)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	host := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	requests := []guestcontrolwire.Request{
		controlTestRequest(guestcontrolwire.Inspect), controlTestRequest(guestcontrolwire.Renew),
	}
	served := make(chan []string, 1)
	serverErrors := make(chan error, 1)
	go func() {
		var ids []string
		for range requests {
			request, err := guestcontrolwire.DecodeRequest(ctx, host)
			if err != nil {
				serverErrors <- err
				return
			}
			ids = append(ids, request.RequestID)
			if err := guestcontrolwire.EncodeResponse(ctx, host, request, controlTestResponse(request)); err != nil {
				serverErrors <- err
				return
			}
		}
		served <- ids
	}()
	var clients sync.WaitGroup
	failures := make(chan error, 2)
	for _, request := range requests {
		clients.Go(func() {
			response, err := controlLocalExchange(ctx, fixture.local, request)
			if err == nil && (response.Result == nil || response.RequestID != request.RequestID) {
				err = errors.New("uncorrelated result")
			}
			failures <- err
		})
	}
	clients.Wait()
	require.NoError(t, <-failures)
	require.NoError(t, <-failures)
	select {
	case ids := <-served:
		require.NotEqual(t, ids[0], ids[1])
		for _, id := range ids {
			for _, request := range requests {
				require.NotEqual(t, request.RequestID, id)
			}
		}
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("host worker did not finish")
	}
}

func TestGuestControlGatewayIdleEOFAllowsReconnectAndRejectsSecondActiveHost(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	first := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	second, err := net.Dial("tcp", fixture.gateway.host.Addr().String())
	require.NoError(t, err)
	_, err = guestcontrolauth.AuthenticateAgent(ctx, second, fixture.host)
	require.Error(t, err)
	_ = first.Close()
	require.Eventually(t, func() bool {
		fixture.gateway.mu.Lock()
		defer fixture.gateway.mu.Unlock()
		return fixture.gateway.session == nil
	}, time.Second, time.Millisecond)
	fresh := fixture.connect(t)
	request := controlTestRequest(guestcontrolwire.Associate)
	done := make(chan error, 1)
	go func() {
		forwarded, err := guestcontrolwire.DecodeRequest(ctx, fresh)
		if err == nil {
			err = guestcontrolwire.EncodeResponse(ctx, fresh, forwarded, controlTestResponse(forwarded))
		}
		done <- err
	}()
	response, err := controlLocalExchange(ctx, fixture.local, request)
	require.NoError(t, err)
	require.NotNil(t, response.Result)
	require.NoError(t, <-done)
}

func TestGuestControlGatewayCancellationPoisonsOldStreamAndLateResponse(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	host := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client, err := net.Dial("unix", fixture.local)
	require.NoError(t, err)
	request := controlTestRequest(guestcontrolwire.Associate)
	require.NoError(t, guestcontrolwire.EncodeRequest(ctx, client, request))
	require.NoError(t, client.(*net.UnixConn).CloseWrite())
	forwarded, err := guestcontrolwire.DecodeRequest(ctx, host)
	require.NoError(t, err)
	require.NoError(t, client.Close())
	require.Eventually(t, func() bool {
		fixture.gateway.mu.Lock()
		defer fixture.gateway.mu.Unlock()
		return fixture.gateway.session == nil
	}, time.Second, time.Millisecond)
	_ = host.SetDeadline(time.Now().Add(time.Second))
	_ = guestcontrolwire.EncodeResponse(ctx, host, forwarded, controlTestResponse(forwarded))
	fresh := fixture.connect(t)
	done := make(chan error, 1)
	go func() {
		next, err := guestcontrolwire.DecodeRequest(ctx, fresh)
		if err == nil && next.RequestID == forwarded.RequestID {
			err = errors.New("old request ID reused")
		}
		if err == nil {
			err = guestcontrolwire.EncodeResponse(ctx, fresh, next, controlTestResponse(next))
		}
		done <- err
	}()
	result, err := controlLocalExchange(ctx, fixture.local, request)
	require.NoError(t, err)
	require.NotNil(t, result.Result)
	require.NoError(t, <-done)
}

func TestGuestControlGatewayRefusesWrongTLSAndTrailingLocalInput(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	wrong, _, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	raw, err := net.Dial("tcp", fixture.gateway.host.Addr().String())
	require.NoError(t, err)
	_, err = guestcontrolauth.AuthenticateAgent(ctx, raw, wrong)
	require.Error(t, err)
	require.Eventually(t, func() bool {
		fixture.gateway.mu.Lock()
		defer fixture.gateway.mu.Unlock()
		return !fixture.gateway.connecting
	}, time.Second, time.Millisecond)
	host := fixture.connect(t)
	request := controlTestRequest(guestcontrolwire.Inspect)
	local, err := net.Dial("unix", fixture.local)
	require.NoError(t, err)
	require.NoError(t, guestcontrolwire.EncodeRequest(ctx, local, request))
	_, err = local.Write([]byte("trailing"))
	require.NoError(t, err)
	_ = local.(*net.UnixConn).CloseWrite()
	_ = local.SetDeadline(time.Now().Add(time.Second))
	_, err = guestcontrolwire.DecodeResponse(ctx, local, request)
	require.Error(t, err)
	_ = local.Close()
	// The refused local frame never poisoned the still idle authenticated host session.
	done := make(chan error, 1)
	go func() {
		next, err := guestcontrolwire.DecodeRequest(ctx, host)
		if err == nil {
			err = guestcontrolwire.EncodeResponse(ctx, host, next, controlTestResponse(next))
		}
		done <- err
	}()
	response, err := controlLocalExchange(ctx, fixture.local, request)
	require.NoError(t, err)
	require.NotNil(t, response.Result)
	require.NoError(t, <-done)
}

func TestGuestControlGatewayMismatchedResponsePoisonsSession(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	host := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		request, err := guestcontrolwire.DecodeRequest(ctx, host)
		if err == nil {
			request.RequestID = uuid.NewString()
			err = guestcontrolwire.EncodeResponse(ctx, host, request, controlTestResponse(request))
		}
		done <- err
	}()
	result, err := controlLocalExchange(ctx, fixture.local, controlTestRequest(guestcontrolwire.Associate))
	require.NoError(t, err)
	require.NotNil(t, result.Failure)
	require.Equal(t, guestcontrolwire.FailureOutcomeUnknown, result.Failure.Code)
	require.NoError(t, <-done)
	require.Eventually(t, func() bool {
		fixture.gateway.mu.Lock()
		defer fixture.gateway.mu.Unlock()
		return fixture.gateway.session == nil
	}, time.Second, time.Millisecond)
}

func TestGuestControlGatewayFailedLocalResponseWritePoisonsHostSession(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	host := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	fixture.gateway.mu.Lock()
	session := fixture.gateway.session
	fixture.gateway.mu.Unlock()
	require.NotNil(t, session)
	workerDone := make(chan error, 1)
	go func() {
		request, err := guestcontrolwire.DecodeRequest(ctx, host)
		if err == nil {
			err = guestcontrolwire.EncodeResponse(ctx, host, request, controlTestResponse(request))
		}
		workerDone <- err
	}()
	request := controlTestRequest(guestcontrolwire.Associate)
	response, err := session.exchange(ctx, request)
	require.NoError(t, err)
	require.NoError(t, <-workerDone)
	require.False(t, session.closed(), "the real host acknowledgement has already completed")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: "@mvm-control-write-" + uuid.NewString(), Net: "unix"})
	require.NoError(t, err)
	defer listener.Close()
	client, err := net.Dial("unix", listener.Addr().String())
	require.NoError(t, err)
	defer client.Close()
	local, err := listener.AcceptUnix()
	require.NoError(t, err)
	defer local.Close()
	// Fail only the actual Unix response write. The peer stays open; the request context is not cancelled/expired.
	require.NoError(t, local.SetWriteDeadline(time.Now().Add(-time.Second)))
	err = deliverGuestControlResponse(ctx, local, request, response, session)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrDeadlineExceeded))
	require.True(t, session.closed(), "failed local acknowledgement must poison its already-admitted host stream")
	require.NoError(t, ctx.Err(), "request cancellation/deadline must not mask the write-failure path")
}
