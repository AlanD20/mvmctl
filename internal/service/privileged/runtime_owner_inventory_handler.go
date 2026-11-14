package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type runtimeOwnerInventoryObserve func(
	context.Context,
	jailer.RuntimeStatusCaller,
	jailer.RuntimeOwnerInventoryRequest,
) (jailer.RuntimeOwnerInventoryResult, error)

func handleRuntimeOwnerInventory(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	observe runtimeOwnerInventoryObserve,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.RuntimeOwnerInventoryRequest](
		ctx, reader, jailer.RuntimeOwnerInventoryAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeOwnerInventoryAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeOwnerInventoryAction, err, deps)
	}
	if observe == nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeOwnerInventoryAction,
			errs.New(errs.CodeInternal, "runtime owner inventory handler is not active"), deps)
	}
	result, err := observe(ctx, jailer.RuntimeStatusCaller{UID: caller.uid, GID: caller.gid}, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeOwnerInventoryAction, err, deps)
	}
	if err := result.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.RuntimeOwnerInventoryAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.RuntimeOwnerInventoryAction, result, deps)
}
