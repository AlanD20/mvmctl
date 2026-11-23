package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func handleVMLaunch(ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMLaunchRequest](ctx, reader, jailer.VMLaunchAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMLaunchAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMLaunchAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMLaunchAction, err, deps)
	}
	result, err := jailer.NewVMLifecycleServer().Launch(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMLaunchAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMLaunchAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMLaunchAction, result, deps)
}

func handleVMStop(ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMStopRequest](ctx, reader, jailer.VMStopAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMStopAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMStopAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMStopAction, err, deps)
	}
	result, err := jailer.NewVMLifecycleServer().Stop(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMStopAction, err, deps)
	}
	if err := result.Validate(jailer.VMCleanupRequest{VMID: request.Body.VMID}); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMStopAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMStopAction, result, deps)
}

// handleVMCleanup decodes the shared identity body for the three closed cleanup actions.
// The action is supplied only by the authenticated receiver's fixed dispatch switch.
func handleVMCleanup(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	action string,
	deps controlDeps,
) error {
	switch action {
	case jailer.VMAbortAction, jailer.VMCleanupAction, jailer.VMRemoveAction:
	default:
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "unsupported VM cleanup action"), deps)
	}
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMCleanupRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	server := jailer.NewVMLifecycleServer()
	var result jailer.VMCleanupResult
	switch action {
	case jailer.VMAbortAction:
		result, err = server.Abort(ctx, caller.uid, caller.gid, request.Body)
	case jailer.VMCleanupAction:
		result, err = server.Cleanup(ctx, caller.uid, caller.gid, request.Body)
	case jailer.VMRemoveAction:
		result, err = server.Remove(ctx, caller.uid, caller.gid, request.Body)
	}
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
