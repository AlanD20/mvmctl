package privileged

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// RunGuestControlWorker is a private inherited-descriptor entrypoint, before ordinary initialization.
func RunGuestControlWorker(ctx context.Context, args []string) (returnErr error) {
	setup, bootstrap, err := jailer.ReceiveGuestControlWorkerSetup(ctx, args)
	if err != nil {
		return err
	}
	defer func() {
		if bootstrap != nil {
			returnErr = errors.Join(returnErr, bootstrap.Close())
		}
	}()
	session, err := openGuestControlWorkerSession(ctx, setup)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := session.Close(context.WithoutCancel(ctx))
		retire, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, closeErr, jailer.RetireGuestControlWorkerSelf(retire, setup))
	}()
	if err := jailer.AdmitGuestControlWorker(ctx, setup, session, bootstrap); err != nil {
		return err
	}
	bootstrap = nil
	// The bootstrap transport is now closed. No VM/network/global mutation lease remains idle.
	for {
		if err := serveGuestControlWorkerSession(ctx, setup, session); err == nil {
			return nil
		}
		_ = session.Close(context.WithoutCancel(ctx))
		// Reconnect never replays the outstanding request. Effects with lost responses stay unknown.
		for {
			if err := jailer.RecheckGuestControlWorker(ctx, setup); err != nil {
				return err
			}
			if err := requireGuestControlWorkerGrants(ctx, setup.Principal); err != nil {
				return err
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			session, err = openGuestControlWorkerSession(ctx, setup)
			if err == nil {
				break
			}
		}
	}
}

func openGuestControlWorkerSession(
	ctx context.Context,
	setup jailer.GuestControlWorkerSetup,
) (*jailer.GuestControlSession, error) {
	if err := requireGuestControlWorkerGrants(ctx, setup.Principal); err != nil {
		return nil, err
	}
	p := setup.Principal
	session, err := jailer.NewHostVsockServer().
		OpenGuestControlSession(ctx, p.OwnerUID, p.OwnerGID, jailer.HostVsockOpenRequest{VMID: p.VMID})
	if err != nil {
		return nil, err
	}
	actual, err := session.Principal()
	if err != nil || actual != p {
		return nil, errors.Join(err, session.Close(context.WithoutCancel(ctx)), errGuestControlWorkerUnavailable())
	}
	return session, nil
}

func serveGuestControlWorkerSession(
	ctx context.Context,
	setup jailer.GuestControlWorkerSetup,
	session *jailer.GuestControlSession,
) error {
	scoped, cancel := context.WithCancel(ctx)
	defer cancel()
	grantsStopped := make(chan struct{})
	go func() {
		defer close(grantsStopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-scoped.Done():
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(scoped, 30*time.Second)
				err := requireGuestControlWorkerGrants(check, setup.Principal)
				cancel()
				if err != nil {
					_ = session.Close(context.Background())
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-grantsStopped }()
	exited := make(chan error, 1)
	go func() {
		err := session.WaitVMMExit(scoped)
		if scoped.Err() == nil {
			_ = session.Close(context.Background())
		}
		exited <- err
	}()
	defer func() { cancel(); <-exited }()
	for {
		request, err := session.ReceiveRequest(scoped)
		if err != nil {
			return err
		}
		// Fresh operation admission and exact principal are retained only for the request effect.
		operation, cancel := context.WithTimeout(scoped, 30*time.Second)
		result, effectsStarted, cause := executeAdmittedGuestControlRequest(operation, setup.Principal, request)
		response := guestControlWorkerResponse(request, result, effectsStarted, cause)
		err = session.SendResponse(operation, response)
		cancel()
		if err != nil {
			return err
		}
	}
}

func requireGuestControlWorkerGrants(ctx context.Context, p jailer.GuestControlPrincipal) (returnErr error) {
	gate, err := hostadmission.AcquireOperation(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, gate.Release(context.WithoutCancel(ctx))) }()
	if err := authorizeCaller(callerIdentity{uid: p.OwnerUID, gid: p.OwnerGID}, realAuthorizationDeps()); err != nil {
		return err
	}
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, p.OwnerUID, p.OwnerGID, jailer.FloatingIPAdmissionRequest{VMID: p.VMID})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return err
	}
	if err := requireGuestControlRequestIncarnation(p, identity); err != nil {
		return err
	}
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().
		OpenVM(ctx, hold, network.FloatingIPVMIncarnation{BootID: p.BootID, LaunchGeneration: p.LaunchGeneration, ExecutionUID: p.ExecutionUID})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, vm.Release(context.WithoutCancel(ctx))) }()
	active, err := vm.HasActiveGuestControlDelegations(ctx)
	if err != nil {
		return err
	}
	if !active {
		return errGuestControlWorkerUnavailable()
	}
	return nil
}

func errGuestControlWorkerUnavailable() error {
	return errs.New(errs.CodeValidationFailed, "Guest Control worker has no current enabled authority")
}

func executeAdmittedGuestControlRequest(
	ctx context.Context,
	p jailer.GuestControlPrincipal,
	request guestcontrolwire.Request,
) (result network.FloatingIPGuestResult, effectsStarted bool, returnErr error) {
	gate, err := hostadmission.AcquireOperation(ctx)
	if err != nil {
		return result, false, err
	}
	defer func() {
		cleanup := gate.Release(context.WithoutCancel(ctx))
		if cleanup != nil && request.Operation != guestcontrolwire.Inspect {
			effectsStarted = true
		}
		returnErr = errors.Join(returnErr, cleanup)
	}()
	if err := authorizeCaller(callerIdentity{uid: p.OwnerUID, gid: p.OwnerGID}, realAuthorizationDeps()); err != nil {
		return result, false, err
	}
	return executeGuestControlRequest(ctx, p, request)
}
