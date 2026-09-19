package network

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// HostServiceClient exposes only the fixed gateway-bound NoCloud service.
type HostServiceClient struct{}

// NewHostServiceClient binds NoCloud operations to the installed root dispatcher.
func NewHostServiceClient() *HostServiceClient { return &HostServiceClient{} }

// InspectHostService resolves the root-owned gateway before attempting the exclusive bind.
func (client *HostServiceClient) InspectHostService(ctx context.Context, request HostServiceRequest) (HostServiceResult, error) {
	return client.exchange(ctx, HostServiceInspectAction, request)
}

// ApplyHostService installs the fixed NoCloud grant in the durable root generation.
func (client *HostServiceClient) ApplyHostService(ctx context.Context, request HostServiceRequest) (HostServiceResult, error) {
	return client.exchange(ctx, HostServiceApplyAction, request)
}

// RemoveHostService revokes the exact NoCloud grant before releasing the listener reservation.
func (client *HostServiceClient) RemoveHostService(ctx context.Context, request HostServiceRequest) (HostServiceResult, error) {
	return client.exchange(ctx, HostServiceRemoveAction, request)
}

func (client *HostServiceClient) exchange(ctx context.Context, action string, request HostServiceRequest) (HostServiceResult, error) {
	if ctx == nil || client == nil {
		return HostServiceResult{}, errs.New(errs.CodeValidationFailed, "NoCloud client and context are required")
	}
	if err := request.Validate(); err != nil {
		return HostServiceResult{}, err
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, action, request, 0); err != nil {
		return HostServiceResult{}, err
	}
	control, err := system.NewPrivilegedControlRequest(action, frame.Bytes(), 0, nil)
	if err != nil {
		return HostServiceResult{}, err
	}
	outcome := (system.PrivilegedControlTransport{}).Exchange(ctx, control)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return HostServiceResult{}, hostServiceResponseError(outcome.ProcessStarted(), outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[HostServiceResult](context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()), action)
	if err != nil {
		return HostServiceResult{}, hostServiceResponseError(true, errors.Join(err, outcome.Diagnostic()))
	}
	if err := response.OperationError(); err != nil {
		return HostServiceResult{}, err
	}
	result := response.Result()
	if err := result.Validate(request.NetworkID); err != nil {
		return HostServiceResult{}, hostServiceResponseError(true, err)
	}
	return result, nil
}

func hostServiceResponseError(started bool, cause error) error {
	return errs.WrapMsg(errs.CodeCloudInitNetModeFailed, "root NoCloud response is not authoritative", cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{"process_started": started, "outcome_unknown": started}))
}
