package jailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// VMStopAction requests graceful guest shutdown followed by exact pidfd cleanup.
const VMStopAction = "jailer-vm-stop"

// VMStopRequest contains only a VM selection and explicit graceful/forced intent.
type VMStopRequest struct {
	VMID  string `json:"vm_id"`
	Force bool   `json:"force"`
}

// Validate checks the closed VM selection.
func (request VMStopRequest) Validate() error {
	return (VMCleanupRequest{VMID: request.VMID}).Validate()
}

// UnmarshalJSON requires the force switch to be explicit.
func (request *VMStopRequest) UnmarshalJSON(raw []byte) error {
	var wire struct {
		VMID  string `json:"vm_id"`
		Force *bool  `json:"force"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Force == nil {
		return instanceValidationError("VM stop force is required")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return instanceValidationError("VM stop request has trailing JSON")
	}
	request.VMID, request.Force = wire.VMID, *wire.Force
	return nil
}

// Stop retains the recovered instance lock while asking the admitted API peer to
// shut down, waiting only on the exact Firecracker pidfd, and finishing cleanup.
func (server *VMLifecycleServer) Stop(ctx context.Context, ownerUID, ownerGID uint32, request VMStopRequest) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	cleanup, err := server.instances.beginLifecycleCleanup(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.VMID)
	if err != nil {
		return VMCleanupResult{}, err
	}
	if cleanup == nil {
		return VMCleanupResult{VMID: request.VMID, Cleaned: true}, nil
	}
	if !request.Force {
		if err := cleanup.requestGracefulStop(ctx); err != nil {
			return VMCleanupResult{}, err
		}
	}
	if err := finishLifecycleCleanup(ctx, cleanup, true); err != nil {
		return VMCleanupResult{}, err
	}
	return VMCleanupResult{VMID: request.VMID, Cleaned: true}, nil
}

// Stop preserves graceful intent without sending caller-selected paths or PID authority.
func (client *VMLifecycleClient) Stop(ctx context.Context, request VMStopRequest) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMStopAction, request, nil, 0,
		func(result VMCleanupResult) error { return result.Validate(VMCleanupRequest{VMID: request.VMID}) })
}

func (lease *cleanupLease) requestGracefulStop(ctx context.Context) error {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.record.lifecycle != instanceLifecycleCleaning || state.freshRecoveryAdmission != nil {
		return instanceStateError(state.record.vmID, "graceful stop requires admitted cleaning authority")
	}
	if state.record.active == nil || state.recoveredProcesses == nil {
		return nil
	}
	processes := state.recoveredProcesses.state
	processes.mu.Lock()
	defer processes.mu.Unlock()
	pidfd := -1
	for _, process := range processes.retained {
		if process.role == "Firecracker" && !process.reaped {
			pidfd = process.pidfd
		}
	}
	if pidfd < 0 {
		return nil
	}
	alive, err := processes.deps.pidfdAlive(ctx, pidfd)
	if err != nil || !alive {
		return err
	}
	if err := resolveCleanupManagedVMRuntimeLocked(ctx, state); err != nil {
		return err
	}
	if state.runtimeDirectory == nil {
		return nil
	}
	runtime := state.runtimeDirectory.state
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	path := fmt.Sprintf("/proc/self/fd/%d/%s", runtime.vmDirectoryFD, infra.VMFirecrackerAPISocketFilename)
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil
	} // Unavailable API falls back to exact retained-pidfd cleanup.
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return instanceStateError(state.record.vmID, "graceful stop API connection is not AF_UNIX")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return err
	}
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peerErr = admitStopAPIPeer(ctx, int(fd), state.record, pidfd) }); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	deadline := time.Now().Add(2 * time.Second)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	body := `{"action_type":"SendCtrlAltDel"}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker/actions", bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Close = true
	if err := request.Write(connection); err != nil {
		return nil
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 64<<10)), request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil
	}
	for time.Now().Before(deadline) {
		alive, err := processes.deps.pidfdAlive(ctx, pidfd)
		if err != nil || !alive {
			return err
		}
		if err := realProcessIdentityVerifierDeps().wait(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func admitStopAPIPeer(ctx context.Context, fd int, record instanceRecord, retainedPIDFD int) (returnErr error) {
	deps := realVMExecPeerAuthorityDeps()
	credentials, err := deps.peerCredentials(ctx, fd)
	if err != nil {
		return err
	}
	peerPIDFD, err := deps.peerPIDFD(ctx, fd)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(peerPIDFD)) }()
	if credentials == nil || int(credentials.Pid) != record.launch.process.pid || credentials.Uid != record.ownerUID || credentials.Gid != record.launch.ownerGID {
		return processVerificationError(record.vmID, "graceful API peer differs from admitted Firecracker", nil)
	}
	identity := realProcessIdentityVerifierDeps()
	if err := requireExactPIDFDLocator(ctx, identity, retainedPIDFD, record.launch.process, record.vmID, "Firecracker"); err != nil {
		return err
	}
	if err := requireExactPIDFDLocator(ctx, identity, peerPIDFD, record.launch.process, record.vmID, "Firecracker API peer"); err != nil {
		return err
	}
	if err := requireProcessAlive(ctx, deps.pidfdAlive, peerPIDFD, record.vmID, "Firecracker API peer"); err != nil {
		return err
	}
	return requireProcessAlive(ctx, deps.pidfdAlive, retainedPIDFD, record.vmID, "Firecracker")
}
