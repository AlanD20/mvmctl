package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func handleVMSnapshotCapture(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMSnapshotCaptureAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMSnapshotCaptureRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewVMSnapshotServer().Capture(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if result.SnapshotID != request.Body.SnapshotID || result.SourceVMID != request.Body.VMID {
		return deliverOperationError(ctx, endpoint, action, snapshotResultSelectionError(), deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleVMSnapshotInspect(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMSnapshotInspectAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMSnapshotRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewVMSnapshotServer().Inspect(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if result.SnapshotID != request.Body.SnapshotID {
		return deliverOperationError(ctx, endpoint, action, snapshotResultSelectionError(), deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleVMSnapshotRemove(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMSnapshotRemoveAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMSnapshotRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewVMSnapshotServer().Remove(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if result.SnapshotID != request.Body.SnapshotID || !result.Removed {
		return deliverOperationError(ctx, endpoint, action, snapshotResultSelectionError(), deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleVMSnapshotRestore(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMSnapshotRestoreAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.VMSnapshotRestoreRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewVMSnapshotServer().Restore(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if result.VMID != request.Body.VMID || result.Configuration.SnapshotID != request.Body.SnapshotID {
		return deliverOperationError(ctx, endpoint, action, snapshotResultSelectionError(), deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func snapshotResultSelectionError() error {
	return errs.New(
		errs.CodeInternal,
		"snapshot result differs from requested selection",
		errs.WithClass(errs.ClassInternal),
	)
}
