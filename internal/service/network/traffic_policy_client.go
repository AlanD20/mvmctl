package network

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// TrafficPolicyClient exposes named VM and managed-host traffic-policy mutations.
type TrafficPolicyClient struct{}

// NewTrafficPolicyClient binds policy mutations to the fixed installed privileged transport.
func NewTrafficPolicyClient() *TrafficPolicyClient { return &TrafficPolicyClient{} }

// ApplyNetworkTrafficPolicy installs a network-source grant before local projection.
func (client *TrafficPolicyClient) ApplyNetworkTrafficPolicy(
	ctx context.Context,
	request NetworkTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkTrafficPolicyApplyAction, request, request.PolicyID())
}

// ApplyVMTrafficPolicy installs an exact VM-source grant before local projection.
func (client *TrafficPolicyClient) ApplyVMTrafficPolicy(
	ctx context.Context,
	request VMTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMTrafficPolicyApplyAction, request, request.PolicyID())
}

// RemoveNetworkTrafficPolicy revokes a network-source grant before local deletion.
func (client *TrafficPolicyClient) RemoveNetworkTrafficPolicy(
	ctx context.Context,
	request NetworkTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, NetworkTrafficPolicyRemoveAction, request, request.PolicyID())
}

// RemoveVMTrafficPolicy revokes an exact VM-source grant before local deletion.
func (client *TrafficPolicyClient) RemoveVMTrafficPolicy(
	ctx context.Context,
	request VMTrafficPolicyRequest,
) (TrafficPolicyMutationResult, error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return exchangeTrafficPolicy(ctx, client, VMTrafficPolicyRemoveAction, request, request.PolicyID())
}

func exchangeTrafficPolicy[Request NetworkTrafficPolicyRequest | VMTrafficPolicyRequest |
	VMHostTrafficPolicyRequest | NetworkHostTrafficPolicyRequest |
	VMFloatingIPTrafficPolicyRequest | NetworkFloatingIPTrafficPolicyRequest | LabelTrafficPolicyRequest](
	ctx context.Context, client *TrafficPolicyClient, action string, request Request, expectedID string,
) (TrafficPolicyMutationResult, error) {
	result, err := exchangeTrafficPolicyFrame[TrafficPolicyMutationResult](ctx, client, action, request)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := result.Validate(expectedID); err != nil {
		return TrafficPolicyMutationResult{}, trafficPolicyResponseError(true, err)
	}
	return result, nil
}

func exchangeTrafficPolicyFrame[Result any, Request any](
	ctx context.Context, client *TrafficPolicyClient, action string, request Request,
) (Result, error) {
	var zero Result
	if ctx == nil || client == nil {
		return zero, errs.New(
			errs.CodeValidationFailed,
			"traffic policy client and context are required",
		)
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
		return zero, trafficPolicyResponseError(outcome.ProcessStarted(), outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[Result](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.Response()), action)
	if err != nil {
		return zero, trafficPolicyResponseError(true, errors.Join(err, outcome.Diagnostic()))
	}
	if operationErr := response.OperationError(); operationErr != nil {
		return zero, operationErr
	}
	return response.Result(), nil
}

func trafficPolicyResponseError(started bool, cause error) error {
	return errs.WrapMsg(
		errs.CodePolicySyncFailed,
		"root traffic policy response is not authoritative",
		cause,
		errs.WithClass(
			errs.ClassInternal,
		),
		errs.WithDetails(map[string]any{"process_started": started, "outcome_unknown": started}),
	)
}
