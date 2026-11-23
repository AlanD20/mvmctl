package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func handleLabelTrafficPolicy(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps, action string,
) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.LabelTrafficPolicyRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	server := network.NewTrafficPolicyServer()
	var result network.TrafficPolicyMutationResult
	switch action {
	case network.LabelTrafficPolicyApplyAction:
		result, err = server.ApplyLabelTrafficPolicy(ctx, caller.uid, request.Body)
	case network.LabelTrafficPolicyRemoveAction:
		result, err = server.RemoveLabelTrafficPolicy(ctx, caller.uid, request.Body)
	}
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleListLabelTrafficPolicies(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = network.LabelTrafficPolicyListAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	if _, err := privilegedwire.DecodeRequest[network.LabelTrafficPolicyListRequest](ctx, reader, action, 0); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := network.NewTrafficPolicyServer().ListLabelTrafficPolicies(ctx, caller.uid)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
