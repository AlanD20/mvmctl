package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type runtimeStatusObserve func(
	context.Context,
	jailer.RuntimeStatusCaller,
	jailer.RuntimeStatusRequest,
) (jailer.RuntimeStatusResult, error)

func handleRuntimeStatus(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	observe runtimeStatusObserve,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.RuntimeStatusRequest](
		ctx, reader, jailer.RuntimeStatusAction, 0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction, err, deps)
	}
	if observe == nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction,
			errs.New(errs.CodeInternal, "runtime status handler is not active"), deps)
	}
	result, err := observe(ctx, jailer.RuntimeStatusCaller{UID: caller.uid, GID: caller.gid}, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeStatusAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.RuntimeStatusAction, result, deps)
}
