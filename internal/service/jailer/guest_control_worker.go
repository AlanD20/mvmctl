package jailer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// GuestControlWorkerServer admits worker records only after genuine current VM admission.
// This foundation does not spawn a worker, publish readiness, or register a private entrypoint.
type GuestControlWorkerServer struct{}

func NewGuestControlWorkerServer() *GuestControlWorkerServer { return &GuestControlWorkerServer{} }

type GuestControlWorkerLease struct{ state *guestControlWorkerLeaseState }

type guestControlWorkerLeaseState struct {
	mu            sync.Mutex // Guards the retained child, worker lock, publication and cached release state.
	hold          *authorityfs.TopologyHold
	principal     GuestControlPrincipal
	operationBoot string
	cleanupOnly   bool
	store         *guestControlWorkerStore
	released      bool
	releaseErr    error
}

// GuestControlWorkerSnapshot is observed lifecycle state, never process or resource authority.
type GuestControlWorkerSnapshot struct {
	Principal  GuestControlPrincipal
	Generation uint64
	Revision   uint64
	Phase      string
	Found      bool
}

// Lock acquires VM child -> worker record, never the opposite order. The caller owns the Jailer
// parent and must Release this lease before parent Release and before any child bootstrap gate.
func (server *GuestControlWorkerServer) Lock(
	ctx context.Context, admission *FloatingIPAdmissionLease,
) (_ *GuestControlWorkerLease, returnErr error) {
	if ctx == nil || admission == nil || admission.state == nil {
		return nil, instanceValidationError("worker admission context and VM authority are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return nil, err
	}
	state := &guestControlWorkerLeaseState{hold: hold}
	lease := &GuestControlWorkerLease{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	admitted := admission.state
	admitted.mu.Lock()
	err = admitted.recheck(ctx)
	if err == nil {
		r := admitted.record
		state.principal = GuestControlPrincipal{OwnerUID: r.ownerUID, OwnerGID: r.launch.ownerGID, VMID: r.vmID,
			BootID: r.launch.hostBootID, LaunchGeneration: r.launchGeneration, ExecutionUID: r.active.userIDs.effective}
	}
	admitted.mu.Unlock()
	if err != nil {
		return nil, err
	}
	state.operationBoot = state.principal.BootID
	state.store, err = openGuestControlWorkerStore(ctx, realInstanceAuthorityDeps(),
		productionInstanceAuthorityPolicy(),
		state.principal.OwnerUID, state.principal.VMID)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (state *guestControlWorkerLeaseState) ready(ctx context.Context) error {
	if ctx == nil || state.released || state.hold == nil || state.store == nil || state.store.uncertain {
		return instanceValidationError("worker lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, vm, err := state.hold.Binding()
	if err != nil || owner != state.principal.OwnerUID || vm.String() != state.principal.VMID {
		return instanceAtomicError("worker VM child binding differs", err)
	}
	boot, err := realProcessIdentityVerifierDeps().readBootID(ctx)
	if err != nil || boot != state.operationBoot {
		return instanceAtomicError("worker admission boot changed", err)
	}
	_, raw, found, err := state.store.read(ctx)
	if err != nil || found != state.store.found || !bytes.Equal(raw, state.store.raw) {
		return instanceAtomicError("worker record changed under lock", err)
	}
	return nil
}

func (lease *GuestControlWorkerLease) Snapshot(ctx context.Context) (GuestControlWorkerSnapshot, error) {
	if lease == nil || lease.state == nil {
		return GuestControlWorkerSnapshot{}, instanceValidationError("worker lease is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return GuestControlWorkerSnapshot{}, err
	}
	r := state.store.record
	return GuestControlWorkerSnapshot{Principal: r.principal(), Generation: r.Generation, Revision: r.Revision,
		Phase: r.Phase, Found: state.store.found}, nil
}

func (lease *GuestControlWorkerLease) Prepare(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceValidationError("worker lease is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if state.cleanupOnly {
		return instanceValidationError("worker cleanup authority cannot prepare a process")
	}
	before := state.store.record
	if state.store.found && before.Phase != "stopped" {
		return instanceValidationError("worker already has an unfinished lifecycle")
	}
	generation, revision := uint64(1), uint64(1)
	if state.store.found {
		if before.Generation == ^uint64(0) || before.Revision == ^uint64(0) {
			return instanceValidationError("worker generation is exhausted")
		}
		generation, revision = before.Generation+1, before.Revision+1
	}
	p := state.principal
	return state.store.persist(ctx, guestControlWorkerRecord{Schema: 1,
		ResourceOwnerUID: p.OwnerUID, ResourceOwnerGID: p.OwnerGID,
		VMID: p.VMID, BootID: p.BootID, LaunchGeneration: p.LaunchGeneration, ExecutionUID: p.ExecutionUID,
		Generation: generation, Revision: revision, Phase: "prepared"})
}

// RecordSpawn accepts only a private closed-spawn capability, never a caller PID or pidfd field.
// Parent must durably record it, then release worker and VM authority before releasing the child gate.
func (lease *GuestControlWorkerLease) RecordSpawn(ctx context.Context, process *GuestControlWorkerProcess) error {
	if lease == nil || lease.state == nil || process == nil || process.state == nil {
		return instanceValidationError("worker closed-spawn authority is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if state.cleanupOnly {
		return instanceValidationError("worker cleanup authority cannot record a spawn")
	}
	spawn := process.state
	spawn.mu.Lock()
	defer spawn.mu.Unlock()
	if spawn.released || spawn.principal != state.principal || state.store.record.Phase != "prepared" ||
		state.store.record.principal() != state.principal {
		return instanceValidationError("worker spawn scope differs from prepared record")
	}
	actual, err := observeGuestControlWorkerProcess(ctx, spawn.pidfd,
		instanceProcessLocator{pid: spawn.record.PID, startTimeTicks: spawn.record.StartTimeTicks},
		state.principal.VMID, spawn.record.Executable)
	if err != nil {
		return err
	}
	if actual != spawn.record {
		return instanceValidationError("worker spawn proof changed")
	}
	target := state.store.record
	if target.Revision == ^uint64(0) {
		return instanceValidationError("worker revision is exhausted")
	}
	target.Revision++
	target.Phase = "spawned"
	target.Process = &actual
	return state.store.persist(ctx, target)
}

// StopVerified fences the exact fixed root record before signaling a freshly verified pidfd.
// It cannot select an arbitrary process, and a replaced PID/executable fails without signaling.
func (lease *GuestControlWorkerLease) StopVerified(ctx context.Context) (returnErr error) {
	if lease == nil || lease.state == nil {
		return instanceValidationError("worker lease is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	if !state.store.found || state.store.record.Phase == "stopped" {
		return nil
	}
	target := state.store.record
	if target.Phase != "stopping" {
		if target.Revision == ^uint64(0) {
			return instanceValidationError("worker revision is exhausted")
		}
		target.Phase = "stopping"
		target.Revision++
		if err := state.store.persist(ctx, target); err != nil {
			return err
		}
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, state.store.record)
	if err != nil {
		return err
	}
	if !absent {
		defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
		if err := terminateProcessThroughPIDFD(ctx, fd); err != nil {
			return err
		}
		if err := waitAndReapProcessThroughPIDFD(ctx, fd); err != nil {
			return err
		}
	}
	target = state.store.record
	if target.Revision == ^uint64(0) {
		return instanceValidationError("worker revision is exhausted")
	}
	target.Phase = "stopped"
	target.Revision++
	return state.store.persist(ctx, target)
}

func (lease *GuestControlWorkerLease) Release(ctx context.Context) error {
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
	if state.store != nil {
		state.releaseErr = state.store.close(cleanup)
	}
	state.releaseErr = errors.Join(state.releaseErr, state.hold.Release(cleanup))
	state.released = true
	return state.releaseErr
}

// lockGuestControlWorkerForCleanup consumes child authority from an ALREADY held cleanup VM lock.
// It never requires a live old VMM and cannot prepare or record a replacement worker. Lifecycle
// integration will call it before clearing the VM's root record; this foundation does not invoke it.
func lockGuestControlWorkerForCleanup(
	ctx context.Context, cleanup *cleanupLease,
) (_ *GuestControlWorkerLease, returnErr error) {
	if ctx == nil || cleanup == nil || cleanup.state == nil {
		return nil, instanceValidationError("worker cleanup VM authority is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parent := cleanup.state
	parent.mu.Lock()
	if parent.released || parent.vmLock == nil || !parent.vmLock.isActive() {
		parent.mu.Unlock()
		return nil, instanceValidationError("worker cleanup VM lock is unavailable")
	}
	record := parent.record
	retained, err := parent.vmLock.retainTopologyAuthority(ctx)
	parent.mu.Unlock()
	if err != nil {
		return nil, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return nil, errors.Join(err, retained.Release(context.WithoutCancel(ctx)))
	}
	state := &guestControlWorkerLeaseState{hold: hold, cleanupOnly: true,
		principal: GuestControlPrincipal{OwnerUID: record.ownerUID, VMID: record.vmID}}
	lease := &GuestControlWorkerLease{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	state.operationBoot, err = realProcessIdentityVerifierDeps().readBootID(ctx)
	if err != nil {
		return nil, err
	}
	state.store, err = openGuestControlWorkerStore(ctx, realInstanceAuthorityDeps(),
		productionInstanceAuthorityPolicy(), record.ownerUID, record.vmID)
	if err != nil {
		return nil, err
	}
	return lease, nil
}
