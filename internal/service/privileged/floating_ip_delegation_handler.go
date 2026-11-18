package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

type floatingIPDelegationHandlerEffects struct {
	removeOwned func(context.Context, uint32,
		network.FloatingIPDelegationRemoveOwnedRequest) (network.FloatingIPDelegationResult, error)
	grant func(context.Context, uint32, uint32,
		network.FloatingIPDelegationGrantRequest) (network.FloatingIPDelegationResult, error)
	revoke func(context.Context, uint32,
		network.FloatingIPDelegationRevokeRequest) (network.FloatingIPDelegationResult, error)
	list func(context.Context, uint32,
		network.FloatingIPDelegationListRequest) (network.FloatingIPDelegationPage, error)
	get func(context.Context, uint32,
		network.FloatingIPDelegationGetRequest) (network.FloatingIPDelegationResult, error)
}

// grantFloatingIPDelegation composes concrete root admissions. Parent VM authority outlives every child;
// no network lock is reacquired through a caller-selected callback, and no guest call impersonates this operator.
func grantFloatingIPDelegation(
	ctx context.Context, uid, gid uint32, request network.FloatingIPDelegationGrantRequest,
) (_ network.FloatingIPDelegationResult, returnErr error) {
	parent, err := jailer.NewFloatingIPAdmissionServer().Open(ctx, uid, gid,
		jailer.FloatingIPAdmissionRequest{VMID: request.VMID})
	if err != nil {
		return network.FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, parent.Release(context.WithoutCancel(ctx))) }()
	identity, err := parent.Identity(ctx)
	if err != nil {
		return network.FloatingIPDelegationResult{}, err
	}
	hold, err := parent.RetainTopologyHold(ctx)
	if err != nil {
		return network.FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	child, err := network.NewFloatingIPServer().OpenVM(ctx, hold, network.FloatingIPVMIncarnation{
		BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
	if err != nil {
		return network.FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Release(context.WithoutCancel(ctx))) }()
	if err := parent.Recheck(ctx); err != nil {
		return network.FloatingIPDelegationResult{}, err
	}
	return child.GrantFloatingIPDelegation(ctx, request)
}

func handleGrantFloatingIPDelegation(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	effects floatingIPDelegationHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPDelegationGrantAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(),
		requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPDelegationGrantRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.grant == nil {
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "delegation handler is unavailable"), deps)
	}
	result, err := effects.grant(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleRevokeFloatingIPDelegation(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	effects floatingIPDelegationHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPDelegationRevokeAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(),
		requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPDelegationRevokeRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.revoke == nil {
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "delegation handler is unavailable"), deps)
	}
	result, err := effects.revoke(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleListFloatingIPDelegation(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	effects floatingIPDelegationHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPDelegationListAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(),
		requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPDelegationListRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.list == nil {
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "delegation handler is unavailable"), deps)
	}
	result, err := effects.list(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleGetFloatingIPDelegation(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	effects floatingIPDelegationHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPDelegationGetAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(),
		requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPDelegationGetRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.get == nil {
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "delegation handler is unavailable"), deps)
	}
	result, err := effects.get(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleRemoveOwnedFloatingIPDelegation(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	effects floatingIPDelegationHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPDelegationRemoveOwnedAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(),
		requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPDelegationRemoveOwnedRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.removeOwned == nil {
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "delegation handler is unavailable"), deps)
	}
	result, err := effects.removeOwned(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
