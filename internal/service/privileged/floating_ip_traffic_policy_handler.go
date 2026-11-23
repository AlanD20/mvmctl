package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func handleApplyNetworkFloatingIPTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkFloatingIPTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkFloatingIPTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyNetworkFloatingIPTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyApplyAction, result, deps)
}

func handleApplyVMFloatingIPTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMFloatingIPTrafficPolicyRequest](
		ctx,
		reader,
		network.VMFloatingIPTrafficPolicyApplyAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ApplyVMFloatingIPTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMFloatingIPTrafficPolicyApplyAction, result, deps)
}

func handleRemoveNetworkFloatingIPTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.NetworkFloatingIPTrafficPolicyRequest](
		ctx,
		reader,
		network.NetworkFloatingIPTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveNetworkFloatingIPTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.NetworkFloatingIPTrafficPolicyRemoveAction, result, deps)
}

func handleRemoveVMFloatingIPTrafficPolicy(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps controlDeps,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.VMFloatingIPTrafficPolicyRequest](
		ctx,
		reader,
		network.VMFloatingIPTrafficPolicyRemoveAction,
		0,
	)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().RemoveVMFloatingIPTrafficPolicy(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.PolicyID()); err != nil {
		return deliverOperationError(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.VMFloatingIPTrafficPolicyRemoveAction, result, deps)
}
