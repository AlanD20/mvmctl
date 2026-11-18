package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
)

const guestControlHandshakeTimeout = 5 * time.Second
const guestControlRequestTimeout = 30 * time.Second

// GuestControlGateway forwards only closed, root-local feature requests over one pinned host TLS session.
// The session owns no host locks. A second host connection is refused while authentication or a live session exists.
type GuestControlGateway struct {
	host       net.Listener
	local      net.Listener
	material   guestcontrolauth.AgentMaterial
	mu         sync.Mutex
	connecting bool
	session    *guestControlSession
	aliases    *guestControlAliases
}

// NewGuestControlGateway owns both supplied listeners. Production supplies the fixed vsock and private Unix listeners.
func NewGuestControlGateway(
	host, local net.Listener, material guestcontrolauth.AgentMaterial,
) (*GuestControlGateway, error) {
	if host == nil || local == nil {
		return nil, errors.New("guest control listeners are required")
	}
	if _, err := material.Encode(); err != nil {
		return nil, errors.New("guest control material is invalid")
	}
	return &GuestControlGateway{host: host, local: local, material: material}, nil
}

// Run owns accepted connections and interrupts every blocked read, write and authentication on cancellation.
func (gateway *GuestControlGateway) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("guest control context is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 2)
	var accepted sync.WaitGroup
	accept := func(listener net.Listener, host bool) {
		defer accepted.Done()
		for {
			conn, err := listener.Accept()
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				failed <- err
				return
			}
			accepted.Go(func() {
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				if host {
					gateway.acceptHost(ctx, conn)
				} else {
					gateway.acceptLocal(ctx, conn)
				}
			})
		}
	}
	accepted.Add(2)
	go accept(gateway.host, true)
	go accept(gateway.local, false)
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-failed:
	}
	cancel()
	_ = gateway.host.Close()
	_ = gateway.local.Close()
	gateway.mu.Lock()
	session := gateway.session
	gateway.mu.Unlock()
	if session != nil {
		session.close()
	}
	accepted.Wait()
	if ctx.Err() != nil && errors.Is(runErr, net.ErrClosed) {
		return nil
	}
	return runErr
}

func (gateway *GuestControlGateway) acceptHost(ctx context.Context, conn net.Conn) {
	gateway.mu.Lock()
	busy := gateway.connecting || gateway.session != nil && !gateway.session.closed()
	if !busy {
		gateway.connecting = true
	}
	gateway.mu.Unlock()
	if busy {
		_ = conn.Close()
		return
	}
	authentication, cancel := context.WithTimeout(ctx, guestControlHandshakeTimeout)
	secured, err := guestcontrolauth.AuthenticateHost(authentication, conn, gateway.material)
	cancel()
	gateway.mu.Lock()
	gateway.connecting = false
	if err != nil {
		gateway.mu.Unlock()
		return
	}
	session := newGuestControlSession(secured, conn)
	if gateway.aliases != nil {
		setup, cancel := context.WithTimeout(ctx, guestControlAliasIOTimeout)
		epoch, err := gateway.aliases.beginSession(setup)
		cancel()
		if err != nil {
			gateway.mu.Unlock()
			_ = conn.Close()
			return
		}
		session.aliases, session.aliasEpoch = gateway.aliases, epoch
	}
	gateway.session = session
	gateway.mu.Unlock()
	defer session.close()
	session.readResponses(ctx)
	gateway.mu.Lock()
	if gateway.session == session {
		gateway.session = nil
	}
	gateway.mu.Unlock()
}

func (gateway *GuestControlGateway) acceptLocal(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if err := requireGuestControlRootPeer(conn); err != nil {
		return
	}
	requestContext, cancel := context.WithTimeout(ctx, guestControlRequestTimeout)
	defer cancel()
	deadline, _ := requestContext.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	request, err := guestcontrolwire.DecodeRequest(requestContext, conn)
	if err != nil || validateGuestControlRequest(request) != nil {
		return
	}
	// One complete frame followed by physical write EOF is admitted before host effects.
	var trailing [1]byte
	if n, err := conn.Read(trailing[:]); n != 0 || !errors.Is(err, io.EOF) {
		return
	}
	stopWatching := watchGuestControlLocalDisconnect(requestContext, conn.(*net.UnixConn), cancel)
	defer stopWatching()
	gateway.mu.Lock()
	session := gateway.session
	gateway.mu.Unlock()
	response := guestcontrolwire.Response{RequestID: request.RequestID, Operation: request.Operation,
		Failure: &guestcontrolwire.Failure{Code: guestcontrolwire.FailureUnavailable}}
	if session != nil {
		result, err := session.exchange(requestContext, request)
		if err == nil {
			response = result
		} else {
			response.Failure = &guestcontrolwire.Failure{Code: guestcontrolwire.FailureOutcomeUnknown}
		}
	}
	_ = deliverGuestControlResponse(requestContext, conn, request, response, session)
}

func validateGuestControlRequest(request guestcontrolwire.Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Operation != guestcontrolwire.Inspect && request.Input.ExpectedGeneration == 0 {
		return errors.New("guest control mutation requires a nonzero ownership generation")
	}
	return nil
}

func requireGuestControlRootPeer(conn net.Conn) error {
	local, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("guest control requires a local Unix peer")
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if peerErr != nil || credentials == nil || credentials.Uid != 0 {
		return errors.New("guest control requires guest root")
	}
	return nil
}

type guestControlPending struct {
	request  guestcontrolwire.Request
	response chan guestcontrolwire.Response
}
type guestControlSession struct {
	conn       net.Conn
	transport  net.Conn
	gate       chan struct{}
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	pending    *guestControlPending
	aliases    *guestControlAliases
	aliasEpoch string
}

func newGuestControlSession(conn, transport net.Conn) *guestControlSession {
	return &guestControlSession{
		conn:      conn,
		transport: transport,
		gate:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
}
func (session *guestControlSession) closed() bool {
	select {
	case <-session.done:
		return true
	default:
		return false
	}
}
func (session *guestControlSession) close() {
	session.once.Do(func() {
		_ = session.transport.Close()
		if session.aliases != nil {
			session.aliases.sessionLost(session.aliasEpoch)
		}
		close(session.done)
	})
}

// readResponses is the sole TLS reader, including while idle. EOF or unsolicited/partial data poisons this session.
func (session *guestControlSession) readResponses(ctx context.Context) {
	reader := bufio.NewReader(session.conn)
	for {
		if _, err := reader.Peek(1); err != nil {
			return
		}
		session.mu.Lock()
		pending := session.pending
		session.mu.Unlock()
		if pending == nil {
			return
		}
		response, err := guestcontrolwire.DecodeResponse(ctx, reader, pending.request)
		if err != nil {
			return
		}
		session.mu.Lock()
		if session.pending != pending {
			session.mu.Unlock()
			return
		}
		session.pending = nil
		// Clear the request deadline before waiting for the next host response/idle EOF.
		if err := session.conn.SetDeadline(time.Time{}); err != nil {
			session.mu.Unlock()
			return
		}
		session.mu.Unlock()
		pending.response <- response
	}
}

func (session *guestControlSession) exchange(
	ctx context.Context, request guestcontrolwire.Request,
) (guestcontrolwire.Response, error) {
	select {
	case session.gate <- struct{}{}:
	case <-ctx.Done():
		return guestcontrolwire.Response{}, ctx.Err()
	case <-session.done:
		return guestcontrolwire.Response{}, io.EOF
	}
	defer func() { <-session.gate }()
	if session.closed() {
		return guestcontrolwire.Response{}, io.EOF
	}
	// Never let a reused local request ID make an old host response eligible for a later operation.
	localRequest := request
	id, err := uuid.NewRandom()
	if err != nil {
		return guestcontrolwire.Response{}, err
	}
	request.RequestID = id.String()
	pending := &guestControlPending{request: request, response: make(chan guestcontrolwire.Response, 1)}
	session.mu.Lock()
	session.pending = pending
	session.mu.Unlock()
	stop := context.AfterFunc(ctx, session.close)
	defer stop()
	deadline, bounded := ctx.Deadline()
	if !bounded {
		session.close()
		return guestcontrolwire.Response{}, errors.New("guest control exchange needs a deadline")
	}
	if err := session.conn.SetDeadline(deadline); err != nil {
		session.close()
		return guestcontrolwire.Response{}, err
	}
	var aliasStarted uint64
	if session.aliases != nil {
		var err error
		aliasStarted, err = guestControlAliasNow()
		if err != nil {
			session.close()
			return guestcontrolwire.Response{}, err
		}
	}
	if err := guestcontrolwire.EncodeRequest(ctx, session.conn, request); err != nil {
		session.close()
		return guestcontrolwire.Response{}, err
	}
	var response guestcontrolwire.Response
	select {
	case response = <-pending.response:
	case <-ctx.Done():
		session.close()
		return guestcontrolwire.Response{}, ctx.Err()
	case <-session.done:
		select {
		case response = <-pending.response:
		default:
			return guestcontrolwire.Response{}, io.EOF
		}
	}
	if err := ctx.Err(); err != nil {
		session.close()
		return guestcontrolwire.Response{}, err
	}
	response.RequestID = localRequest.RequestID
	if err := response.ValidateFor(localRequest); err != nil {
		session.close()
		return guestcontrolwire.Response{}, err
	}
	if session.aliases != nil {
		if err := session.aliases.reconcile(ctx, session.aliasEpoch, aliasStarted, localRequest, response); err != nil {
			return guestcontrolwire.Response{RequestID: localRequest.RequestID, Operation: localRequest.Operation,
				Failure: &guestcontrolwire.Failure{Code: guestcontrolwire.FailureInternal}}, nil
		}
	}
	return response, nil
}

// A write-half-close admits a request; full disconnect cancels its outstanding host exchange.
// Poll runs only while the owner keeps this exact Unix descriptor alive, never across descriptor reuse.
func watchGuestControlLocalDisconnect(ctx context.Context, conn *net.UnixConn, cancel context.CancelFunc) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := conn.SyscallConn()
		if err != nil {
			cancel()
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			default:
			}
			var pollErr error
			var disconnected bool
			err := raw.Control(func(fd uintptr) {
				events := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLHUP | unix.POLLERR}}
				_, pollErr = unix.Poll(events, 50)
				disconnected = events[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0
			})
			if err != nil || pollErr != nil && !errors.Is(pollErr, unix.EINTR) || disconnected {
				cancel()
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

// deliverGuestControlResponse retires the admitted stream if its local acknowledgement cannot be written.
func deliverGuestControlResponse(
	ctx context.Context, conn net.Conn, request guestcontrolwire.Request,
	response guestcontrolwire.Response, session *guestControlSession,
) error {
	err := guestcontrolwire.EncodeResponse(ctx, conn, request, response)
	if err != nil && session != nil {
		session.close()
	}
	return err
}
