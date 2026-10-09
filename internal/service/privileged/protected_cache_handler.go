package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleProtectedCacheClean(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.ProtectedCacheCleanAction
	reader := newRequestReader(ctx, int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[jailer.ProtectedCacheCleanRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := jailer.NewProtectedCacheServer().Clean(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := result.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
