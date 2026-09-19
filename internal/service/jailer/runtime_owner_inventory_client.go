package jailer

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// RuntimeOwnerInventoryClient requests one complete inventory through the fixed privileged transport.
type RuntimeOwnerInventoryClient struct {
	transport system.PrivilegedControlTransport
}

// NewRuntimeOwnerInventoryClient binds caller-owned inventory discovery to the system transport.
func NewRuntimeOwnerInventoryClient() *RuntimeOwnerInventoryClient {
	return &RuntimeOwnerInventoryClient{transport: system.PrivilegedControlTransport{}}
}

// Observe accepts no VM selection; the receiver derives the owner from the authenticated control peer.
func (client *RuntimeOwnerInventoryClient) Observe(ctx context.Context) (RuntimeOwnerInventoryResult, error) {
	if ctx == nil {
		return RuntimeOwnerInventoryResult{}, instanceValidationError("runtime owner inventory context is required")
	}
	if err := ctx.Err(); err != nil {
		return RuntimeOwnerInventoryResult{}, runtimeStatusUnavailableError("", "runtime inventory context ended", err)
	}
	if client == nil {
		return RuntimeOwnerInventoryResult{}, runtimeStatusUnavailableError("", "runtime inventory client is inactive", nil)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, RuntimeOwnerInventoryAction,
		RuntimeOwnerInventoryRequest{}, 0); err != nil {
		return RuntimeOwnerInventoryResult{}, err
	}
	request, err := system.NewPrivilegedControlRequest(RuntimeOwnerInventoryAction, frame.Bytes(), 0, nil)
	if err != nil {
		return RuntimeOwnerInventoryResult{}, err
	}
	outcome := client.transport.Exchange(ctx, request)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return RuntimeOwnerInventoryResult{}, runtimeStatusUnavailableError(
			"", "runtime inventory response did not reach authoritative EOF", outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[RuntimeOwnerInventoryResult](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.Response()), RuntimeOwnerInventoryAction)
	if err != nil {
		return RuntimeOwnerInventoryResult{}, runtimeStatusUnavailableError(
			"", "runtime inventory response is not authoritative", errors.Join(err, outcome.Diagnostic()))
	}
	if err := response.OperationError(); err != nil {
		return RuntimeOwnerInventoryResult{}, err
	}
	result := response.Result()
	if err := result.Validate(); err != nil {
		return RuntimeOwnerInventoryResult{}, err
	}
	return result, nil
}
