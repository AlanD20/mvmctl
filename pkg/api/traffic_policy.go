package api

import (
	"context"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// NetworkToVMTrafficPolicyCreate applies a root-authorized grant before projecting its immutable identity locally.
func (op *Operation) NetworkToVMTrafficPolicyCreate(
	ctx context.Context, input inputs.NetworkToVMTrafficPolicyCreateInput,
) (*results.NetworkToVMTrafficPolicy, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	destination, err := input.ResolveDestinationVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID))
	if err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID: source.ID, DestinationVMID: destination.ID, Protocol: protocol,
		DestinationPortStart: start, DestinationPortEnd: end, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.NetworkToVMTrafficPolicyDigest(*intent)
	request := networkTrafficPolicyRequest(intent)
	if _, err := op.trafficPolicyClient.ApplyNetworkTrafficPolicy(ctx, request); err != nil {
		return nil, err
	}
	existing, err := op.Repos.TrafficPolicy.GetNetworkToVM(ctx, intent.ID)
	if err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	if existing != nil {
		if crypto.NetworkToVMTrafficPolicyDigest(*existing) != intent.ID {
			return nil, trafficPolicyProjectionError(intent.ID, false,
				errs.New(errs.CodePolicyCreateFailed, "local traffic policy identity conflicts with root intent"))
		}
		intent = existing
	} else if err := op.Services.TrafficPolicy.CreateNetworkToVM(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.network.create", map[string]any{"id": intent.ID}, "")
	return networkTrafficPolicyResult(intent, source, destination, destinationNetwork), nil
}

// VMToVMTrafficPolicyCreate applies an exact root-authorized VM grant before local projection.
func (op *Operation) VMToVMTrafficPolicyCreate(
	ctx context.Context, input inputs.VMToVMTrafficPolicyCreateInput,
) (*results.VMToVMTrafficPolicy, error) {
	source, destination, err := input.ResolveVMs(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID))
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID))
	if err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToVMTrafficAllowIntent{
		SourceVMID: source.ID, DestinationVMID: destination.ID, Protocol: protocol,
		DestinationPortStart: start, DestinationPortEnd: end, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToVMTrafficPolicyDigest(*intent)
	request := vmTrafficPolicyRequest(intent)
	if _, err := op.trafficPolicyClient.ApplyVMTrafficPolicy(ctx, request); err != nil {
		return nil, err
	}
	existing, err := op.Repos.TrafficPolicy.GetVMToVM(ctx, intent.ID)
	if err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	if existing != nil {
		if crypto.VMToVMTrafficPolicyDigest(*existing) != intent.ID {
			return nil, trafficPolicyProjectionError(intent.ID, false,
				errs.New(errs.CodePolicyCreateFailed, "local traffic policy identity conflicts with root intent"))
		}
		intent = existing
	} else if err := op.Services.TrafficPolicy.CreateVMToVM(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.vm.create", map[string]any{"id": intent.ID}, "")
	return vmTrafficPolicyResult(intent, source, destination, sourceNetwork, destinationNetwork), nil
}

// NetworkToVMTrafficPolicyList returns network-source desired grants with current resource names.
func (op *Operation) NetworkToVMTrafficPolicyList(ctx context.Context) ([]*results.NetworkToVMTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListNetworkToVM(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list network traffic policies", err)
	}
	items := make([]*results.NetworkToVMTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.networkTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// VMToVMTrafficPolicyList returns exact VM-source desired grants with current resource names.
func (op *Operation) VMToVMTrafficPolicyList(ctx context.Context) ([]*results.VMToVMTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListVMToVM(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list VM traffic policies", err)
	}
	items := make([]*results.VMToVMTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.vmTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// NetworkToVMTrafficPolicyInspect resolves one network-source grant by immutable ID or prefix.
func (op *Operation) NetworkToVMTrafficPolicyInspect(
	ctx context.Context, input inputs.NetworkToVMTrafficPolicyInput,
) (*results.NetworkToVMTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one network traffic policy")
	}
	return op.networkTrafficPolicyResult(ctx, intents[0])
}

// VMToVMTrafficPolicyInspect resolves one exact VM-source grant by immutable ID or prefix.
func (op *Operation) VMToVMTrafficPolicyInspect(
	ctx context.Context, input inputs.VMToVMTrafficPolicyInput,
) (*results.VMToVMTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one VM traffic policy")
	}
	return op.vmTrafficPolicyResult(ctx, intents[0])
}

// NetworkToVMTrafficPolicyRemove revokes each root grant before deleting its local projection.
func (op *Operation) NetworkToVMTrafficPolicyRemove(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		request := networkTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(
				errs.CodePolicyRemoveFailed,
				"local traffic policy identity is not canonical",
				errs.WithEntity(intent.ID),
			)
		}
		if _, err := op.trafficPolicyClient.RemoveNetworkTrafficPolicy(ctx, request); err != nil {
			return err
		}
		ids = append(ids, intent.ID)
	}
	if err := op.Services.TrafficPolicy.RemoveNetworkToVM(ctx, ids); err != nil {
		return trafficPolicyProjectionError("", true, err)
	}
	for _, id := range ids {
		op.AuditLog.LogOperation("policy.traffic.network.remove", map[string]any{"id": id}, "")
	}
	return nil
}

// VMToVMTrafficPolicyRemove revokes each directional root grant before deleting its local projection.
func (op *Operation) VMToVMTrafficPolicyRemove(ctx context.Context, input inputs.VMToVMTrafficPolicyInput) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		request := vmTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(
				errs.CodePolicyRemoveFailed,
				"local traffic policy identity is not canonical",
				errs.WithEntity(intent.ID),
			)
		}
		if _, err := op.trafficPolicyClient.RemoveVMTrafficPolicy(ctx, request); err != nil {
			return err
		}
		ids = append(ids, intent.ID)
	}
	if err := op.Services.TrafficPolicy.RemoveVMToVM(ctx, ids); err != nil {
		return trafficPolicyProjectionError("", true, err)
	}
	for _, id := range ids {
		op.AuditLog.LogOperation("policy.traffic.vm.remove", map[string]any{"id": id}, "")
	}
	return nil
}

func networkTrafficPolicyRequest(
	intent *model.NetworkToVMTrafficAllowIntent,
) networkservice.NetworkTrafficPolicyRequest {
	return networkservice.NetworkTrafficPolicyRequest{
		SourceNetworkID: intent.SourceNetworkID,
		DestinationVMID: intent.DestinationVMID,
		Protocol:        intent.Protocol,
		PortStart:       uint16(intent.DestinationPortStart),
		PortEnd:         uint16(intent.DestinationPortEnd),
	}
}

func vmTrafficPolicyRequest(intent *model.VMToVMTrafficAllowIntent) networkservice.VMTrafficPolicyRequest {
	return networkservice.VMTrafficPolicyRequest{
		SourceVMID:      intent.SourceVMID,
		DestinationVMID: intent.DestinationVMID,
		Protocol:        intent.Protocol,
		PortStart:       uint16(intent.DestinationPortStart),
		PortEnd:         uint16(intent.DestinationPortEnd),
	}
}

func trafficPolicyProjectionError(id string, revoked bool, cause error) error {
	return errs.WrapMsg(
		errs.CodeDatabaseError,
		"root traffic policy mutation completed but local projection failed; retry the same operation",
		cause,
		errs.WithEntity(id),
		errs.WithDetails(map[string]any{"root_policy_mutated": true, "revoked": revoked, "projection_failed": true}),
	)
}

func (op *Operation) trafficPolicyNetwork(ctx context.Context, id string) (*model.NetworkItem, error) {
	if id == "" {
		return nil, errs.New(errs.CodePolicyEndpointInvalid, "traffic policy VM requires a managed network")
	}
	item, err := op.Repos.Network.Get(ctx, id)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve traffic policy network", err)
	}
	if item == nil || item.DeletedAt != nil {
		return nil, errs.NotFound(
			errs.CodeNetworkNotFound,
			"traffic policy network no longer exists",
			errs.WithEntity(id),
		)
	}
	return item, nil
}

func (op *Operation) trafficPolicyVM(ctx context.Context, id string) (*model.VMItem, error) {
	item, err := op.Repos.VM.Get(ctx, id)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve traffic policy VM", err)
	}
	if item == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "traffic policy VM no longer exists", errs.WithEntity(id))
	}
	return item, nil
}

func (op *Operation) networkTrafficPolicyResult(
	ctx context.Context, intent *model.NetworkToVMTrafficAllowIntent,
) (*results.NetworkToVMTrafficPolicy, error) {
	source, err := op.trafficPolicyNetwork(ctx, intent.SourceNetworkID)
	if err != nil {
		return nil, err
	}
	destination, err := op.trafficPolicyVM(ctx, intent.DestinationVMID)
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID))
	if err != nil {
		return nil, err
	}
	return networkTrafficPolicyResult(intent, source, destination, destinationNetwork), nil
}

func (op *Operation) vmTrafficPolicyResult(
	ctx context.Context, intent *model.VMToVMTrafficAllowIntent,
) (*results.VMToVMTrafficPolicy, error) {
	source, err := op.trafficPolicyVM(ctx, intent.SourceVMID)
	if err != nil {
		return nil, err
	}
	destination, err := op.trafficPolicyVM(ctx, intent.DestinationVMID)
	if err != nil {
		return nil, err
	}
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID))
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID))
	if err != nil {
		return nil, err
	}
	return vmTrafficPolicyResult(intent, source, destination, sourceNetwork, destinationNetwork), nil
}

func networkTrafficPolicyResult(
	intent *model.NetworkToVMTrafficAllowIntent, source *model.NetworkItem,
	destination *model.VMItem, destinationNetwork *model.NetworkItem,
) *results.NetworkToVMTrafficPolicy {
	return &results.NetworkToVMTrafficPolicy{
		NetworkToVMTrafficAllowIntent: *intent, SourceNetworkName: source.Name,
		DestinationVMName: destination.Name, DestinationNetworkID: destinationNetwork.ID,
		DestinationNetworkName: destinationNetwork.Name,
		NetworkRelation:        trafficNetworkRelation(source.ID, destinationNetwork.ID),
	}
}

func vmTrafficPolicyResult(
	intent *model.VMToVMTrafficAllowIntent, source, destination *model.VMItem,
	sourceNetwork, destinationNetwork *model.NetworkItem,
) *results.VMToVMTrafficPolicy {
	return &results.VMToVMTrafficPolicy{
		VMToVMTrafficAllowIntent: *intent, SourceVMName: source.Name,
		SourceNetworkID: sourceNetwork.ID, SourceNetworkName: sourceNetwork.Name,
		DestinationVMName: destination.Name, DestinationNetworkID: destinationNetwork.ID,
		DestinationNetworkName: destinationNetwork.Name,
		NetworkRelation:        trafficNetworkRelation(sourceNetwork.ID, destinationNetwork.ID),
	}
}

func trafficNetworkRelation(source, destination string) model.ManagedNetworkRelation {
	if source == destination {
		return model.ManagedNetworkRelationSame
	}
	return model.ManagedNetworkRelationCross
}
