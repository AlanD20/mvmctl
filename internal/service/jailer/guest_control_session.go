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

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
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
	mu        sync.Mutex
	ioMu      sync.Mutex
	conn      *tls.Conn
	pidfd     int
	principal GuestControlPrincipal
	pending   *guestcontrolwire.Request
	closed    bool
	closeErr  error
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
		return errors.Join(err, io.ErrShortWrite)
	}
	var line []byte
	for len(line) < 64 {
		var one [1]byte
		if _, err := io.ReadFull(connection, one[:]); err != nil {
			return err
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

func (session *GuestControlSession) ReceiveRequest(ctx context.Context) (_ guestcontrolwire.Request, returnErr error) {
	if session == nil || session.state == nil {
		return guestcontrolwire.Request{}, instanceStateError("", "Guest Control session is unavailable")
	}
	state := session.state
	state.ioMu.Lock()
	defer state.ioMu.Unlock()
	if state.pending != nil {
		return guestcontrolwire.Request{}, instanceValidationError("Guest Control request is already outstanding")
	}
	connection, err := session.connection()
	if err != nil {
		return guestcontrolwire.Request{}, err
	}
	watch, err := beginGuestControlIO(ctx, connection)
	if err != nil {
		return guestcontrolwire.Request{}, err
	}
	defer func() {
		returnErr = watch.finish(ctx, returnErr)
		if returnErr != nil {
			returnErr = errors.Join(returnErr, session.Close(context.Background()))
		}
	}()
	request, err := guestcontrolwire.DecodeRequest(ctx, connection)
	if err != nil {
		return guestcontrolwire.Request{}, err
	}
	state.pending = &request
	return request, nil
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
	if state.pending == nil {
		return instanceValidationError("Guest Control response has no outstanding request")
	}
	if err := response.ValidateFor(*state.pending); err != nil {
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
	if err := guestcontrolwire.EncodeResponse(ctx, connection, *state.pending, response); err != nil {
		return err
	}
	state.pending = nil
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
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
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
	if result == nil {
		result = watch.connection.SetDeadline(time.Time{})
	}
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
