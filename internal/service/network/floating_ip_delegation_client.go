package network

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// GrantFloatingIPDelegation requests stored permission; it does not claim a ready worker or installed address.
func (client *FloatingIPClient) GrantFloatingIPDelegation(
	ctx context.Context, request FloatingIPDelegationGrantRequest,
) (FloatingIPDelegationResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	result, err := exchangeFloatingIPDelegation[FloatingIPDelegationGrantRequest, FloatingIPDelegationResult](
		ctx, client, FloatingIPDelegationGrantAction, request)
	if err != nil {
		return result, err
	}
	if err := validateFloatingIPDelegationResult(result, request.NetworkID); err != nil {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true, err)
	}
	if result.ID != request.ID || result.ResourceID != request.ResourceID || result.VMID != request.VMID ||
		result.Method != request.Method ||
		result.LeaseDuration != request.LeaseDuration || result.State != "active" ||
		(request.Expected != nil && !request.Expected.Matches(result.BootID, result.LaunchGeneration, result.ExecutionUID)) {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true,
			networkAuthorityValidationError("delegation grant acknowledgement differs"))
	}
	return result, nil
}

// RevokeFloatingIPDelegation requires an authoritative acknowledgement for the exact lifetime.
func (client *FloatingIPClient) RevokeFloatingIPDelegation(
	ctx context.Context, request FloatingIPDelegationRevokeRequest,
) (FloatingIPDelegationResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	result, err := exchangeFloatingIPDelegation[FloatingIPDelegationRevokeRequest, FloatingIPDelegationResult](
		ctx, client, FloatingIPDelegationRevokeAction, request)
	if err != nil {
		return result, err
	}
	if err := validateFloatingIPDelegationResult(result, request.NetworkID); err != nil {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true, err)
	}
	if result.ID != request.ID || result.State != "revoked" {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true,
			networkAuthorityValidationError("delegation revocation acknowledgement differs"))
	}
	return result, nil
}

// GetFloatingIPDelegation reads an exact root grant projection.
func (client *FloatingIPClient) GetFloatingIPDelegation(
	ctx context.Context, request FloatingIPDelegationGetRequest,
) (FloatingIPDelegationResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	result, err := exchangeFloatingIPDelegation[FloatingIPDelegationGetRequest, FloatingIPDelegationResult](
		ctx, client, FloatingIPDelegationGetAction, request)
	if err != nil {
		return result, err
	}
	if err := validateFloatingIPDelegationResult(result, request.NetworkID); err != nil {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, false, err)
	}
	if result.ID != request.ID {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, false,
			networkAuthorityValidationError("delegation inspection acknowledgement differs"))
	}
	return result, nil
}

// ListFloatingIPDelegations validates one bounded root inventory page.
func (client *FloatingIPClient) ListFloatingIPDelegations(
	ctx context.Context, request FloatingIPDelegationListRequest,
) (FloatingIPDelegationPage, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationPage{}, err
	}
	page, err := exchangeFloatingIPDelegation[FloatingIPDelegationListRequest, FloatingIPDelegationPage](
		ctx, client, FloatingIPDelegationListAction, request)
	if err != nil {
		return page, err
	}
	if err := validateFloatingIPDelegationPage(page, request); err != nil {
		return FloatingIPDelegationPage{}, floatingIPResponseError(true, false, err)
	}
	return page, nil
}

func validateFloatingIPDelegationPage(page FloatingIPDelegationPage, request FloatingIPDelegationListRequest) error {
	if page.Items == nil || len(page.Items) > floatingIPPageSize {
		return networkAuthorityValidationError("delegation inventory page size is invalid")
	}
	previous := request.AfterID
	for _, item := range page.Items {
		if err := validateFloatingIPDelegationResult(item, request.NetworkID); err != nil {
			return err
		}
		if item.ResourceID != request.ResourceID || item.ID <= previous {
			return networkAuthorityValidationError("delegation inventory scope or order differs")
		}
		previous = item.ID
	}
	if page.NextID != "" && (len(page.Items) != floatingIPPageSize || page.NextID != previous) {
		return networkAuthorityValidationError("delegation inventory cursor is invalid")
	}
	return nil
}

func exchangeFloatingIPDelegation[
	Request FloatingIPDelegationRemoveOwnedRequest | FloatingIPDelegationGrantRequest |
		FloatingIPDelegationListRequest | FloatingIPDelegationGetRequest | FloatingIPDelegationRevokeRequest,
	Result FloatingIPDelegationResult | FloatingIPDelegationPage,
](ctx context.Context, client *FloatingIPClient, action string, request Request) (Result, error) {
	var zero Result
	if ctx == nil || client == nil {
		return zero, networkAuthorityValidationError("delegation client and context are required")
	}
	mutation := action == FloatingIPDelegationGrantAction || action == FloatingIPDelegationRevokeAction ||
		action == FloatingIPDelegationRemoveOwnedAction
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
		return zero, floatingIPResponseError(outcome.ProcessStarted(), mutation, outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[Result](context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()), action)
	if err != nil {
		return zero, floatingIPResponseError(true, mutation, errors.Join(err, outcome.Diagnostic()))
	}
	if err := response.OperationError(); err != nil {
		return zero, err
	}
	return response.Result(), nil
}

// RemoveOwnedFloatingIPDelegation retires only the immutable grant lifetime in a retained receipt.
func (client *FloatingIPClient) RemoveOwnedFloatingIPDelegation(
	ctx context.Context, request FloatingIPDelegationRemoveOwnedRequest,
) (FloatingIPDelegationResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	result, err := exchangeFloatingIPDelegation[FloatingIPDelegationRemoveOwnedRequest, FloatingIPDelegationResult](
		ctx, client, FloatingIPDelegationRemoveOwnedAction, request)
	if err != nil {
		return result, err
	}
	if err := validateFloatingIPDelegationResult(result, request.Grant.NetworkID); err != nil {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true, err)
	}
	expected := request.Grant
	expected.Revision = 3
	expected.State = "revoked"
	if result != expected {
		return FloatingIPDelegationResult{}, floatingIPResponseError(true, true,
			networkAuthorityValidationError("owned delegation removal acknowledgement differs"))
	}
	return result, nil
}
