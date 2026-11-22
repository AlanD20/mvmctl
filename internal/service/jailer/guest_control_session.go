package jailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/pkg/errs"
)

// GuestControlPrincipal is a root-derived observation, never authority minted from request fields.
// A worker must reopen current Jailer admission before each feature request.
type GuestControlPrincipal struct {
	OwnerUID         uint32
	OwnerGID         uint32
	VMID             string
	BootID           string
	LaunchGeneration uint64
	ExecutionUID     uint32
}

// GuestControlSession owns only its authenticated stream and independent VMM exit pidfd.
// No VM, backing, network or global mutation lock survives bootstrap. Copies share close state.
type GuestControlSession struct{ state *guestControlSessionState }

type guestControlSessionState struct {
	// mu guards descriptor ownership and close state; ioMu serializes the one outstanding request.
	// Close never waits on ioMu: it must interrupt a blocked receive or send.
	mu              sync.Mutex
	ioMu            sync.Mutex
	receiveMu       sync.Mutex
	readerStarted   bool
	requests        chan guestcontrolwire.Request
	readerDone      chan struct{}
	readerErr       error
	readOutstanding bool
	syncMu          sync.Mutex
	aliasSession    string
	aliasExpected   *guestcontrolwire.FloatingIPAliasAck
	aliasAck        chan guestcontrolwire.FloatingIPAliasAck
	aliasConfirmed  *guestcontrolwire.FloatingIPAliasAck
	conn            *tls.Conn
	pidfd           int
	principal       GuestControlPrincipal
	pending         *guestcontrolwire.Request
	closed          bool
	closeErr        error
}

// OpenGuestControlSession authenticates the protected VMM peer and its provisioned agent endpoint
// under one VM admission. Root host material never crosses this service boundary.
func (server *HostVsockServer) OpenGuestControlSession(
	ctx context.Context, ownerUID, ownerGID uint32, request HostVsockOpenRequest,
) (_ *GuestControlSession, returnErr error) {
	if ctx == nil {
		return nil, instanceValidationError("Guest Control bootstrap context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	admitted, err := server.Open(ctx, ownerUID, ownerGID, request)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, admitted.Close(context.WithoutCancel(ctx))) }()
	if err := requireGuestControlRunning(ctx, admitted); err != nil {
		return nil, err
	}
	backing, err := openGuestControlSessionBacking(ctx, admitted.record)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, backing.close(context.WithoutCancel(ctx))) }()
	if err := connectGuestControl(ctx, admitted.conn); err != nil {
		return nil, err
	}
	secured, err := guestcontrolauth.AuthenticateAgent(ctx, admitted.conn, backing.material)
	if err != nil {
		return nil, err
	}
	// Transfer the sole connection owner before closing bootstrap authority.
	admitted.conn = nil
	session := &GuestControlSession{state: &guestControlSessionState{conn: secured, pidfd: -1}}
	accepted := false
	defer func() {
		if !accepted || returnErr != nil {
			returnErr = errors.Join(returnErr, session.Close(context.WithoutCancel(ctx)))
		}
	}()
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(),
		realProcessIdentityVerifierDeps(), admitted.record, admitted.processes, admitted.peerPIDFD,
		"Guest Control bootstrap"); err != nil {
		return nil, err
	}
	session.state.pidfd, err = unix.FcntlInt(uintptr(admitted.peerPIDFD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, processVerificationError(admitted.record.vmID, "retain independent Guest Control VMM pidfd", err)
	}
	r := admitted.record
	session.state.principal = GuestControlPrincipal{OwnerUID: r.ownerUID, OwnerGID: r.launch.ownerGID,
		VMID: r.vmID, BootID: r.launch.hostBootID, LaunchGeneration: r.launchGeneration,
		ExecutionUID: r.active.userIDs.effective}
	// Explicit close before success makes a failed authority release a rejected bootstrap.
	if err := backing.close(context.WithoutCancel(ctx)); err != nil {
		return nil, err
	}
	if err := admitted.Close(context.WithoutCancel(ctx)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	return session, nil
}

type guestControlSessionBacking struct {
	cache    *managedCacheLease
	backing  *protectedBackingLease
	material guestcontrolauth.RootHostMaterial
}

func openGuestControlSessionBacking(
	ctx context.Context, record instanceRecord,
) (_ *guestControlSessionBacking, returnErr error) {
	if err := requireFloatingIPExecutionIdentity(record); err != nil {
		return nil, err
	}
	value := &guestControlSessionBacking{}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, value.close(context.WithoutCancel(ctx)))
		}
	}()
	var err error
	value.cache, err = repinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(),
		instanceCaller{uid: record.ownerUID, gid: record.launch.ownerGID}, record.launch.cacheLocator,
		record.launch.cacheIdentity)
	if err != nil {
		return nil, err
	}
	rootFD, err := pinSnapshotActiveRoot(ctx, record)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(rootFD)) }()
	source, err := readVMLaunchDeviceAuthority(ctx, record, rootFD)
	if err != nil {
		return nil, err
	}
	if source.Config.Vsock == nil || source.Rootfs.ownerUID != record.ownerUID ||
		protectedCacheIdentity(source.Rootfs.cacheIdentity) != protectedCacheIdentity(value.cache.identity) {
		return nil, instanceStateError(record.vmID, "Guest Control protected rootfs or vsock binding differs")
	}
	value.backing, err = reopenProtectedBacking(ctx, source.Rootfs)
	if err != nil {
		return nil, err
	}
	if err := value.backing.RequireExclusiveCapture(ctx, record.vmID); err != nil {
		return nil, err
	}
	value.material, err = value.backing.readGuestControlHostMaterial(ctx, value.cache)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (value *guestControlSessionBacking) close(ctx context.Context) error {
	var result error
	if value.backing != nil {
		result = errors.Join(result, value.backing.Release(ctx))
		value.backing = nil
	}
	if value.cache != nil {
		result = errors.Join(result, value.cache.Release(ctx))
		value.cache = nil
	}
	value.material = guestcontrolauth.RootHostMaterial{}
	return result
}

// requireGuestControlRunning borrows the one admitted VM/process authority and owns only its
// separate API descriptors. It cannot release the parent or create a transferable admission lease.
func requireGuestControlRunning(ctx context.Context, admitted *HostVsockConnection) (returnErr error) {
	record := admitted.record
	directory, connection, err := openVMAPIConnection(ctx, record, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, connection.Close(), unix.Close(directory)) }()
	peer, err := admitVMAPIPeer(ctx, connection, record, admitted.processes)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(peer)) }()
	observed, err := observeVMAPIState(ctx, connection, record.vmID)
	if err != nil {
		return err
	}
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(),
		realProcessIdentityVerifierDeps(), record, admitted.processes, peer,
		"Guest Control running state"); err != nil {
		return err
	}
	if observed != "Running" {
		return instanceStateError(record.vmID, "Guest Control bootstrap requires a running VM")
	}
	return nil
}

// connectGuestControl reads exactly the CONNECT line, never buffering TLS bytes that follow it.
func connectGuestControl(ctx context.Context, connection net.Conn) (returnErr error) {
	watch, err := beginGuestControlIO(ctx, connection)
	if err != nil {
		return err
	}
	defer func() { returnErr = watch.finish(ctx, returnErr) }()
	request := fmt.Sprintf("CONNECT %d\n", infra.GuestControlVsockPort)
	if n, err := io.WriteString(connection, request); err != nil || n != len(request) {
		return guestControlConnectTransportError(ctx, errors.Join(err, io.ErrShortWrite), true)
	}
	var line []byte
	for len(line) < 64 {
		var one [1]byte
		if _, err := io.ReadFull(connection, one[:]); err != nil {
			return guestControlConnectTransportError(ctx, err, len(line) == 0)
		}
		if one[0] == '\n' {
			text := string(line)
			if !strings.HasPrefix(text, "OK ") {
				break
			}
			port, err := strconv.ParseUint(text[3:], 10, 32)
			if err == nil && port > 0 && strconv.FormatUint(port, 10) == text[3:] {
				return nil
			}
			break
		}
		line = append(line, one[0])
	}
	return instanceValidationError("invalid Guest Control CONNECT acknowledgement")
}

// A transport refusal before the CONNECT acknowledgement is cold-agent unavailability,
// not an authentication failure. Never retry a partial or malformed acknowledgement.
func guestControlConnectTransportError(ctx context.Context, cause error, beforeAcknowledgement bool) error {
	if ctx.Err() == nil && beforeAcknowledgement &&
		(errors.Is(cause, io.EOF) || errors.Is(cause, unix.ECONNRESET) ||
			errors.Is(cause, unix.EPIPE) || errors.Is(cause, unix.ECONNREFUSED)) {
		return errs.WrapMsg(errs.CodeAgentUnreachable, "Guest Control agent is not accepting connections", cause,
			errs.WithClass(errs.ClassRetryable))
	}
	return cause
}

// Principal returns only immutable observed identity, not network or process mutation authority.
func (session *GuestControlSession) Principal() (GuestControlPrincipal, error) {
	if session == nil || session.state == nil {
		return GuestControlPrincipal{}, instanceStateError("", "Guest Control session is unavailable")
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return GuestControlPrincipal{}, instanceStateError("", "Guest Control session is closed")
	}
	return state.principal, nil
}

func (session *GuestControlSession) ReceiveRequest(ctx context.Context) (guestcontrolwire.Request, error) {
	if ctx == nil || session == nil || session.state == nil {
		return guestcontrolwire.Request{}, instanceValidationError(
			"Guest Control receive context and session are required",
		)
	}
	state := session.state
	state.receiveMu.Lock()
	defer state.receiveMu.Unlock()
	state.mu.Lock()
	if state.pending != nil {
		state.mu.Unlock()
		return guestcontrolwire.Request{}, instanceValidationError("Guest Control request is already outstanding")
	}
	if !state.readerStarted && !state.closed {
		state.readerStarted = true
		state.requests = make(chan guestcontrolwire.Request, 1)
		state.readerDone = make(chan struct{})
		go session.readMessages()
	}
	requests, done, closed := state.requests, state.readerDone, state.closed
	state.mu.Unlock()
	if closed {
		return guestcontrolwire.Request{}, instanceStateError("", "Guest Control session is closed")
	}
	select {
	case request := <-requests:
		state.mu.Lock()
		state.pending = &request
		state.mu.Unlock()
		return request, nil
	case <-done:
		state.mu.Lock()
		err := state.readerErr
		state.mu.Unlock()
		return guestcontrolwire.Request{}, err
	case <-ctx.Done():
		return guestcontrolwire.Request{}, errors.Join(ctx.Err(), session.Close(context.Background()))
	}
}

// readMessages owns the only authenticated-stream read, including while guest effects execute.
// The reader retains no admission locks and Close interrupts its underlying descriptor directly.
func (session *GuestControlSession) readMessages() {
	state := session.state
	var failure error
	defer func() {
		if failure == nil {
			failure = instanceStateError("", "Guest Control stream stopped")
		}
		state.mu.Lock()
		state.readerErr = failure
		close(state.readerDone)
		state.mu.Unlock()
		_ = session.Close(context.Background())
	}()
	connection, err := session.connection()
	if err != nil {
		failure = err
		return
	}
	for {
		request, ack, err := guestcontrolwire.DecodeGuestMessage(context.Background(), connection)
		if err != nil {
			failure = err
			return
		}
		state.mu.Lock()
		if ack != nil {
			expected := state.aliasExpected
			if expected == nil || ack.SessionID != expected.SessionID || ack.SnapshotID != expected.SnapshotID ||
				ack.Digest != expected.Digest {
				state.mu.Unlock()
				failure = instanceValidationError("unsolicited operator alias acknowledgement")
				return
			}
			select {
			case state.aliasAck <- *ack:
				state.aliasExpected = nil
			default:
				state.mu.Unlock()
				failure = instanceValidationError("duplicate operator alias acknowledgement")
				return
			}
			state.mu.Unlock()
			continue
		}
		if state.readOutstanding {
			state.mu.Unlock()
			failure = instanceValidationError("Guest Control request is already outstanding")
			return
		}
		state.readOutstanding = true
		state.requests <- *request
		state.mu.Unlock()
	}
}

// SyncFloatingIPAliases sends one complete host-authorized snapshot and waits for its exact ACK.
// ReceiveRequest must be running concurrently as the sole stream reader. No root mutation authority is retained.
func (session *GuestControlSession) SyncFloatingIPAliases(
	ctx context.Context,
	snapshot guestcontrolwire.FloatingIPAliasSnapshot,
) (returnErr error) {
	if ctx == nil || session == nil || session.state == nil {
		return instanceValidationError("operator alias sync requires a session and context")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state := session.state
	state.syncMu.Lock()
	defer state.syncMu.Unlock()
	connection, err := session.connection()
	if err != nil {
		return err
	}
	watch, err := beginGuestControlIO(ctx, connection)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = watch.finish(ctx, returnErr)
		if returnErr != nil {
			returnErr = errors.Join(returnErr, session.Close(context.Background()))
		}
	}()
	state.mu.Lock()
	if state.aliasSession == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			state.mu.Unlock()
			return err
		}
		state.aliasSession = id.String()
	}
	expected := guestcontrolwire.FloatingIPAliasAck{
		SchemaVersion: 1,
		Feature:       "floating_ip_alias_ack",
		SessionID:     state.aliasSession,
		SnapshotID:    snapshot.ID,
		Digest:        digest,
	}
	state.aliasExpected = &expected
	channel := make(chan guestcontrolwire.FloatingIPAliasAck, 1)
	state.aliasAck = channel
	state.mu.Unlock()
	state.ioMu.Lock()
	writeErr := func() error {
		count := len(snapshot.Aliases)
		for offset, index := 0, uint32(0); ; offset, index = offset+guestcontrolwire.FloatingIPAliasPageSize, index+1 {
			end := min(offset+guestcontrolwire.FloatingIPAliasPageSize, count)
			page := guestcontrolwire.FloatingIPAliasPage{
				SchemaVersion: 1,
				Feature:       "floating_ip_alias_sync",
				SessionID:     expected.SessionID,
				SnapshotID:    snapshot.ID,
				Index:         index,
				Final:         end == count,
				Digest:        digest,
				Aliases:       snapshot.Aliases[offset:end],
			}
			if err := guestcontrolwire.EncodeFloatingIPAliasPage(ctx, connection, page); err != nil {
				return err
			}
			if page.Final {
				return nil
			}
		}
	}()
	state.ioMu.Unlock()
	if writeErr != nil {
		return writeErr
	}
	select {
	case ack := <-channel:
		if !ack.Accepted {
			return instanceStateError("", "operator ownership committed but guest alias synchronization failed")
		}
		state.mu.Lock()
		state.aliasConfirmed = &ack
		state.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *GuestControlSession) SendResponse(
	ctx context.Context, response guestcontrolwire.Response,
) (returnErr error) {
	if session == nil || session.state == nil {
		return instanceStateError("", "Guest Control session is unavailable")
	}
	state := session.state
	state.ioMu.Lock()
	defer state.ioMu.Unlock()
	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if pending == nil {
		return instanceValidationError("Guest Control response has no outstanding request")
	}
	if err := response.ValidateFor(*pending); err != nil {
		return err
	}
	connection, err := session.connection()
	if err != nil {
		return err
	}
	watch, err := beginGuestControlIO(ctx, connection)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = watch.finish(ctx, returnErr)
		if returnErr != nil {
			returnErr = errors.Join(returnErr, session.Close(context.Background()))
		}
	}()
	// Release the completed request before the peer can observe any response bytes.
	// A legitimate sequential peer may send its next request before the final Write returns.
	// ioMu still serializes response/snapshot writes, and any failed write closes the session.
	state.mu.Lock()
	state.pending = nil
	state.readOutstanding = false
	state.mu.Unlock()
	if err := guestcontrolwire.EncodeResponse(ctx, connection, *pending, response); err != nil {
		return err
	}
	return nil
}

func (session *GuestControlSession) connection() (*tls.Conn, error) {
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.conn == nil {
		return nil, instanceStateError("", "Guest Control session is closed")
	}
	return state.conn, nil
}

// WaitVMMExit observes an independently duplicated pidfd; Close cannot recycle its polled descriptor.
func (session *GuestControlSession) WaitVMMExit(ctx context.Context) (returnErr error) {
	if ctx == nil || session == nil || session.state == nil {
		return instanceValidationError("Guest Control exit context and session are required")
	}
	state := session.state
	state.mu.Lock()
	if state.closed || state.pidfd < 0 {
		state.mu.Unlock()
		return instanceStateError("", "Guest Control exit authority is closed")
	}
	fd, err := unix.FcntlInt(uintptr(state.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	state.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		count, err := unix.Poll(poll, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 1 && poll[0].Revents != 0 && poll[0].Revents&^int16(unix.POLLIN|unix.POLLHUP) == 0 {
			return nil
		}
		if count != 0 || poll[0].Revents != 0 {
			return instanceStateError("", "Guest Control exit pidfd has invalid readiness")
		}
		state.mu.Lock()
		closed := state.closed
		state.mu.Unlock()
		if closed {
			return instanceStateError("", "Guest Control session closed while observing VMM exit")
		}
	}
}

func (session *GuestControlSession) Close(ctx context.Context) error {
	if session == nil || session.state == nil {
		return nil
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeErr
	}
	state.closed = true
	if state.conn != nil {
		state.closeErr = errors.Join(state.closeErr, state.conn.NetConn().Close())
	}
	if state.pidfd >= 0 {
		state.closeErr = errors.Join(state.closeErr, unix.Close(state.pidfd))
		state.pidfd = -1
	}
	if ctx == nil {
		state.closeErr = errors.Join(state.closeErr, instanceValidationError("Guest Control close context is required"))
	}
	return state.closeErr
}

type guestControlIOWatch struct {
	connection net.Conn
	stop       func() bool
	done       chan struct{}
}

func beginGuestControlIO(ctx context.Context, connection net.Conn) (*guestControlIOWatch, error) {
	if ctx == nil || connection == nil {
		return nil, instanceValidationError("Guest Control I/O context and connection are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	watch := &guestControlIOWatch{connection: connection, done: make(chan struct{})}
	watch.stop = context.AfterFunc(ctx, func() { _ = closeGuestControlTransport(connection); close(watch.done) })
	return watch, nil
}

func (watch *guestControlIOWatch) finish(ctx context.Context, primary error) error {
	if !watch.stop() {
		<-watch.done
	}
	result := errors.Join(primary, ctx.Err())
	if result != nil {
		result = errors.Join(result, closeGuestControlTransport(watch.connection))
	}
	return result
}

// Abort the owned transport directly so cancellation never waits for TLS close-notify I/O.
func closeGuestControlTransport(connection net.Conn) error {
	if secured, ok := connection.(*tls.Conn); ok {
		return secured.NetConn().Close()
	}
	return connection.Close()
}
