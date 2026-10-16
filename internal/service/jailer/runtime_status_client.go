package jailer

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// RuntimeStatusClient performs one fixed privileged exchange for each bounded observation request.
type RuntimeStatusClient struct {
	transport system.PrivilegedControlTransport
}

// NewRuntimeStatusClient binds the read-only protocol to the canonical system transport.
func NewRuntimeStatusClient() *RuntimeStatusClient {
	return &RuntimeStatusClient{transport: system.PrivilegedControlTransport{}}
}

// Observe accepts no caller PID, path, descriptor, or authority identity.
func (client *RuntimeStatusClient) Observe(
	ctx context.Context,
	request RuntimeStatusRequest,
) (RuntimeStatusResult, error) {
	if ctx == nil {
		return RuntimeStatusResult{}, instanceValidationError("runtime status context is required")
	}
	if err := ctx.Err(); err != nil {
		return RuntimeStatusResult{}, runtimeStatusUnavailableError("", "runtime status context ended", err)
	}
	if err := request.Validate(); err != nil {
		return RuntimeStatusResult{}, err
	}
	if client == nil {
		return RuntimeStatusResult{}, runtimeStatusUnavailableError("", "runtime status client is not active", nil)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, RuntimeStatusAction, request, 0); err != nil {
		return RuntimeStatusResult{}, err
	}
	control, err := system.NewPrivilegedControlRequest(RuntimeStatusAction, frame.Bytes(), 0, nil)
	if err != nil {
		return RuntimeStatusResult{}, err
	}
	outcome := client.transport.Exchange(ctx, control)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return RuntimeStatusResult{}, runtimeStatusUnavailableError(
			"", "runtime status response did not reach authoritative EOF", outcome.Diagnostic(),
		)
	}
	response, err := privilegedwire.DecodeResponse[RuntimeStatusResult](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.Response()), RuntimeStatusAction,
	)
	if err != nil {
		return RuntimeStatusResult{}, runtimeStatusUnavailableError(
			"", "runtime status response is not authoritative", errors.Join(err, outcome.Diagnostic()),
		)
	}
	if err := response.OperationError(); err != nil {
		return RuntimeStatusResult{}, err
	}
	result := response.Result()
	if err := result.Validate(request); err != nil {
		return RuntimeStatusResult{}, err
	}
	return result, nil
}
