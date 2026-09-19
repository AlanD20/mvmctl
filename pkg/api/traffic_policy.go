package api

import (
	"context"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// NetworkToVMTrafficPolicyCreate records a network-source allow intent, including same-network destinations.
// Desired intent is not evidence of applied kernel enforcement.
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
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, destination.NetworkID)
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
	if err := op.Services.TrafficPolicy.CreateNetworkToVM(ctx, intent); err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("policy.traffic.network.create", map[string]any{"id": intent.ID}, "")
	return networkTrafficPolicyResult(intent, source, destination, destinationNetwork), nil
}

// VMToVMTrafficPolicyCreate records an exact directional VM-source traffic allow intent.
// Desired intent is not evidence of applied kernel enforcement.
func (op *Operation) VMToVMTrafficPolicyCreate(
	ctx context.Context, input inputs.VMToVMTrafficPolicyCreateInput,
) (*results.VMToVMTrafficPolicy, error) {
	source, destination, err := input.ResolveVMs(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, source.NetworkID)
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, destination.NetworkID)
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
	if err := op.Services.TrafficPolicy.CreateVMToVM(ctx, intent); err != nil {
		return nil, err
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

// NetworkToVMTrafficPolicyRemove resolves network-source grants and preserves them until safe revocation is available.
func (op *Operation) NetworkToVMTrafficPolicyRemove(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	return errs.New(errs.CodePolicyRemoveFailed,
		"traffic policy removal requires authoritative firewall reconciliation; desired intent was retained",
		errs.WithDetails(map[string]any{"policies": len(intents), "reconciliation_required": true}))
}

// VMToVMTrafficPolicyRemove resolves VM-source grants and preserves them until safe revocation is available.
func (op *Operation) VMToVMTrafficPolicyRemove(ctx context.Context, input inputs.VMToVMTrafficPolicyInput) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	return errs.New(errs.CodePolicyRemoveFailed,
		"traffic policy removal requires authoritative firewall reconciliation; desired intent was retained",
		errs.WithDetails(map[string]any{"policies": len(intents), "reconciliation_required": true}))
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
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, destination.NetworkID)
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
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, source.NetworkID)
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, destination.NetworkID)
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
