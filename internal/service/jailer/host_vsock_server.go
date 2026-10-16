package jailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// HostVsockServer admits the fixed private vsock endpoint of one owned active VM.
type HostVsockServer struct{ instances *instanceAuthority }

// NewHostVsockServer constructs the production instance authority.
func NewHostVsockServer() *HostVsockServer {
	return &HostVsockServer{instances: newInstanceAuthority()}
}

// HostVsockConnection retains process and instance authority until its one descriptor handoff completes.
// THREAD-SAFE: its mutex serializes delivery and close across all pointer users.
type HostVsockConnection struct {
	// mu guards the connection, authority resources, sent flag, and cached cleanup result.
	mu        sync.Mutex
	active    *activeLease
	processes *reopenedRecordedProcesses
	record    instanceRecord
	conn      *net.UnixConn
	directory int
	peerPIDFD int
	sent      bool
	closed    bool
	closeErr  error
}

// Open authenticates operator ownership separately from the isolated Firecracker socket peer.
func (server *HostVsockServer) Open(
	ctx context.Context, ownerUID, ownerGID uint32, request HostVsockOpenRequest,
) (_ *HostVsockConnection, returnErr error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, instanceValidationError("host vsock context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	active, err := server.instances.LockActive(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.VMID)
	if err != nil {
		return nil, err
	}
	connection := &HostVsockConnection{active: active, record: active.snapshot(), directory: -1, peerPIDFD: -1}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, connection.Close(context.WithoutCancel(ctx)))
		}
	}()
	record := connection.record
	if record.launch == nil || record.active == nil || record.active.userIDs.effective == 0 ||
		record.active.groupIDs.effective == 0 || record.active.userIDs.effective == record.ownerUID ||
		record.active.groupIDs.effective == record.launch.ownerGID {
		return nil, instanceStateError(request.VMID, "host vsock requires isolated execution identity")
	}
	identity := realProcessIdentityVerifierDeps()
	bootID, err := identity.readBootID(ctx)
	if err != nil || bootID != record.launch.hostBootID {
		return nil, processVerificationError(record.vmID, "host vsock boot identity mismatch", err)
	}
	connection.processes, err = reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
	if err != nil {
		return nil, err
	}
	if _, err := verifyRecoveredActiveIdentity(ctx, identity, record); err != nil {
		return nil, err
	}
	if err := connection.processes.verifyLocators(ctx); err != nil {
		return nil, err
	}
	connection.directory, err = pinVMAPIPrivateDirectory(ctx, record)
	if err != nil {
		return nil, err
	}
	var socket unix.Stat_t
	if err := unix.Fstatat(connection.directory, infra.VMVsockSocketFilename, &socket, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, vmAPIBrokerError("inspect protected host vsock socket", err)
	}
	if socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Uid != record.active.userIDs.effective ||
		socket.Gid != record.active.groupIDs.effective || socket.Mode&0077 != 0 || socket.Mode&0200 == 0 {
		return nil, instanceStateError(record.vmID, "host vsock socket is not private to the execution identity")
	}
	path := fmt.Sprintf("/proc/self/fd/%d/%s", connection.directory, infra.VMVsockSocketFilename)
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, vmAPIBrokerError("connect protected host vsock socket", err)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, errors.Join(instanceStateError(record.vmID, "host vsock connection is not AF_UNIX"), conn.Close())
	}
	connection.conn = unixConn
	connection.peerPIDFD, err = admitVMAPIPeer(ctx, unixConn, record, connection.processes)
	if err != nil {
		return nil, err
	}
	accepted = true
	return connection, nil
}

// Deliver sends the sole fixed success response and exactly one connected descriptor on control fd 0.
// It never writes a guest CONNECT request, token, command, or file-transfer payload.
func (connection *HostVsockConnection) Deliver(ctx context.Context, controlFD uintptr) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if ctx == nil || controlFD != 0 || connection.closed || connection.sent || connection.conn == nil {
		return instanceValidationError("host vsock handoff is unavailable")
	}
	connection.sent = true
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(),
		realProcessIdentityVerifierDeps(), connection.record, connection.processes, connection.peerPIDFD, "host vsock"); err != nil {
		return err
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeSuccess(ctx, &frame, HostVsockOpenAction, system.HostVsockOpenResult{
		VMID: connection.record.vmID, Connected: true,
	}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := connection.conn.SyscallConn()
	if err != nil {
		return err
	}
	value := frame.Bytes()
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var n int
		var sendErr error
		if len(value) == frame.Len() {
			controlErr := raw.Control(func(fd uintptr) {
				n, sendErr = unix.SendmsgN(0, value[:1], unix.UnixRights(int(fd)), nil,
					unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
			})
			sendErr = errors.Join(sendErr, controlErr)
		} else {
			n, sendErr = unix.SendmsgN(0, value, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		}
		// CRITICAL: Advance after every positive send; never resend ancillary rights after their carrier byte.
		if n > 0 {
			value = value[n:]
		}
		if sendErr != nil && !errors.Is(sendErr, unix.EINTR) && !errors.Is(sendErr, unix.EAGAIN) &&
			!errors.Is(sendErr, unix.EWOULDBLOCK) {
			return sendErr
		}
		if n == 0 {
			if _, err := unix.Poll([]unix.PollFd{{Fd: 0, Events: unix.POLLOUT}}, 100); err != nil &&
				!errors.Is(err, unix.EINTR) {
				return err
			}
		}
	}
	return unix.Shutdown(0, unix.SHUT_WR)
}

// Close releases root-owned resources without shutting down the caller's transferred connection description.
func (connection *HostVsockConnection) Close(ctx context.Context) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return connection.closeErr
	}
	connection.closed = true
	cleanupCtx := context.Background()
	if ctx == nil {
		connection.closeErr = instanceValidationError("host vsock cleanup context is required")
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if connection.peerPIDFD >= 0 {
		connection.closeErr = errors.Join(connection.closeErr, unix.Close(connection.peerPIDFD))
	}
	if connection.conn != nil {
		connection.closeErr = errors.Join(connection.closeErr, connection.conn.Close())
	}
	if connection.directory >= 0 {
		connection.closeErr = errors.Join(connection.closeErr, unix.Close(connection.directory))
	}
	if connection.processes != nil {
		connection.closeErr = errors.Join(connection.closeErr, connection.processes.Release(cleanupCtx))
	}
	connection.closeErr = errors.Join(connection.closeErr, connection.active.Release(cleanupCtx))
	return connection.closeErr
}
