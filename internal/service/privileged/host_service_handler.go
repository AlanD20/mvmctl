package privileged

import (
	"context"
	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func handleInspectHostService(ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.HostServiceRequest](ctx, reader, network.HostServiceInspectAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceInspectAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceInspectAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceInspectAction, err, deps)
	}
	result, err := network.NewHostServiceServer().InspectHostService(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceInspectAction, err, deps)
	}
	if err := result.Validate(request.Body.NetworkID); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceInspectAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.HostServiceInspectAction, result, deps)
}

func handleApplyHostService(ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.HostServiceRequest](ctx, reader, network.HostServiceApplyAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceApplyAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceApplyAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceApplyAction, err, deps)
	}
	result, err := network.NewHostServiceServer().ApplyHostService(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceApplyAction, err, deps)
	}
	if err := result.Validate(request.Body.NetworkID); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceApplyAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.HostServiceApplyAction, result, deps)
}

func handleRemoveHostService(ctx context.Context, endpoint controlEndpoint, caller callerIdentity, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	request, err := privilegedwire.DecodeRequest[network.HostServiceRequest](ctx, reader, network.HostServiceRemoveAction, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceRemoveAction, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceRemoveAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceRemoveAction, err, deps)
	}
	result, err := network.NewHostServiceServer().RemoveHostService(ctx, caller.uid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceRemoveAction, err, deps)
	}
	if err := result.Validate(request.Body.NetworkID); err != nil {
		return deliverOperationError(ctx, endpoint, network.HostServiceRemoveAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, network.HostServiceRemoveAction, result, deps)
}
