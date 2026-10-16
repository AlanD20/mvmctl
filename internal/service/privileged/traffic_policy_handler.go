package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func handleApplyNetworkTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyNetworkTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkTrafficPolicyApplyAction, result, deps)
}

func handleApplyVMTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMTrafficPolicyRequest](
		ctx,
		reader,
		network.VMTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyVMTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMTrafficPolicyApplyAction, result, deps)
}

func handleRemoveNetworkTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveNetworkTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkTrafficPolicyRemoveAction, result, deps)
}

func handleRemoveVMTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMTrafficPolicyRequest](
		ctx,
		reader,
		network.VMTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveVMTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMTrafficPolicyRemoveAction, result, deps)
}
