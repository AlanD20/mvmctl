package jailer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type vmAPIOperation uint8

const (
	vmAPIPause vmAPIOperation = iota + 1
	vmAPIResume
	vmAPIStartInstance
	vmAPIReady
)

// VMAPIBrokerServer authorizes named control operations under the instance lock.
// Operator ownership and Firecracker execution credentials are distinct authorities.
type VMAPIBrokerServer struct{ instances *instanceAuthority }

// NewVMAPIBrokerServer constructs the root-owned instance authority.
func NewVMAPIBrokerServer() *VMAPIBrokerServer {
	return &VMAPIBrokerServer{instances: newInstanceAuthority()}
}

// WaitReady waits only for the authenticated protected API socket. It does not
// make an HTTP request or change the VM's state.
func (server *VMAPIBrokerServer) WaitReady(
	ctx context.Context, ownerUID, ownerGID uint32, request VMAPIRequest,
) (VMAPIReadyResult, error) {
	if _, err := server.apply(ctx, ownerUID, ownerGID, request, vmAPIReady); err != nil {
		return VMAPIReadyResult{}, err
	}
	return VMAPIReadyResult{VMID: request.VMID, Ready: true}, nil
}

// Pause applies only the fixed pause request to an authenticated Firecracker peer.
func (server *VMAPIBrokerServer) Pause(
	ctx context.Context, ownerUID, ownerGID uint32, request VMAPIRequest,
) (VMAPIResult, error) {
	return server.apply(ctx, ownerUID, ownerGID, request, vmAPIPause)
}

// Resume applies only the fixed resume request to an authenticated Firecracker peer.
func (server *VMAPIBrokerServer) Resume(
	ctx context.Context, ownerUID, ownerGID uint32, request VMAPIRequest,
) (VMAPIResult, error) {
	return server.apply(ctx, ownerUID, ownerGID, request, vmAPIResume)
}

// StartInstance applies the fixed InstanceStart action without spawning or configuring a process.
func (server *VMAPIBrokerServer) StartInstance(
	ctx context.Context, ownerUID, ownerGID uint32, request VMAPIRequest,
) (VMAPIResult, error) {
	return server.apply(ctx, ownerUID, ownerGID, request, vmAPIStartInstance)
}

func (server *VMAPIBrokerServer) apply(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMAPIRequest,
	operation vmAPIOperation,
) (_ VMAPIResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	if ctx == nil {
		return VMAPIResult{}, instanceValidationError("VM API operation context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	active, err := server.instances.LockActive(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.VMID)
	if err != nil {
		return VMAPIResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release VM API instance authority",
			active.Release(context.WithoutCancel(ctx)))
	}()
	record := active.snapshot()
	if record.launch == nil || record.active == nil || record.active.userIDs.effective == 0 ||
		record.active.groupIDs.effective == 0 || record.active.userIDs.effective == record.ownerUID ||
		record.active.groupIDs.effective == record.launch.ownerGID {
		return VMAPIResult{}, instanceStateError(request.VMID, "VM API requires isolated execution identity")
	}
	identity := realProcessIdentityVerifierDeps()
	bootID, err := identity.readBootID(ctx)
	if err != nil || bootID != record.launch.hostBootID {
		return VMAPIResult{}, processVerificationError(request.VMID, "VM API host boot identity mismatch", err)
	}
	processes, err := reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
	if err != nil {
		return VMAPIResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release VM API process authority",
			processes.Release(context.WithoutCancel(ctx)))
	}()
	if _, err := verifyRecoveredActiveIdentity(ctx, identity, record); err != nil {
		return VMAPIResult{}, err
	}
	if err := processes.verifyLocators(ctx); err != nil {
		return VMAPIResult{}, err
	}
	directory, connection, err := openVMAPIConnection(ctx, record, operation == vmAPIReady)
	if err != nil {
		return VMAPIResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close VM API runtime directory", unix.Close(directory))
	}()
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close VM API connection", connection.Close())
	}()
	peerPIDFD, err := admitVMAPIPeer(ctx, connection, record, processes)
	if err != nil {
		return VMAPIResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close VM API peer pidfd", unix.Close(peerPIDFD))
	}()
	if operation != vmAPIReady {
		if err := sendVMAPIOperation(ctx, connection, operation); err != nil {
			return VMAPIResult{}, err
		}
	}
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(), identity,
		record, processes, peerPIDFD, "API"); err != nil {
		return VMAPIResult{}, err
	}
	return VMAPIResult{VMID: request.VMID, Applied: true}, nil
}

// The launch-only readiness path retries only a socket not yet published or
// listening. An identity, ownership, or authority failure is never retried.
func openVMAPIConnection(ctx context.Context, record instanceRecord, waitReady bool) (int, net.Conn, error) {
	if waitReady {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	for {
		directory, err := pinVMAPIPrivateDirectory(ctx, record)
		if err == nil {
			path := fmt.Sprintf("/proc/self/fd/%d/%s", directory, infra.VMFirecrackerAPISocketFilename)
			connection, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
			if dialErr == nil {
				return directory, connection, nil
			}
			if closeErr := unix.Close(directory); closeErr != nil {
				return -1, nil, vmAPIBrokerError("close protected VM API directory after failed connection",
					errors.Join(dialErr, closeErr))
			}
			err = vmAPIBrokerError("connect protected VM API", dialErr)
			if !waitReady || (!errors.Is(dialErr, unix.ENOENT) && !errors.Is(dialErr, unix.ECONNREFUSED)) {
				return -1, nil, err
			}
		} else if !waitReady || !errors.Is(err, unix.ENOENT) {
			return -1, nil, err
		}
		select {
		case <-ctx.Done():
			return -1, nil, vmAPIBrokerError("protected VM API did not become ready", errors.Join(err, ctx.Err()))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// CRITICAL: The observed jail root is pinned before traversing only compiled, no-follow runtime names.
// An operator-owned runtime is rejected even if its socket currently answers as the right Firecracker.
func pinVMAPIPrivateDirectory(ctx context.Context, record instanceRecord) (directoryFD int, returnErr error) {
	defer func() {
		if returnErr != nil && directoryFD >= 0 {
			returnErr = errors.Join(returnErr, unix.Close(directoryFD))
			directoryFD = -1
		}
	}()
	deps := realProcessIdentityVerifierDeps()
	processDirectory, err := deps.openProcessDirectory(ctx, record.launch.process.pid)
	if err != nil {
		return -1, vmAPIBrokerError("pin VM API process directory", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(processDirectory)) }()
	root, err := deps.pinProcessObject(ctx, processDirectory, processRootObject)
	if err != nil {
		return -1, vmAPIBrokerError("pin VM API jail root", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(root)) }()
	var rootStat unix.Stat_t
	if err := unix.Fstat(root, &rootStat); err != nil {
		return -1, vmAPIBrokerError("inspect VM API jail root", err)
	}
	if uint64(rootStat.Dev) != record.active.jailRoot.device || rootStat.Ino != record.active.jailRoot.inode ||
		rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Uid != 0 || rootStat.Gid != 0 ||
		rootStat.Mode&0022 != 0 {
		return -1, instanceStateError(record.vmID, "VM API jail root differs from active identity")
	}
	directory, err := unix.Openat2(root, strings.TrimPrefix(managedVMPrivateRuntimeMountPath, "/"), &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return -1, vmAPIBrokerError("pin protected VM API directory", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, unix.Close(directory))
		}
	}()
	var runtimeStat unix.Stat_t
	if err := unix.Fstat(directory, &runtimeStat); err != nil {
		return -1, vmAPIBrokerError("inspect protected VM API directory", err)
	}
	if runtimeStat.Uid != record.active.userIDs.effective || runtimeStat.Gid != record.active.groupIDs.effective ||
		runtimeStat.Mode&unix.S_IFMT != unix.S_IFDIR || runtimeStat.Mode&07777 != 0700 {
		return -1, instanceStateError(record.vmID, "VM API runtime is not private to the execution identity")
	}
	var socket unix.Stat_t
	if err := unix.Fstatat(
		directory, infra.VMFirecrackerAPISocketFilename, &socket, unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return -1, vmAPIBrokerError("inspect protected VM API socket", err)
	}
	if socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Uid != record.active.userIDs.effective ||
		socket.Gid != record.active.groupIDs.effective || socket.Mode&0077 != 0 || socket.Mode&0200 == 0 {
		return -1, instanceStateError(record.vmID, "VM API socket is not private to the execution identity")
	}
	accepted = true
	return directory, nil
}

func admitVMAPIPeer(
	ctx context.Context,
	connection net.Conn,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
) (_ int, returnErr error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return -1, instanceStateError(record.vmID, "VM API connection is not AF_UNIX")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return -1, vmAPIBrokerError("access VM API connection", err)
	}
	deps := realVMExecPeerAuthorityDeps()
	var credentials *unix.Ucred
	peerPIDFD := -1
	var peerErr error
	controlErr := raw.Control(func(fd uintptr) {
		credentials, peerErr = deps.peerCredentials(ctx, int(fd))
		if peerErr == nil {
			peerPIDFD, peerErr = deps.peerPIDFD(ctx, int(fd))
		}
	})
	accepted := false
	defer func() {
		if !accepted && peerPIDFD >= 0 {
			returnErr = errors.Join(returnErr, unix.Close(peerPIDFD))
		}
	}()
	if err := errors.Join(controlErr, peerErr); err != nil {
		return -1, vmAPIBrokerError("acquire VM API peer identity", err)
	}
	if peerPIDFD < 0 || credentials == nil || int(credentials.Pid) != record.launch.process.pid ||
		credentials.Uid != record.active.userIDs.effective || credentials.Gid != record.active.groupIDs.effective {
		return -1, processVerificationError(record.vmID, "VM API peer differs from isolated Firecracker", nil)
	}
	identity := realProcessIdentityVerifierDeps()
	if err := requireProcessDescriptorCLOEXEC(ctx, identity, peerPIDFD, "VM API peer pidfd"); err != nil {
		return -1, err
	}
	if err := validateVMExecRetainedProcessAuthority(processes, record, "API"); err != nil {
		return -1, err
	}
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, deps, identity, record, processes, peerPIDFD, "API"); err != nil {
		return -1, err
	}
	accepted = true
	return peerPIDFD, nil
}

func sendVMAPIOperation(ctx context.Context, connection net.Conn, operation vmAPIOperation) (returnErr error) {
	var method, path, body string
	switch operation {
	case vmAPIPause:
		method, path, body = http.MethodPatch, "/vm", `{"state":"Paused"}`
	case vmAPIResume:
		method, path, body = http.MethodPatch, "/vm", `{"state":"Resumed"}`
	case vmAPIStartInstance:
		method, path, body = http.MethodPut, "/actions", `{"action_type":"InstanceStart"}`
	default:
		return instanceValidationError("VM API operation is not supported")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return instanceValidationError("VM API operation requires a deadline")
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return vmAPIBrokerError("set VM API deadline", err)
	}
	stop := context.AfterFunc(ctx, func() {
		// The caller still owns Close; an expired deadline interrupts partial HTTP I/O.
		_ = connection.SetDeadline(time.Now())
	})
	defer stop()
	request, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, strings.NewReader(body))
	if err != nil {
		return vmAPIBrokerError("construct fixed VM API request", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Close = true
	// CRITICAL: A failed write/read can have applied the mutation. Never replay it automatically.
	if err := request.Write(connection); err != nil {
		return vmAPIBrokerError("VM API operation outcome is unknown", errors.Join(err, ctx.Err()))
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 64<<10)), request)
	if err != nil {
		return vmAPIBrokerError("VM API operation outcome is unknown", errors.Join(err, ctx.Err()))
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusNoContent {
		return vmAPIBrokerError("Firecracker rejected the named VM API operation", nil)
	}
	return nil
}

func vmAPIBrokerError(message string, cause error) error {
	return errs.WrapMsg(errs.CodeFirecrackerClientError, message, cause)
}
