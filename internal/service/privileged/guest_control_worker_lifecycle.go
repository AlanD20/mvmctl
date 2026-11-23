package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// reconcileGuestControlWorker starts only for enabled exact root grants; last-grant removal retires it.
func reconcileGuestControlWorker(ctx context.Context, uid, gid uint32, vmID string) (returnErr error) {
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, uid, gid, jailer.FloatingIPAdmissionRequest{VMID: vmID})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return err
	}
	hint, err := guestControlWorkerGrantHint(ctx, admission, identity)
	if err != nil {
		return err
	}
	if !hint {
		return nil
	}
	active, err := guestControlWorkerEnabled(ctx, admission, identity)
	if err != nil {
		return err
	}
	worker, err := jailer.NewGuestControlWorkerServer().Lock(ctx, admission)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	if !active {
		return worker.StopVerified(ctx)
	}
	snapshot, err := worker.Snapshot(ctx)
	if err != nil {
		return err
	}
	live, err := worker.VerifyRunning(ctx)
	if err != nil {
		return err
	}
	if live && snapshot.Principal.BootID == identity.BootID &&
		snapshot.Principal.LaunchGeneration == identity.LaunchGeneration &&
		snapshot.Principal.ExecutionUID == identity.ExecutionUID {
		if snapshot.Phase == "ready" {
			return nil
		}
		if snapshot.Phase == "spawned" {
			return errs.New(
				errs.CodeValidationFailed,
				"Guest Control worker bootstrap is already in progress",
				errs.WithClass(errs.ClassConflict),
			)
		}
	}
	if snapshot.Found && snapshot.Phase != "stopped" {
		if err := worker.StopVerified(ctx); err != nil {
			return err
		}
	}
	if err := worker.Prepare(ctx); err != nil {
		return err
	}
	process, err := worker.SpawnBlocked(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, process.Release(context.WithoutCancel(ctx))) }()
	if err := worker.RecordSpawn(ctx, process); err != nil {
		return err
	}
	// CRITICAL: neither worker lock nor parent VM authority may cross the child bootstrap gate.
	if err := worker.Release(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	if err := admission.Release(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return process.Start(ctx)
}

func guestControlWorkerEnabled(
	ctx context.Context,
	admission *jailer.FloatingIPAdmissionLease,
	identity jailer.FloatingIPVMIdentity,
) (_ bool, returnErr error) {
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().
		OpenVM(ctx, hold, network.FloatingIPVMIncarnation{BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, vm.Release(context.WithoutCancel(ctx))) }()
	return vm.HasActiveGuestControlDelegations(ctx)
}

// grantFloatingIPDelegationWithWorker publishes the exact permission, releases all publication
// authority, then starts the worker. A startup failure never pretends the stored grant was rolled back.
func grantFloatingIPDelegationWithWorker(
	ctx context.Context, uid, gid uint32, request network.FloatingIPDelegationGrantRequest,
) (network.FloatingIPDelegationResult, error) {
	result, err := grantFloatingIPDelegation(ctx, uid, gid, request)
	if err != nil {
		return result, err
	}
	if err := reconcileGuestControlWorker(ctx, uid, gid, result.VMID); err != nil {
		return result, err
	}
	return result, nil
}

func guestControlWorkerGrantHint(
	ctx context.Context,
	admission *jailer.FloatingIPAdmissionLease,
	identity jailer.FloatingIPVMIdentity,
) (_ bool, returnErr error) {
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	return network.NewFloatingIPServer().
		HasCurrentGuestControlDelegations(ctx, hold, network.FloatingIPVMIncarnation{BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
}
