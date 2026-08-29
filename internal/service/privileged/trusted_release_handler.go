package privileged

import (
	"context"
	"io"
	"time"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

const (
	requestHeaderReadTimeout             = 5 * time.Second
	trustedReleasePayloadReadTimeout     = 5 * time.Minute
	trustedReleaseInstallMaxPayloadBytes = uint64(128 * 1024 * 1024)
)

type trustedReleaseHandlerEffects struct {
	install func(
		context.Context,
		jailer.TrustedReleaseInstallRequest,
		io.Reader,
		uint64,
	) (jailer.TrustedReleaseInstallResult, error)
	remove func(
		context.Context,
		jailer.TrustedReleaseRemoveRequest,
	) (jailer.TrustedReleaseRemoveResult, error)
}

func handleTrustedReleaseInstall(
	ctx context.Context,
	endpoint controlEndpoint,
	effects trustedReleaseHandlerEffects,
	deps controlDeps,
) error {
	headerDeadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	headerReader := newRequestReader(ctx, int(endpoint.Fd()), headerDeadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.TrustedReleaseInstallRequest](
		ctx,
		headerReader,
		jailer.TrustedReleaseInstallAction,
		trustedReleaseInstallMaxPayloadBytes,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseInstallAction, err, deps)
	}
	var payload io.Reader
	if request.PayloadLength > 0 {
		payloadDeadline := boundedReadDeadline(ctx, deps.now(), trustedReleasePayloadReadTimeout)
		payload = newRequestReader(ctx, int(endpoint.Fd()), payloadDeadline, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseInstallAction, err, deps)
	}
	if effects.install == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			jailer.TrustedReleaseInstallAction,
			errs.New(errs.CodeInternal, "trusted release install handler is not active"),
			deps,
		)
	}

	if request.PayloadLength == 0 {
		if err := requireRequestEOF(ctx, headerReader); err != nil {
			return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseInstallAction, err, deps)
		}
	}
	result, err := effects.install(ctx, request.Body, payload, request.PayloadLength)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseInstallAction, err, deps)
	}
	if err := result.Validate(request.Body); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseInstallAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.TrustedReleaseInstallAction, result, deps)
}

func handleTrustedReleaseRemove(
	ctx context.Context,
	endpoint controlEndpoint,
	effects trustedReleaseHandlerEffects,
	deps controlDeps,
) error {
	headerDeadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	headerReader := newRequestReader(ctx, int(endpoint.Fd()), headerDeadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.TrustedReleaseRemoveRequest](
		ctx,
		headerReader,
		jailer.TrustedReleaseRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, headerReader); err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseRemoveAction, err, deps)
	}
	if effects.remove == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			jailer.TrustedReleaseRemoveAction,
			errs.New(errs.CodeInternal, "trusted release remove handler is not active"),
			deps,
		)
	}
	result, err := effects.remove(ctx, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, jailer.TrustedReleaseRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, jailer.TrustedReleaseRemoveAction, result, deps)
}

func boundedReadDeadline(ctx context.Context, now time.Time, timeout time.Duration) time.Time {
	deadline := now.Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func requireRequestEOF(ctx context.Context, reader io.Reader) error {
	var trailing [1]byte
	count, err := reader.Read(trailing[:])
	if contextErr := ctx.Err(); contextErr != nil {
		return errs.WrapMsg(errs.CodeProcessError, "read trailing privileged request input", contextErr)
	}
	if count > 0 || err == nil {
		return errs.New(errs.CodeValidationFailed, "privileged request contains trailing input")
	}
	if err != io.EOF {
		return errs.WrapMsg(errs.CodeValidationFailed, "read trailing privileged request input", err)
	}
	return nil
}
