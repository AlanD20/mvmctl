package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func handleProtectedVolumeInspect(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedVolumeInspectAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedVolumeInspectRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedVolumeServer().Inspect(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body.VolumeID); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleProtectedVolumeGrow(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedVolumeGrowAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedVolumeGrowRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedVolumeServer().Grow(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body.VolumeID); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if result.LogicalBytes != request.Body.TargetBytes {
		return deliverOperationError(ctx, endpoint, action, errs.New(
			errs.CodeVolumeError, "volume growth result differs from requested logical size",
			errs.WithClass(errs.ClassInternal), errs.WithEntity(request.Body.VolumeID),
		), deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleProtectedVolumeRemove(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedVolumeRemoveAction
	reader := newRequestReader(ctx, int(endpoint.Fd()), boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedVolumeRemoveRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedVolumeServer().Remove(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body.VolumeID); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
