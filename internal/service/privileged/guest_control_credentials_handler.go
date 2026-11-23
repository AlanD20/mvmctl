package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleGuestControlPrepare(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.GuestControlPrepareRequest](
		ctx, reader, jailer.GuestControlPrepareAction, 0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.GuestControlPrepareAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.GuestControlPrepareAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.GuestControlPrepareAction, err, deps)
	}
	result, err := jailer.NewGuestControlCredentialServer().Prepare(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.GuestControlPrepareAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.GuestControlPrepareAction, err, deps)
	}
	// Sensitive agent material crosses only the authenticated internal descriptor channel.
	return deliverOperationSuccess(ctx, endpoint, jailer.GuestControlPrepareAction, result, deps)
}
