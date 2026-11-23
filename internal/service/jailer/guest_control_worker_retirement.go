package jailer

import (
	"context"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// retireGuestControlWorkerForCleanup runs beneath the already-held VM cleanup authority.
func retireGuestControlWorkerForCleanup(ctx context.Context, cleanup *cleanupLease) (returnErr error) {
	if cleanup == nil || cleanup.state == nil {
		return instanceValidationError("worker cleanup authority is unavailable")
	}
	cleanup.state.mu.Lock()
	owner, vmID := cleanup.state.record.ownerUID, cleanup.state.record.vmID
	cleanup.state.mu.Unlock()
	found, err := guestControlWorkerRecordExists(ctx, owner, vmID)
	if err != nil || !found {
		return err
	}
	worker, err := lockGuestControlWorkerForCleanup(ctx, cleanup)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	return worker.StopVerified(ctx)
}

func retireGuestControlWorkerForOwnedInstance(ctx context.Context, locked *lockedInstance) (returnErr error) {
	found, err := guestControlWorkerRecordExists(ctx, locked.record.ownerUID, locked.record.vmID)
	if err != nil || !found {
		return err
	}
	worker, err := lockGuestControlWorkerForOwnedInstance(ctx, locked)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	return worker.StopVerified(ctx)
}

// RetireGuestControlWorkerSelf runs only after the worker has closed its last session.
// It checks the exact self process under VM→child→worker authority and does not signal a PID.
func RetireGuestControlWorkerSelf(ctx context.Context, setup GuestControlWorkerSetup) (returnErr error) {
	p := setup.Principal
	locked, err := newInstanceAuthority().lockOwnedInstance(ctx, instanceCaller{uid: p.OwnerUID, gid: p.OwnerGID}, p.VMID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, locked.Release(context.WithoutCancel(ctx))) }()
	worker, err := lockGuestControlWorkerForOwnedInstance(ctx, locked)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	s := worker.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return err
	}
	record := s.store.record
	if !s.store.found || record.Phase == "stopped" {
		return nil
	}
	if record.principal() != p || record.Generation != setup.Generation || record.Process == nil ||
		record.Process.PID != os.Getpid() {
		return instanceValidationError("worker self-retirement record differs")
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
	if err != nil {
		return err
	}
	if absent {
		return instanceValidationError("worker self-retirement process is absent")
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if record.Phase != "stopping" {
		if record.Revision == ^uint64(0) {
			return instanceValidationError("worker revision is exhausted")
		}
		record.Phase = "stopping"
		record.Revision++
		if err := s.store.persist(ctx, record); err != nil {
			return err
		}
	}
	if record.Revision == ^uint64(0) {
		return instanceValidationError("worker revision is exhausted")
	}
	record.Phase = "stopped"
	record.Revision++
	return s.store.persist(ctx, record)
}

func lockGuestControlWorkerForOwnedInstance(
	ctx context.Context,
	locked *lockedInstance,
) (_ *GuestControlWorkerLease, returnErr error) {
	if ctx == nil || locked == nil || locked.vmLock == nil || !locked.vmLock.isActive() {
		return nil, instanceValidationError("worker retirement VM authority is unavailable")
	}
	retained, err := locked.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return nil, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return nil, errors.Join(err, retained.Release(context.WithoutCancel(ctx)))
	}
	state := &guestControlWorkerLeaseState{
		hold:        hold,
		cleanupOnly: true,
		principal:   GuestControlPrincipal{OwnerUID: locked.record.ownerUID, VMID: locked.record.vmID},
	}
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
	state.store, err = openGuestControlWorkerStore(
		ctx,
		realInstanceAuthorityDeps(),
		productionInstanceAuthorityPolicy(),
		locked.record.ownerUID,
		locked.record.vmID,
	)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

// Absence is only an optimization. Every existing record is reopened through retained VM authority.
func guestControlWorkerRecordExists(ctx context.Context, owner uint32, vmID string) (_ bool, returnErr error) {
	if ctx == nil || owner == 0 || !vmIDPattern.MatchString(vmID) {
		return false, instanceValidationError("worker record selector is invalid")
	}
	deps, policy := realInstanceAuthorityDeps(), productionInstanceAuthorityPolicy()
	fd, err := deps.open(ctx, policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return false, err
	}
	retained := []int{fd}
	defer func() {
		returnErr = errors.Join(returnErr, closeInstanceFDs(context.WithoutCancel(ctx), deps, retained))
	}()
	if err := verifyInstanceDirectory(
		ctx,
		deps,
		fd,
		"worker absence root",
		policy,
		instanceAncestorDirectory,
	); err != nil {
		return false, err
	}
	for i, name := range []string{"var", "lib", "mvmctl", "guest-control", strconv.FormatUint(uint64(owner), 10)} {
		class := instancePrivateDirectory
		if i < 2 {
			class = instanceAncestorDirectory
		}
		if i == 2 {
			class = instanceStateBaseDirectory
		}
		next, err := openInstanceDirectory(ctx, deps, fd, name, false, class, policy)
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		retained = append(retained, next)
		fd = next
	}
	record, err := deps.openAt(ctx, fd, vmID+".json", instanceReadFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	retained = append(retained, record)
	if err := verifyGuestControlWorkerFile(ctx, deps, record, "worker absence record", policy); err != nil {
		return false, err
	}
	return true, nil
}
