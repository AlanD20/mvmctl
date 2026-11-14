package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type cgroupObservationObserve func(
	context.Context,
	jailer.RuntimeStatusCaller,
	jailer.VMCgroupObserveRequest,
) (jailer.VMCgroupObserveResult, error)

func handleVMCgroupObserve(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	observe cgroupObservationObserve,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMCgroupObserveRequest](
		ctx, reader, jailer.VMCgroupObserveAction, 0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction, err, deps)
	}
	if observe == nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction,
			errs.New(errs.CodeInternal, "VM cgroup observation handler is not active"), deps)
	}
	result, err := observe(ctx, jailer.RuntimeStatusCaller{UID: caller.uid, GID: caller.gid}, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.VMCgroupObserveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.VMCgroupObserveAction, result, deps)
}
