package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

type floatingIPHandlerEffects struct {
	create func(context.Context, uint32, network.FloatingIPCreateRequest) (network.FloatingIPResult, error)
	remove func(context.Context, uint32, network.FloatingIPRemoveRequest) (network.FloatingIPResult, error)
	list   func(context.Context, uint32, network.FloatingIPListRequest) (network.FloatingIPPage, error)
	get    func(context.Context, uint32, network.FloatingIPGetRequest) (network.FloatingIPResult, error)
}

func handleCreateFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPCreateAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPCreateRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.create == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP handler is unavailable"),
			deps,
		)
	}
	result, err := effects.create(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleRemoveFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPRemoveAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPRemoveRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.remove == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP handler is unavailable"),
			deps,
		)
	}
	result, err := effects.remove(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleListFloatingIPs(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPHandlerEffects, deps controlDeps,
) error {
	action := network.FloatingIPListAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPListRequest](ctx, reader, action, 0)
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
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP handler is unavailable"),
			deps,
		)
	}
	result, err := effects.list(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleGetFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPHandlerEffects,
	deps controlDeps,
) error {
	action := network.FloatingIPGetAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPGetRequest](ctx, reader, action, 0)
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
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP handler is unavailable"),
			deps,
		)
	}
	result, err := effects.get(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
