package jailer

import (
	"context"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type vmExecPeerAuthorityDeps struct {
	peerCredentials func(context.Context, int) (*unix.Ucred, error)
	peerPIDFD       func(context.Context, int) (int, error)
	descriptorFlags func(context.Context, int) (int, error)
	deriveLocator   func(context.Context, int) (instanceProcessLocator, error)
	pidfdAlive      func(context.Context, int) (bool, error)
	close           func(context.Context, int) error
}

type vmExecFirecrackerPeer struct {
	state *vmExecFirecrackerPeerState
}

type vmExecFirecrackerPeerState struct {
	// mu guards the retained peer pidfd and its complete close-once lifecycle.
	mu         sync.Mutex
	deps       vmExecPeerAuthorityDeps
	vmID       string
	role       string
	pidfd      int
	released   bool
	releaseErr error
}

// CRITICAL: admitVMExecFirecrackerPeer binds one borrowed, structurally admitted AF_UNIX connection to the exact
// retained Firecracker runtime. The connection descriptor remains borrowed; only the kernel-returned peer pidfd is
// retained.
func admitVMExecFirecrackerPeer(
	ctx context.Context,
	deps vmExecPeerAuthorityDeps,
	identityDeps processIdentityVerifierDeps,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
	connectionFD int,
	role string,
) (_ *vmExecFirecrackerPeer, returnErr error) {
	if ctx == nil {
		return nil, vmExecPeerError(record.vmID, role, "peer admission context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, vmExecPeerError(record.vmID, role, "peer admission was cancelled", err)
	}
	if err := validateVMExecPeerAuthorityDeps(deps); err != nil {
		return nil, vmExecPeerError(record.vmID, role, "peer authority dependencies are incomplete", err)
	}
	if err := validateProcessIdentityVerifierDeps(identityDeps); err != nil {
		return nil, vmExecPeerError(record.vmID, role, "process identity dependencies are incomplete", err)
	}
	if connectionFD < 0 {
		return nil, vmExecPeerError(record.vmID, role, "connection descriptor is invalid", nil)
	}
	if role != "source" && role != "destination" {
		return nil, vmExecPeerError(record.vmID, role, "peer role is invalid", nil)
	}
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	if record.lifecycle != instanceLifecycleActive || record.launch == nil || record.active == nil {
		return nil, vmExecPeerError(record.vmID, role, "active Firecracker identity is incomplete", nil)
	}
	if processes == nil || processes.state == nil {
		return nil, vmExecPeerError(record.vmID, role, "retained process authority is incomplete", nil)
	}
	if err := validateVMExecRetainedProcessAuthority(processes, record, role); err != nil {
		return nil, err
	}

	credentials, err := deps.peerCredentials(ctx, connectionFD)
	if err != nil {
		return nil, vmExecPeerError(record.vmID, role, "read connection peer credentials", err)
	}
	peerPIDFD, err := deps.peerPIDFD(ctx, connectionFD)
	if err != nil {
		return nil, vmExecPeerError(record.vmID, role, "acquire connection peer pidfd", err)
	}
	if peerPIDFD < 0 {
		return nil, vmExecPeerError(record.vmID, role, "connection peer pidfd is unavailable", nil)
	}
	peer := &vmExecFirecrackerPeer{state: &vmExecFirecrackerPeerState{
		deps: deps, vmID: record.vmID, role: role, pidfd: peerPIDFD,
	}}
	committed := false
	defer func() {
		if committed {
			return
		}
		returnErr = appendVMExecSessionError(
			returnErr,
			"release rejected "+role+" Firecracker peer authority",
			peer.Release(context.WithoutCancel(ctx)),
		)
	}()

	flags, err := deps.descriptorFlags(ctx, peerPIDFD)
	if err != nil {
		return nil, vmExecPeerError(record.vmID, role, "inspect peer pidfd flags", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return nil, vmExecPeerError(record.vmID, role, "peer pidfd does not have close-on-exec", nil)
	}
	if credentials == nil || credentials.Pid <= 0 {
		return nil, vmExecPeerError(record.vmID, role, "connection peer credentials are invalid", nil)
	}
	if int(credentials.Pid) != record.launch.process.pid {
		return nil, vmExecPeerError(record.vmID, role, "connection peer PID does not match Firecracker", nil)
	}
	if credentials.Uid != record.active.userIDs.effective ||
		credentials.Gid != record.active.groupIDs.effective {
		return nil, vmExecPeerError(record.vmID, role, "connection peer credentials do not match Firecracker", nil)
	}
	if err := verifyVMExecFirecrackerPeerIdentity(
		ctx,
		deps,
		identityDeps,
		record,
		processes,
		peerPIDFD,
		role,
	); err != nil {
		return nil, err
	}

	committed = true
	return peer, nil
}

// CRITICAL: verifyVMExecFirecrackerPeerIdentity brackets the complete runtime identity proof with exact pidfd locator
// and liveness checks, preventing a stale or substituted AF_UNIX peer from satisfying admission.
func verifyVMExecFirecrackerPeerIdentity(
	ctx context.Context,
	deps vmExecPeerAuthorityDeps,
	identityDeps processIdentityVerifierDeps,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
	peerPIDFD int,
	role string,
) error {
	derived, err := deps.deriveLocator(ctx, peerPIDFD)
	if err != nil {
		return vmExecPeerError(record.vmID, role, "derive connection peer locator", err)
	}
	if derived != record.launch.process {
		return vmExecPeerError(record.vmID, role, "connection peer pidfd does not match Firecracker locator", nil)
	}
	alive, err := deps.pidfdAlive(ctx, peerPIDFD)
	if err != nil {
		return vmExecPeerError(record.vmID, role, "inspect connection peer liveness", err)
	}
	if !alive {
		return vmExecPeerError(record.vmID, role, "connection peer is not live", nil)
	}
	if err := verifyRecoveredActiveIdentity(ctx, identityDeps, record); err != nil {
		return err
	}
	if err := processes.verifyLocators(ctx); err != nil {
		return err
	}
	derived, err = deps.deriveLocator(ctx, peerPIDFD)
	if err != nil {
		return vmExecPeerError(record.vmID, role, "rederive connection peer locator", err)
	}
	if derived != record.launch.process {
		return vmExecPeerError(record.vmID, role, "connection peer locator changed during admission", nil)
	}
	alive, err = deps.pidfdAlive(ctx, peerPIDFD)
	if err != nil {
		return vmExecPeerError(record.vmID, role, "recheck connection peer liveness", err)
	}
	if !alive {
		return vmExecPeerError(record.vmID, role, "connection peer exited during admission", nil)
	}
	return nil
}

// CRITICAL: validateVMExecRetainedProcessAuthority proves that the retained process set is complete, exact, and still
// owned by the same VM admission before it participates in peer binding.
func validateVMExecRetainedProcessAuthority(
	processes *reopenedRecordedProcesses,
	record instanceRecord,
	role string,
) error {
	if processes == nil || processes.state == nil || record.launch == nil {
		return vmExecPeerError(record.vmID, role, "retained process authority is incomplete", nil)
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmID != record.vmID {
		return vmExecPeerError(record.vmID, role, "retained process authority does not match Firecracker", nil)
	}
	if err := validateProcessRecoveryDeps(state.deps); err != nil {
		return vmExecPeerError(record.vmID, role, "retained process authority dependencies are incomplete", err)
	}
	wantCount := 1
	if record.launch.relay.kind == instanceRelayPresent {
		wantCount++
	}
	if len(state.retained) != wantCount || !vmExecRetainedProcessMatches(
		state.retained[0],
		"Firecracker",
		record.launch.process,
	) {
		return vmExecPeerError(record.vmID, role, "retained process authority is not complete", nil)
	}
	if wantCount == 2 && !vmExecRetainedProcessMatches(
		state.retained[1],
		"relay",
		record.launch.relay.locator,
	) {
		return vmExecPeerError(record.vmID, role, "retained process authority is not complete", nil)
	}
	return nil
}

func vmExecRetainedProcessMatches(
	process recoveredRecordedProcess,
	role string,
	locator instanceProcessLocator,
) bool {
	return process.role == role && process.locator == locator && process.pidfd >= 0 &&
		!process.signalSent && !process.reaped
}

func validateVMExecPeerAuthorityDeps(deps vmExecPeerAuthorityDeps) error {
	if deps.peerCredentials == nil || deps.peerPIDFD == nil || deps.descriptorFlags == nil ||
		deps.deriveLocator == nil || deps.pidfdAlive == nil || deps.close == nil {
		return vmExecSessionError("one or more Firecracker peer effects are unavailable", nil)
	}
	return nil
}

// CRITICAL: recheck repeats the complete Firecracker identity proof through the retained peer and process authorities.
func (peer *vmExecFirecrackerPeer) recheck(
	ctx context.Context,
	identityDeps processIdentityVerifierDeps,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
) error {
	if peer == nil || peer.state == nil {
		return vmExecPeerError(record.vmID, "retained", "peer authority is unavailable", nil)
	}
	state := peer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if ctx == nil {
		return vmExecPeerError(record.vmID, state.role, "peer recheck context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return vmExecPeerError(record.vmID, state.role, "peer recheck was cancelled", err)
	}
	if state.released || state.pidfd < 0 || state.vmID != record.vmID {
		return vmExecPeerError(record.vmID, state.role, "retained peer authority does not match Firecracker", nil)
	}
	if err := validateVMExecPeerAuthorityDeps(state.deps); err != nil {
		return vmExecPeerError(record.vmID, state.role, "retained peer authority dependencies are incomplete", err)
	}
	if err := validateProcessIdentityVerifierDeps(identityDeps); err != nil {
		return vmExecPeerError(record.vmID, state.role, "process identity dependencies are incomplete", err)
	}
	if err := validateVMExecRetainedProcessAuthority(processes, record, state.role); err != nil {
		return err
	}
	return verifyVMExecFirecrackerPeerIdentity(
		ctx,
		state.deps,
		identityDeps,
		record,
		processes,
		state.pidfd,
		state.role,
	)
}

// CRITICAL: recheckLocatorAndLiveness is the bounded final check used after both complete identity proofs. It observes
// the retained peer pidfd directly, so recycled numeric credentials cannot satisfy the session's last-look sweep.
func (peer *vmExecFirecrackerPeer) recheckLocatorAndLiveness(
	ctx context.Context,
	record instanceRecord,
) error {
	if peer == nil || peer.state == nil {
		return vmExecPeerError(record.vmID, "retained", "peer authority is unavailable", nil)
	}
	state := peer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if ctx == nil {
		return vmExecPeerError(record.vmID, state.role, "peer recheck context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return vmExecPeerError(record.vmID, state.role, "peer recheck was cancelled", err)
	}
	if record.launch == nil || state.released || state.pidfd < 0 || state.vmID != record.vmID {
		return vmExecPeerError(record.vmID, state.role, "retained peer authority does not match Firecracker", nil)
	}
	if err := validateVMExecPeerAuthorityDeps(state.deps); err != nil {
		return vmExecPeerError(record.vmID, state.role, "retained peer authority dependencies are incomplete", err)
	}
	derived, err := state.deps.deriveLocator(ctx, state.pidfd)
	if err != nil {
		return vmExecPeerError(record.vmID, state.role, "rederive retained connection peer locator", err)
	}
	if derived != record.launch.process {
		return vmExecPeerError(
			record.vmID,
			state.role,
			"retained connection peer locator no longer matches Firecracker",
			nil,
		)
	}
	alive, err := state.deps.pidfdAlive(ctx, state.pidfd)
	if err != nil {
		return vmExecPeerError(record.vmID, state.role, "recheck retained connection peer liveness", err)
	}
	if !alive {
		return vmExecPeerError(record.vmID, state.role, "retained connection peer is not live", nil)
	}
	return nil
}

// Release closes the retained peer pidfd once across copied capabilities and caches the first classified result.
func (peer *vmExecFirecrackerPeer) Release(ctx context.Context) error {
	if peer == nil || peer.state == nil {
		return nil
	}
	state := peer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	} else {
		state.releaseErr = vmExecPeerError(state.vmID, state.role, "peer release context is required", nil)
	}
	if closeErr := state.deps.close(cleanupCtx, state.pidfd); closeErr != nil {
		primary := vmExecPeerError(state.vmID, state.role, "close retained peer pidfd", closeErr)
		state.releaseErr = appendVMExecSessionError(
			state.releaseErr,
			"close retained "+state.role+" Firecracker peer pidfd",
			primary,
		)
	}
	state.pidfd = -1
	state.released = true
	return state.releaseErr
}

func vmExecPeerError(vmID string, role string, message string, cause error) *errs.DomainError {
	result := vmExecSessionError("vm-exec "+role+" Firecracker "+message, cause)
	result.Entity = vmID
	return result
}
