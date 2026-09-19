package network

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// ManagedNetworkClient exposes only the named managed-network transactions.
type ManagedNetworkClient struct{}

// NewManagedNetworkClient binds network operations to the installed privileged dispatcher.
func NewManagedNetworkClient() *ManagedNetworkClient { return &ManagedNetworkClient{} }

// CreateManagedNetwork projects a network only after authoritative root acknowledgement.
func (client *ManagedNetworkClient) CreateManagedNetwork(
	ctx context.Context, request ManagedNetworkCreateRequest,
) (ManagedNetworkResult, error) {
	if err := request.Validate(); err != nil {
		return ManagedNetworkResult{}, err
	}
	result, err := exchangeManagedNetwork[ManagedNetworkCreateRequest, ManagedNetworkResult](
		ctx, client, ManagedNetworkCreateAction, request)
	if err != nil {
		return ManagedNetworkResult{}, err
	}
	if err := result.Validate(request); err != nil {
		return ManagedNetworkResult{}, managedNetworkResponseError(true, err)
	}
	return result, nil
}

// RemoveManagedNetwork verifies exact root cleanup before local desired-state deletion.
func (client *ManagedNetworkClient) RemoveManagedNetwork(
	ctx context.Context, request ManagedNetworkRemoveRequest,
) (ManagedNetworkRemovalResult, error) {
	if err := request.Validate(); err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	result, err := exchangeManagedNetwork[ManagedNetworkRemoveRequest, ManagedNetworkRemovalResult](
		ctx, client, ManagedNetworkRemoveAction, request)
	if err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	if err := result.Validate(request); err != nil {
		return ManagedNetworkRemovalResult{}, managedNetworkResponseError(true, err)
	}
	return result, nil
}

func exchangeManagedNetwork[
	Request ManagedNetworkCreateRequest | ManagedNetworkRemoveRequest,
	Result ManagedNetworkResult | ManagedNetworkRemovalResult,
](ctx context.Context, client *ManagedNetworkClient, action string, request Request) (Result, error) {
	var zero Result
	if ctx == nil || client == nil {
		return zero, networkAuthorityValidationError("network client and context are required")
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, action, request, 0); err != nil {
		return zero, err
	}
	control, err := system.NewPrivilegedControlRequest(action, frame.Bytes(), 0, nil)
	if err != nil {
		return zero, err
	}
	outcome := (system.PrivilegedControlTransport{}).Exchange(ctx, control)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return zero, managedNetworkResponseError(outcome.ProcessStarted(), outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[Result](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.Response()), action)
	if err != nil {
		return zero, managedNetworkResponseError(true, errors.Join(err, outcome.Diagnostic()))
	}
	if operationErr := response.OperationError(); operationErr != nil {
		return zero, operationErr
	}
	return response.Result(), nil
}

func managedNetworkResponseError(started bool, cause error) error {
	return errs.WrapMsg(errs.CodeNetworkError, "root network response is not authoritative", cause,
		errs.WithDetails(map[string]any{"process_started": started, "outcome_unknown": started}))
}
