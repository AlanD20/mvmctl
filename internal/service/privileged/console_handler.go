package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type consoleObserve func(context.Context, jailer.ConsoleCaller, jailer.ConsoleObserveRequest) (
	jailer.ConsoleObserveResult, error,
)

type consoleStop func(context.Context, jailer.ConsoleCaller, jailer.ConsoleStopRequest) (jailer.ConsoleStopResult, error)

func handleConsoleObserve(ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	observe consoleObserve, deps controlDeps,
) error {
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.ConsoleObserveRequest](
		ctx,
		reader,
		jailer.ConsoleObserveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction, err, deps)
	}
	if observe == nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction,
			errs.New(errs.CodeInternal, "console observation is unavailable"), deps)
	}
	result, err := observe(ctx, jailer.ConsoleCaller{UID: caller.uid, GID: caller.gid}, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleObserveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.ConsoleObserveAction, result, deps)
}

func handleConsoleStop(ctx context.Context, endpoint controlEndpoint, caller callerIdentity,
	stop consoleStop, deps controlDeps,
) error {
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[jailer.ConsoleStopRequest](ctx, reader, jailer.ConsoleStopAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction, err, deps)
	}
	if stop == nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction,
			errs.New(errs.CodeInternal, "console stop is unavailable"), deps)
	}
	result, err := stop(ctx, jailer.ConsoleCaller{UID: caller.uid, GID: caller.gid}, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.ConsoleStopAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.ConsoleStopAction, result, deps)
}
