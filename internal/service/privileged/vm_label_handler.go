package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/jailer"
)

func handleVMLabelUpdate(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMLabelUpdateAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	message, err := privilegedwire.DecodeRequest[jailer.VMLabelUpdateRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	request := message.Body
	if err := request.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := updateOwnedVMLabels(ctx, caller, request)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func updateOwnedVMLabels(
	ctx context.Context, caller callerIdentity, request jailer.VMLabelUpdateRequest,
) (_ jailer.VMLabelUpdateResult, returnErr error) {
	lease, err := jailer.AcquireVMLabelOwnership(ctx, caller.uid, caller.gid, request.VMID, request.CacheRoot)
	if err != nil {
		return jailer.VMLabelUpdateResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	ownerUID, vmID, err := lease.Binding(ctx)
	if err != nil {
		return jailer.VMLabelUpdateResult{}, err
	}
	revision, err := firewall.UpdateVMLabels(ctx, ownerUID, vmID, request.ExpectedRevision, request.Labels)
	if err != nil {
		return jailer.VMLabelUpdateResult{}, err
	}
	return jailer.VMLabelUpdateResult{VMID: vmID, Revision: revision}, nil
}

func handleVMLabelInspect(
	ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps,
) error {
	const action = jailer.VMLabelInspectAction
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	message, err := privilegedwire.DecodeRequest[jailer.VMLabelInspectRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	request := message.Body
	if err := request.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	result, err := inspectOwnedVMLabels(ctx, caller, request)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func inspectOwnedVMLabels(
	ctx context.Context, caller callerIdentity, request jailer.VMLabelInspectRequest,
) (_ jailer.VMLabelInspectResult, returnErr error) {
	lease, err := jailer.AcquireVMLabelOwnership(ctx, caller.uid, caller.gid, request.VMID, request.CacheRoot)
	if err != nil {
		return jailer.VMLabelInspectResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	ownerUID, vmID, err := lease.Binding(ctx)
	if err != nil {
		return jailer.VMLabelInspectResult{}, err
	}
	labels, revision, err := firewall.ReadVMLabels(ctx, ownerUID, vmID)
	if err != nil {
		return jailer.VMLabelInspectResult{}, err
	}
	return jailer.VMLabelInspectResult{VMID: vmID, Revision: revision, Labels: labels}, nil
}
