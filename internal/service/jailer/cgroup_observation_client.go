package jailer

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// VMCgroupObserveClient makes one authenticated, read-only root request.
type VMCgroupObserveClient struct {
	transport system.PrivilegedControlTransport
}

func NewVMCgroupObserveClient() *VMCgroupObserveClient {
	return &VMCgroupObserveClient{transport: system.PrivilegedControlTransport{}}
}

func (client *VMCgroupObserveClient) Observe(
	ctx context.Context,
	request VMCgroupObserveRequest,
) (VMCgroupObserveResult, error) {
	if ctx == nil {
		return VMCgroupObserveResult{}, instanceValidationError("VM cgroup observation context is required")
	}
	if err := ctx.Err(); err != nil {
		return VMCgroupObserveResult{}, err
	}
	if err := request.Validate(); err != nil {
		return VMCgroupObserveResult{}, err
	}
	if client == nil {
		return VMCgroupObserveResult{}, instanceAtomicError("VM cgroup observation client is not active", nil)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, VMCgroupObserveAction, request, 0); err != nil {
		return VMCgroupObserveResult{}, err
	}
	control, err := system.NewPrivilegedControlRequest(VMCgroupObserveAction, frame.Bytes(), 0, nil)
	if err != nil {
		return VMCgroupObserveResult{}, err
	}
	outcome := client.transport.Exchange(ctx, control)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return VMCgroupObserveResult{}, instanceAtomicError(
			"VM cgroup observation response did not reach authoritative EOF", outcome.Diagnostic(),
		)
	}
	response, err := privilegedwire.DecodeResponse[VMCgroupObserveResult](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.Response()), VMCgroupObserveAction,
	)
	if err != nil {
		return VMCgroupObserveResult{}, instanceAtomicError(
			"VM cgroup observation response is invalid", errors.Join(err, outcome.Diagnostic()),
		)
	}
	if err := response.OperationError(); err != nil {
		return VMCgroupObserveResult{}, err
	}
	result := response.Result()
	if err := result.Validate(request); err != nil {
		return VMCgroupObserveResult{}, err
	}
	return result, nil
}
