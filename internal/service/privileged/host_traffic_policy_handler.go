package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func handleApplyNetworkHostTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkHostTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkHostTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyNetworkHostTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkHostTrafficPolicyApplyAction, result, deps)
}

func handleApplyVMHostTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMHostTrafficPolicyRequest](
		ctx,
		reader,
		network.VMHostTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyVMHostTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMHostTrafficPolicyApplyAction, result, deps)
}

func handleRemoveNetworkHostTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkHostTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkHostTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveNetworkHostTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkHostTrafficPolicyRemoveAction, result, deps)
}

func handleRemoveVMHostTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMHostTrafficPolicyRequest](
		ctx,
		reader,
		network.VMHostTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveVMHostTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMHostTrafficPolicyRemoveAction, result, deps)
}
