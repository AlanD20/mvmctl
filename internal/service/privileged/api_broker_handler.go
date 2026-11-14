package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

// The fixed dispatch switch supplies the action; the request contains only a canonical VM ID.
func handleVMAPIBroker(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	action string,
	deps controlDeps,
) error {
	switch action {
	case jailer.VMAPIReadyAction, jailer.VMPauseAction, jailer.VMResumeAction, jailer.VMStartInstanceAction:
	default:
		return deliverOperationError(ctx, endpoint, action,
			errs.New(errs.CodeInternal, "unsupported named VM API action"), deps)
	}
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMAPIRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	server := jailer.NewVMAPIBrokerServer()
	if action == jailer.VMAPIReadyAction {
		result, err := server.WaitReady(ctx, caller.uid, caller.gid, request.Body)
		if err != nil {
			return deliverOperationError(ctx, endpoint, action, err, deps)
		}
		if err := result.Validate(request.Body); err != nil {
			return deliverOperationError(ctx, endpoint, action, err, deps)
		}
		return deliverOperationSuccess(ctx, endpoint, action, result, deps)
	}
	var result jailer.VMAPIResult
	switch action {
	case jailer.VMPauseAction:
		result, err = server.Pause(ctx, caller.uid, caller.gid, request.Body)
	case jailer.VMResumeAction:
		result, err = server.Resume(ctx, caller.uid, caller.gid, request.Body)
	case jailer.VMStartInstanceAction:
		result, err = server.StartInstance(ctx, caller.uid, caller.gid, request.Body)
	}
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
