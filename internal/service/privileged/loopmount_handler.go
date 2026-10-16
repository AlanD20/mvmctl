package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

type loopMountHandlerEffects struct {
	provisionVM func(
		context.Context,
		loopmount.Caller,
		loopmount.VMProvisionRequest,
	) (loopmount.VMProvisionResult, error)
	inspectImage func(
		context.Context,
		loopmount.Caller,
		loopmount.ImageInspectRequest,
	) (loopmount.ImageInspectResult, error)
	optimizeImage func(
		context.Context,
		loopmount.Caller,
		loopmount.ImageOptimizeRequest,
	) (loopmount.ImageOptimizeResult, error)
}

func handleLoopMountOptimizeImage(
	ctx context.Context,
	endpoint controlEndpoint,
	caller loopmount.Caller,
	effects loopMountHandlerEffects,
	deps controlDeps,
) error {
	headerDeadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	headerReader := newRequestReader(ctx, int(endpoint.Fd()), headerDeadline, deps)
	request, err := privilegedwire.DecodeRequest[loopmount.ImageOptimizeRequest](
		ctx,
		headerReader,
		loopmount.ImageOptimizeAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageOptimizeAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageOptimizeAction, err, deps)
	}
	if err := requireRequestEOF(ctx, headerReader); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageOptimizeAction, err, deps)
	}
	if effects.optimizeImage == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			loopmount.ImageOptimizeAction,
			errs.New(errs.CodeInternal, "loopmount image optimization handler is not active"),
			deps,
		)
	}
	result, err := effects.optimizeImage(ctx, caller, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageOptimizeAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageOptimizeAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, loopmount.ImageOptimizeAction, result, deps)
}

func handleLoopMountProvisionVM(
	ctx context.Context,
	endpoint controlEndpoint,
	caller loopmount.Caller,
	effects loopMountHandlerEffects,
	deps controlDeps,
) error {
	headerDeadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	headerReader := newRequestReader(ctx, int(endpoint.Fd()), headerDeadline, deps)
	request, err := privilegedwire.DecodeRequest[loopmount.VMProvisionRequest](
		ctx,
		headerReader,
		loopmount.VMProvisionAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.VMProvisionAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.VMProvisionAction, err, deps)
	}
	if err := requireRequestEOF(ctx, headerReader); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.VMProvisionAction, err, deps)
	}
	if effects.provisionVM == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			loopmount.VMProvisionAction,
			errs.New(errs.CodeInternal, "loopmount VM provision handler is not active"),
			deps,
		)
	}
	result, err := effects.provisionVM(ctx, caller, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.VMProvisionAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.VMProvisionAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, loopmount.VMProvisionAction, result, deps)
}

func handleLoopMountInspectImage(
	ctx context.Context,
	endpoint controlEndpoint,
	caller loopmount.Caller,
	effects loopMountHandlerEffects,
	deps controlDeps,
) error {
	headerDeadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	headerReader := newRequestReader(ctx, int(endpoint.Fd()), headerDeadline, deps)
	request, err := privilegedwire.DecodeRequest[loopmount.ImageInspectRequest](
		ctx,
		headerReader,
		loopmount.ImageInspectAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageInspectAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageInspectAction, err, deps)
	}
	if err := requireRequestEOF(ctx, headerReader); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageInspectAction, err, deps)
	}
	if effects.inspectImage == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			loopmount.ImageInspectAction,
			errs.New(errs.CodeInternal, "loopmount image inspection handler is not active"),
			deps,
		)
	}
	result, err := effects.inspectImage(ctx, caller, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageInspectAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, loopmount.ImageInspectAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, loopmount.ImageInspectAction, result, deps)
}
