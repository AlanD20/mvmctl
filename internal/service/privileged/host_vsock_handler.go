package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleHostVsockOpen(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) (returnErr error) {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.HostVsockOpenRequest](
		ctx,
		reader,
		jailer.HostVsockOpenAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.HostVsockOpenAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.HostVsockOpenAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.HostVsockOpenAction, err, deps)
	}
	connection, err := jailer.NewHostVsockServer().Open(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.HostVsockOpenAction, err, deps)
	}
	defer func() {
		returnErr = errors.Join(returnErr, connection.Close(context.WithoutCancel(ctx)))
	}()
	if err := connection.Deliver(ctx, endpoint.Fd()); err != nil {
		return responseTransportError(nil, err, true)
	}
	return nil
}
