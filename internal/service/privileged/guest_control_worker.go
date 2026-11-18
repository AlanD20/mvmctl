package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// executeGuestControlRequest is a closed feature compositor, not operator CRUD or a command proxy.
// Runtime workers/dispatch are deliberately not registered in this foundation.
func executeGuestControlRequest(
	ctx context.Context, principal jailer.GuestControlPrincipal, request guestcontrolwire.Request,
) (result network.FloatingIPGuestResult, effectsStarted bool, returnErr error) {
	if err := request.Validate(); err != nil {
		return result, false, err
	}
	admission, err := jailer.NewFloatingIPAdmissionServer().Open(ctx, principal.OwnerUID, principal.OwnerGID,
		jailer.FloatingIPAdmissionRequest{VMID: principal.VMID})
	if err != nil {
		return result, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return result, false, err
	}
	if err := requireGuestControlRequestIncarnation(principal, identity); err != nil {
		return result, false, err
	}
	if request.Operation == guestcontrolwire.Associate || request.Operation == guestcontrolwire.Renew {
		if err := admission.RequireRunning(ctx); err != nil {
			return result, false, err
		}
	}
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return result, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().OpenVM(ctx, hold, network.FloatingIPVMIncarnation{
		BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
	if err != nil {
		return result, false, err
	}
	// This defer executes BEFORE child hold and parent Jailer admission release.
	defer func() { returnErr = errors.Join(returnErr, vm.Release(context.WithoutCancel(ctx))) }()
	input := request.Input
	switch request.Operation {
	case guestcontrolwire.Inspect:
		result, err = vm.GuestInspect(ctx, input.ResourceID, input.ExpectedGeneration)
	case guestcontrolwire.Associate:
		effectsStarted = true
		result, err = vm.GuestAssociate(ctx, input.ResourceID, input.ExpectedGeneration)
	case guestcontrolwire.Renew:
		effectsStarted = true
		result, err = vm.GuestRenew(ctx, input.ResourceID, input.ExpectedGeneration)
	case guestcontrolwire.Release:
		effectsStarted = true
		result, err = vm.GuestRelease(ctx, input.ResourceID, input.ExpectedGeneration)
	}
	return result, effectsStarted, err
}

func requireGuestControlRequestIncarnation(
	principal jailer.GuestControlPrincipal, actual jailer.FloatingIPVMIdentity,
) error {
	if principal.OwnerUID != actual.OwnerUID || principal.VMID != actual.VMID || principal.BootID != actual.BootID ||
		principal.LaunchGeneration != actual.LaunchGeneration || principal.ExecutionUID != actual.ExecutionUID {
		return errs.New(errs.CodeValidationFailed, "Guest Control session incarnation is no longer current")
	}
	return nil
}

// guestControlWorkerResponse projects only closed public values. No wrapped cause, host path,
// other-VM identity or credential can cross the authenticated stream.
func guestControlWorkerResponse(
	request guestcontrolwire.Request, result network.FloatingIPGuestResult, effectsStarted bool, cause error,
) guestcontrolwire.Response {
	response := guestcontrolwire.Response{RequestID: request.RequestID, Operation: request.Operation}
	if cause != nil {
		code := guestcontrolwire.FailureUnavailable
		domain := errs.AsDomainError(cause)
		// The feature currently reports no pre/post-commit stage. Once a mutation method has
		// been entered, conservatively require inspection even if its error class is validation.
		if effectsStarted {
			code = guestcontrolwire.FailureOutcomeUnknown
		} else if domain != nil && domain.Class == errs.ClassValidation {
			code = guestcontrolwire.FailureDenied
		} else if domain != nil && domain.Class == errs.ClassConflict {
			code = guestcontrolwire.FailureConflict
		}
		response.Failure = &guestcontrolwire.Failure{Code: code}
		return response
	}
	ownership := guestcontrolwire.Unassociated
	switch result.Ownership {
	case network.FloatingIPGuestUnassociated:
	case network.FloatingIPGuestSelf:
		ownership = guestcontrolwire.OwnedBySelf
	case network.FloatingIPGuestOther:
		ownership = guestcontrolwire.OwnedByOther
	default:
		response.Failure = &guestcontrolwire.Failure{Code: guestcontrolwire.FailureInternal}
		return response
	}
	response.Result = &guestcontrolwire.FloatingIPResult{ResourceID: result.ResourceID, IPv4: result.IPv4,
		Generation: result.Generation, Ownership: ownership, LeaseRemainingMillis: result.LeaseRemainingMillis}
	if err := response.ValidateFor(request); err != nil {
		response.Result = nil
		response.Failure = &guestcontrolwire.Failure{Code: guestcontrolwire.FailureInternal}
	}
	return response
}
