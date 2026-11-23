package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleVMVolumeObserve(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMVolumeObserveRequest](
		ctx,
		reader,
		jailer.VMVolumeObserveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeObserveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeObserveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeObserveAction, err, deps)
	}
	result, err := jailer.NewVMLifecycleServer().ObserveVolume(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeObserveAction, err, deps)
	}
	if err := result.Validate(request.Body.VMID, request.Body.VolumeID); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeObserveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMVolumeObserveAction, result, deps)
}

func handleVMVolumeAttach(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMVolumeAttachRequest](
		ctx,
		reader,
		jailer.VMVolumeAttachAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeAttachAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeAttachAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeAttachAction, err, deps)
	}
	result, err := jailer.NewVMLifecycleServer().AttachVolume(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeAttachAction, err, deps)
	}
	if err := result.Validate(request.Body.VMID, request.Body.Volume.ID, true); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeAttachAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMVolumeAttachAction, result, deps)
}

func handleVMVolumeDetach(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMVolumeDetachRequest](
		ctx,
		reader,
		jailer.VMVolumeDetachAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeDetachAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeDetachAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeDetachAction, err, deps)
	}
	result, err := jailer.NewVMLifecycleServer().DetachVolume(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeDetachAction, err, deps)
	}
	if err := result.Validate(request.Body.VMID, request.Body.VolumeID, false); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMVolumeDetachAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMVolumeDetachAction, result, deps)
}
