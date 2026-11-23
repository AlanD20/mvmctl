package privileged

import (
	"context"
	"errors"
	"slices"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/jailer"
)

func handleApplyExecPolicy(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMExecPolicyApplyAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMExecPolicyApplyRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := applyOwnedExecPolicy(ctx, caller, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func applyOwnedExecPolicy(
	ctx context.Context, caller callerIdentity, request jailer.VMExecPolicyApplyRequest,
) (_ jailer.VMExecPolicyApplyResult, returnErr error) {
	ids := []string{request.Source.VMID, request.Destination.VMID}
	slices.Sort(ids)
	for _, id := range ids {
		if id == "" {
			continue
		}
		lease, err := jailer.AcquireVMLabelOwnership(ctx, caller.uid, caller.gid, id, request.CacheRoot)
		if err != nil {
			return jailer.VMExecPolicyApplyResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	}
	policy, receipt, err := firewall.EnsureVMExecPolicy(
		ctx, caller.uid, request.Source, request.Destination, request.TargetUser, request.ClaimID, request.Incarnation,
	)
	if err != nil {
		return jailer.VMExecPolicyApplyResult{}, err
	}
	return jailer.VMExecPolicyApplyResult{Policy: policy, WasCreated: receipt.WasCreated}, nil
}

func handleRemoveExecPolicy(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMExecPolicyRemoveAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[jailer.VMExecPolicyRemoveRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := firewall.RemoveVMExecPolicy(
		ctx, caller.uid, request.Body.PolicyID, request.Body.ClaimID, request.Body.Incarnation,
	); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(
		ctx,
		endpoint,
		action,
		jailer.VMExecPolicyRemoveResult{PolicyID: request.Body.PolicyID},
		deps,
	)
}

func handleListExecPolicies(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMExecPolicyListAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	if _, err := privilegedwire.DecodeRequest[jailer.VMExecPolicyListRequest](ctx, reader, action, 0); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	policies, err := firewall.ListVMExecPolicies(ctx, caller.uid)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, jailer.VMExecPolicyListResult{Policies: policies}, deps)
}
