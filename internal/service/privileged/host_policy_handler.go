package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/hostpolicy"
	"mvmctl/pkg/errs"
)

type hostPolicyObserve func(context.Context) (hostpolicy.ObserveResult, error)

func handleHostPolicyObserve(ctx context.Context, endpoint controlEndpoint,
	observe hostPolicyObserve, deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	if _, err := privilegedwire.DecodeRequest[hostpolicy.ObserveRequest](
		ctx, reader, hostpolicy.ObserveAction, 0); err != nil {
		return deliverOperationError(ctx, endpoint, hostpolicy.ObserveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, hostpolicy.ObserveAction, err, deps)
	}
	if observe == nil {
		return deliverOperationError(ctx, endpoint, hostpolicy.ObserveAction,
			errs.New(errs.CodeInternal, "managed host policy observer is inactive"), deps)
	}
	result, err := observe(ctx)
	if err != nil {
		return deliverOperationError(ctx, endpoint, hostpolicy.ObserveAction, err, deps)
	}
	if err := result.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, hostpolicy.ObserveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, hostpolicy.ObserveAction, result, deps)
}
