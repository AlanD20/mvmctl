package privileged

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// RunGuestControlWorker is a private inherited-descriptor entrypoint, before ordinary initialization.
func RunGuestControlWorker(ctx context.Context, args []string) (returnErr error) {
	startup, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()
	setup, bootstrap, err := jailer.ReceiveGuestControlWorkerSetup(startup, args)
	if err != nil {
		return err
	}
	defer func() {
		if bootstrap != nil {
			returnErr = errors.Join(returnErr, bootstrap.Close())
		}
	}()
	session, err := waitGuestControlWorkerInitialSession(startup, setup, openGuestControlWorkerSession)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := session.Close(context.WithoutCancel(ctx))
		retire, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, closeErr, jailer.RetireGuestControlWorkerSelf(retire, setup))
	}()
	if err := jailer.AdmitGuestControlWorker(startup, setup, session, bootstrap); err != nil {
		return err
	}
	bootstrap = nil
	cancelStartup()
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

// Each attempt reopens full current grant, VMM and authenticated principal admission.
// Only an explicitly classified pre-TLS refusal may wait; no feature request is replayed.
func waitGuestControlWorkerInitialSession(
	ctx context.Context,
	setup jailer.GuestControlWorkerSetup,
	openSession func(context.Context, jailer.GuestControlWorkerSetup) (*jailer.GuestControlSession, error),
) (*jailer.GuestControlSession, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, err := openSession(ctx, setup)
		if err == nil {
			return session, nil
		}
		domain := errs.AsDomainError(err)
		if domain == nil || domain.Code != errs.CodeAgentUnreachable || domain.Class != errs.ClassRetryable {
			return nil, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func requireGuestControlWorkerSessionPrincipal(expected, actual jailer.GuestControlPrincipal) error {
	if actual != expected {
		return errGuestControlWorkerUnavailable()
	}
	return nil
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
	if principalErr := requireGuestControlWorkerSessionPrincipal(p, actual); err != nil || principalErr != nil {
		return nil, errors.Join(err, session.Close(context.WithoutCancel(ctx)), principalErr)
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
	type received struct {
		request   guestcontrolwire.Request
		err       error
		responded chan struct{}
	}
	requests := make(chan received)
	receiverDone := make(chan struct{})
	go func() {
		defer close(receiverDone)
		for {
			request, err := session.ReceiveRequest(scoped)
			item := received{request: request, err: err, responded: make(chan struct{})}
			select {
			case requests <- item:
			case <-scoped.Done():
				return
			}
			if err != nil {
				return
			}
			select {
			case <-item.responded:
			case <-scoped.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = session.Close(context.Background()); <-receiverDone }()
	exited := make(chan error, 1)
	go func() {
		err := session.WaitVMMExit(scoped)
		if scoped.Err() == nil {
			_ = session.Close(context.Background())
		}
		exited <- err
	}()
	defer func() { cancel(); <-exited }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// The sole reader is started before synchronization so ACKs can always make progress.
	synchronize := func() (bool, error) {
		operation, stop := context.WithTimeout(scoped, 30*time.Second)
		defer stop()
		active, aliases, err := observeGuestControlAliases(operation, setup.Principal)
		if err != nil {
			return false, err
		}
		snapshot := guestcontrolwire.FloatingIPAliasSnapshot{ID: crypto.UUIDV4(), Aliases: aliases}
		if err := session.SyncFloatingIPAliases(operation, snapshot); err != nil {
			return false, err
		}
		_, current, err := observeGuestControlAliases(operation, setup.Principal)
		if err != nil {
			return false, err
		}
		currentSnapshot := guestcontrolwire.FloatingIPAliasSnapshot{ID: snapshot.ID, Aliases: current}
		beforeDigest, err := snapshot.Digest()
		if err != nil {
			return false, err
		}
		afterDigest, err := currentSnapshot.Digest()
		if err != nil {
			return false, err
		}
		if beforeDigest != afterDigest {
			return true, nil
		}
		if err := session.RecordFloatingIPAliasAcknowledgement(operation, setup, snapshot); err != nil {
			return false, err
		}
		return active, nil
	}
	// ReceiveRequest itself starts the internal stream reader; wait for its initialization via Sync.
	active, err := synchronize()
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	for {
		select {
		case <-scoped.Done():
			return scoped.Err()
		case item := <-requests:
			if item.err != nil {
				return item.err
			}
			operation, stop := context.WithTimeout(scoped, 30*time.Second)
			result, effectsStarted, cause := executeAdmittedGuestControlRequest(
				operation,
				setup.Principal,
				item.request,
			)
			response := guestControlWorkerResponse(item.request, result, effectsStarted, cause)
			err := session.SendResponse(operation, response)
			stop()
			close(item.responded)
			if err != nil {
				return err
			}
		case <-ticker.C:
			active, err := synchronize()
			if err != nil {
				return err
			}
			if !active {
				return nil
			}
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
