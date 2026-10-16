package privileged

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func handleVMRootfsExport(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMRootfsExportAction
	reader := newRequestReader(ctx, int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout), deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMRootfsExportRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	server := jailer.NewVMRootfsExportServer()
	result, err := server.Export(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	err = result.Validate(request.Body)
	if err == nil {
		err = deliverOperationSuccess(ctx, endpoint, action, result, deps)
	} else {
		err = errors.Join(err, deliverOperationError(ctx, endpoint, action, err, deps))
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, releaseErr := server.Release(
			recovery,
			caller.uid,
			caller.gid,
			jailer.VMRootfsExportReleaseRequest{ImageID: request.Body.ImageID},
		)
		return errors.Join(err, releaseErr)
	}
	return nil
}
