package jailer

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// FloatingIPAdmissionServer admits one current, owned VMM independently of guest-agent availability.
type FloatingIPAdmissionServer struct{ instances *instanceAuthority }

// NewFloatingIPAdmissionServer constructs the root-owned instance authority.
func NewFloatingIPAdmissionServer() *FloatingIPAdmissionServer {
	return &FloatingIPAdmissionServer{instances: newInstanceAuthority()}
}

// FloatingIPAdmissionRequest selects a VM; it supplies no principal or incarnation evidence.
type FloatingIPAdmissionRequest struct{ VMID string }

// Validate requires one canonical VM selector and does not authenticate it.
func (request FloatingIPAdmissionRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("floating IP admission requires a canonical VM ID")
	}
	return nil
}

// FloatingIPVMIdentity is a root-derived observation, not a capability to perform network effects.
type FloatingIPVMIdentity struct {
	OwnerUID         uint32
	VMID             string
	BootID           string
	LaunchGeneration uint64
	ExecutionUID     uint32
}

// FloatingIPAdmissionLease retains the VM lock, process proof and protected API peer.
// THREAD-SAFE: copies share one state and close-once lifecycle.
type FloatingIPAdmissionLease struct{ state *floatingIPAdmissionState }

type floatingIPAdmissionState struct {
	// mu guards every retained resource and lifecycle field below.
	mu           sync.Mutex
	active       *activeLease
	record       instanceRecord
	processes    *reopenedRecordedProcesses
	connection   net.Conn
	directory    int
	peerPIDFD    int
	identityDeps processIdentityVerifierDeps
	peerDeps     vmExecPeerAuthorityDeps
	released     bool
	releaseErr   error
}

// Open authenticates the fixed protected Firecracker API endpoint without sending an API operation.
func (server *FloatingIPAdmissionServer) Open(
	ctx context.Context, ownerUID, ownerGID uint32, request FloatingIPAdmissionRequest,
) (_ *FloatingIPAdmissionLease, returnErr error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, instanceValidationError("floating IP admission context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	active, err := server.instances.LockActive(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.VMID)
	if err != nil {
		return nil, err
	}
	state := &floatingIPAdmissionState{active: active, record: active.snapshot(), directory: -1, peerPIDFD: -1,
		identityDeps: realProcessIdentityVerifierDeps(), peerDeps: realVMExecPeerAuthorityDeps()}
	lease := &FloatingIPAdmissionLease{state: state}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	record := state.record
	if err := requireFloatingIPExecutionIdentity(record); err != nil {
		return nil, err
	}
	bootID, err := state.identityDeps.readBootID(ctx)
	if err != nil || bootID != record.launch.hostBootID {
		return nil, processVerificationError(record.vmID, "floating IP host boot identity mismatch", err)
	}
	state.processes, err = reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
	if err != nil {
		return nil, err
	}
	if _, err := verifyRecoveredActiveIdentity(ctx, state.identityDeps, record); err != nil {
		return nil, err
	}
	if err := state.processes.verifyLocators(ctx); err != nil {
		return nil, err
	}
	state.directory, state.connection, err = openVMAPIConnection(ctx, record, false)
	if err != nil {
		return nil, err
	}
	state.peerPIDFD, err = admitVMAPIPeer(ctx, state.connection, record, state.processes)
	if err != nil {
		return nil, err
	}
	accepted = true
	return lease, nil
}

func requireFloatingIPExecutionIdentity(record instanceRecord) error {
	if record.launch == nil || record.active == nil || record.active.userIDs.effective == 0 ||
		record.active.groupIDs.effective == 0 || record.active.userIDs.effective == record.ownerUID ||
		record.active.groupIDs.effective == record.launch.ownerGID {
		return instanceStateError(record.vmID, "floating IP admission requires isolated execution identity")
	}
	return nil
}

// Identity observes the admitted incarnation while its authority remains retained.
func (lease *FloatingIPAdmissionLease) Identity(ctx context.Context) (FloatingIPVMIdentity, error) {
	if lease == nil || lease.state == nil {
		return FloatingIPVMIdentity{}, instanceStateError("", "floating IP admission is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.recheck(ctx); err != nil {
		return FloatingIPVMIdentity{}, err
	}
	r := state.record
	return FloatingIPVMIdentity{OwnerUID: r.ownerUID, VMID: r.vmID, BootID: r.launch.hostBootID,
		LaunchGeneration: r.launchGeneration, ExecutionUID: r.active.userIDs.effective}, nil
}

// Recheck proves that the retained peer still names the same live VMM incarnation.
func (lease *FloatingIPAdmissionLease) Recheck(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "floating IP admission is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.recheck(ctx)
}

func (state *floatingIPAdmissionState) recheck(ctx context.Context) error {
	if ctx == nil {
		return instanceValidationError("floating IP recheck context is required")
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("recheck floating IP admission", err)
	}
	if state.released || state.active == nil || state.peerPIDFD < 0 {
		return instanceStateError(state.record.vmID, "floating IP admission is unavailable")
	}
	bootID, err := state.identityDeps.readBootID(ctx)
	if err != nil || state.record.launch == nil || bootID != state.record.launch.hostBootID {
		return processVerificationError(state.record.vmID, "floating IP host boot identity mismatch", err)
	}
	if err := validateVMExecRetainedProcessAuthority(state.processes, state.record, "floating IP"); err != nil {
		return err
	}
	return verifyVMExecFirecrackerPeerIdentity(ctx, state.peerDeps, state.identityDeps,
		state.record, state.processes, state.peerPIDFD, "floating IP")
}

// RetainTopologyHold transfers genuine child authority for the later network transaction.
// The consumer must release it before releasing this admission; no network lock is acquired here.
func (lease *FloatingIPAdmissionLease) RetainTopologyHold(ctx context.Context) (*authorityfs.TopologyHold, error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "floating IP admission is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.recheck(ctx); err != nil {
		return nil, err
	}
	active := state.active.state
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.released || active.vmLock == nil {
		return nil, instanceStateError(state.record.vmID, "floating IP VM lock is unavailable")
	}
	retained, err := active.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return nil, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return nil, errors.Join(err, retained.Release(context.WithoutCancel(ctx)))
	}
	return hold, nil
}

// Release rejects live child holds without consuming any proof, then closes resources exactly once.
func (lease *FloatingIPAdmissionLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanup := context.Background()
	if ctx != nil {
		cleanup = context.WithoutCancel(ctx)
	}
	if state.active != nil && state.active.state != nil {
		active := state.active.state
		active.mu.Lock()
		var err error
		if active.vmLock != nil {
			err = active.vmLock.Release(cleanup)
		}
		active.mu.Unlock()
		if errors.Is(err, authorityfs.ErrTopologyRetained) {
			return err
		}
		state.releaseErr = errors.Join(state.releaseErr, err)
	}
	if ctx == nil {
		state.releaseErr = errors.Join(state.releaseErr, instanceValidationError("floating IP release context is required"))
	}
	if state.peerPIDFD >= 0 {
		state.releaseErr = errors.Join(state.releaseErr, unix.Close(state.peerPIDFD))
	}
	if state.connection != nil {
		state.releaseErr = errors.Join(state.releaseErr, state.connection.Close())
	}
	if state.directory >= 0 {
		state.releaseErr = errors.Join(state.releaseErr, unix.Close(state.directory))
	}
	if state.processes != nil {
		state.releaseErr = errors.Join(state.releaseErr, state.processes.Release(cleanup))
	}
	if state.active != nil {
		state.releaseErr = errors.Join(state.releaseErr, state.active.Release(cleanup))
	}
	state.released = true
	return state.releaseErr
}
