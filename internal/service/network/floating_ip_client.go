package network

import (
	"bytes"
	"context"
	"errors"
	"net/netip"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// FloatingIPClient exposes only the named root reservation operations.
type FloatingIPClient struct{}

func NewFloatingIPClient() *FloatingIPClient { return &FloatingIPClient{} }

func (client *FloatingIPClient) CreateFloatingIP(
	ctx context.Context, request FloatingIPCreateRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPCreateRequest, FloatingIPResult](
		ctx,
		client,
		FloatingIPCreateAction,
		request,
	)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := result.validateCreate(request); err != nil {
		return FloatingIPResult{}, floatingIPResponseError(true, true, err)
	}
	return result, nil
}

func (client *FloatingIPClient) RemoveFloatingIP(
	ctx context.Context, request FloatingIPRemoveRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPRemoveRequest, FloatingIPResult](
		ctx,
		client,
		FloatingIPRemoveAction,
		request,
	)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := result.validateRemove(request); err != nil {
		return FloatingIPResult{}, floatingIPResponseError(true, true, err)
	}
	return result, nil
}

func (client *FloatingIPClient) ListFloatingIPs(
	ctx context.Context, request FloatingIPListRequest,
) (FloatingIPPage, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPPage{}, err
	}
	page, err := exchangeFloatingIP[FloatingIPListRequest, FloatingIPPage](ctx, client, FloatingIPListAction, request)
	if err != nil {
		return FloatingIPPage{}, err
	}
	if err := page.validate(request); err != nil {
		return FloatingIPPage{}, floatingIPResponseError(true, false, err)
	}
	return page, nil
}

func (client *FloatingIPClient) GetFloatingIP(
	ctx context.Context, request FloatingIPGetRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPGetRequest, FloatingIPResult](ctx, client, FloatingIPGetAction, request)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := result.validate(request.NetworkID); err != nil || result.ID != request.ID {
		return FloatingIPResult{}, floatingIPResponseError(
			true,
			false,
			errors.Join(
				err,
				networkAuthorityValidationError("floating-IP inspection acknowledgement differs from its request"),
			),
		)
	}
	return result, nil
}

func (result FloatingIPResult) validate(networkID string) error {
	address, err := netip.ParseAddr(result.IPv4)
	if result.NetworkID != networkID || !networkIDPattern.MatchString(result.NetworkID) ||
		!vmIDPattern.MatchString(result.ID) || result.Generation == 0 || err != nil ||
		!address.Is4() || !address.IsPrivate() || address.String() != result.IPv4 {
		return networkAuthorityValidationError("floating-IP result has invalid identity, generation or address")
	}
	return validators.EntityName(result.Name, "floating IP", 63)
}

func (result FloatingIPResult) validateCreate(request FloatingIPCreateRequest) error {
	if err := result.validate(request.NetworkID); err != nil {
		return err
	}
	if result.ID != request.ID || result.Name != request.Name || result.Retired ||
		(request.IPv4 != "" && result.IPv4 != request.IPv4) {
		return networkAuthorityValidationError("floating-IP creation acknowledgement differs from its request")
	}
	return nil
}

func (result FloatingIPResult) validateRemove(request FloatingIPRemoveRequest) error {
	if err := result.validate(request.NetworkID); err != nil {
		return err
	}
	if result.ID != request.ID || !result.Retired ||
		(result.Generation != request.ExpectedGeneration &&
			(request.ExpectedGeneration == ^uint64(0) || result.Generation != request.ExpectedGeneration+1)) {
		return networkAuthorityValidationError("floating-IP removal acknowledgement differs from its request")
	}
	return nil
}

func (page FloatingIPPage) validate(request FloatingIPListRequest) error {
	if page.Items == nil || len(page.Items) > floatingIPPageSize {
		return networkAuthorityValidationError("floating-IP inventory has an invalid page size")
	}
	previous := request.AfterID
	names := make(map[string]struct{}, len(page.Items))
	addresses := make(map[string]struct{}, len(page.Items))
	for _, item := range page.Items {
		if err := item.validate(request.NetworkID); err != nil {
			return err
		}
		_, duplicateName := names[item.Name]
		_, duplicateAddress := addresses[item.IPv4]
		if item.Retired || item.ID <= previous || duplicateName || duplicateAddress {
			return networkAuthorityValidationError(
				"floating-IP inventory contains retired, unordered or duplicate resources",
			)
		}
		previous = item.ID
		names[item.Name], addresses[item.IPv4] = struct{}{}, struct{}{}
	}
	if page.NextID != "" && (len(page.Items) != floatingIPPageSize || page.NextID != previous) {
		return networkAuthorityValidationError("floating-IP inventory cursor does not advance over a complete page")
	}
	return nil
}

func exchangeFloatingIP[
	Request FloatingIPCreateRequest | FloatingIPRemoveRequest | FloatingIPListRequest | FloatingIPGetRequest,
	Result FloatingIPResult | FloatingIPPage,
](ctx context.Context, client *FloatingIPClient, action string, request Request) (Result, error) {
	var zero Result
	if ctx == nil || client == nil {
		return zero, networkAuthorityValidationError("floating-IP client and context are required")
	}
	mutation := action == FloatingIPCreateAction || action == FloatingIPRemoveAction
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
	response, err := privilegedwire.DecodeResponse[Result](
		context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()),
		action,
	)
	if err != nil {
		return zero, floatingIPResponseError(true, mutation, errors.Join(err, outcome.Diagnostic()))
	}
	if err := response.OperationError(); err != nil {
		return zero, err
	}
	return response.Result(), nil
}

func floatingIPResponseError(started, mutation bool, cause error) error {
	return errs.WrapMsg(errs.CodeNetworkError, "root floating-IP response is not authoritative", cause,
		errs.WithDetails(map[string]any{"process_started": started, "outcome_unknown": started && mutation}))
}
