package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleProtectedKernelImport(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedKernelImportAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedKernelImportRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedKernelServer().Import(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body.KernelID); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleProtectedKernelRemove(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedKernelRemoveAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedKernelRemoveRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedKernelServer().Remove(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body.KernelID); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
