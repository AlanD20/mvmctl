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

// VMToHostTrafficPolicyCreate applies a root-authorized gateway grant before projecting it locally.
func (op *Operation) VMToHostTrafficPolicyCreate(
	ctx context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
) (*results.VMToHostTrafficPolicy, error) {
	source, err := input.ResolveSourceVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID)); err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToHostTrafficAllowIntent{
		SourceVMID: source.ID, Protocol: protocol,
		DestinationPortStart: start, DestinationPortEnd: end, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToHostTrafficPolicyDigest(*intent)
	root, err := op.trafficPolicyClient.ApplyVMHostTrafficPolicy(ctx, vmHostTrafficPolicyRequest(intent))
	if err != nil {
		return nil, err
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectVMToHost(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.vm-host.create", map[string]any{"id": intent.ID}, "")
	return op.VMToHostTrafficPolicyInspect(ctx, inputs.VMToHostTrafficPolicyInput{Identifiers: []string{intent.ID}})
}

// VMToHostTrafficPolicyList returns local desired grants, not proof of applied kernel access.
func (op *Operation) VMToHostTrafficPolicyList(ctx context.Context) ([]*results.VMToHostTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListVMToHost(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list host traffic policies", err)
	}
	items := make([]*results.VMToHostTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.vmHostTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// VMToHostTrafficPolicyInspect resolves exactly one local grant by canonical identity or unambiguous prefix.
func (op *Operation) VMToHostTrafficPolicyInspect(
	ctx context.Context, input inputs.VMToHostTrafficPolicyInput,
) (*results.VMToHostTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one host traffic policy")
	}
	return op.vmHostTrafficPolicyResult(ctx, intents[0])
}

// VMToHostTrafficPolicyRemove revokes exact root grants before deleting their local projections.
func (op *Operation) VMToHostTrafficPolicyRemove(
	ctx context.Context, input inputs.VMToHostTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		request := vmHostTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(errs.CodePolicyRemoveFailed, "local host traffic policy identity is not canonical",
				errs.WithEntity(intent.ID))
		}
		if _, err := op.trafficPolicyClient.RemoveVMHostTrafficPolicy(ctx, request); err != nil {
			return err
		}
		ids = append(ids, intent.ID)
	}
	if err := op.Services.TrafficPolicy.RemoveVMToHost(ctx, ids); err != nil {
		return trafficPolicyProjectionError("", true, err)
	}
	for _, id := range ids {
		op.AuditLog.LogOperation("policy.traffic.vm-host.remove", map[string]any{"id": id}, "")
	}
	return nil
}

func vmHostTrafficPolicyRequest(intent *model.VMToHostTrafficAllowIntent) networkservice.VMHostTrafficPolicyRequest {
	return networkservice.VMHostTrafficPolicyRequest{
		SourceVMID: intent.SourceVMID, Protocol: intent.Protocol,
		PortStart: uint16(intent.DestinationPortStart), PortEnd: uint16(intent.DestinationPortEnd),
	}
}

func (op *Operation) vmHostTrafficPolicyResult(
	ctx context.Context, intent *model.VMToHostTrafficAllowIntent,
) (*results.VMToHostTrafficPolicy, error) {
	source, err := op.trafficPolicyVM(ctx, intent.SourceVMID)
	if err != nil {
		return nil, err
	}
	network, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID))
	if err != nil {
		return nil, err
	}
	return vmHostTrafficPolicyResult(intent, source, network), nil
}

func vmHostTrafficPolicyResult(
	intent *model.VMToHostTrafficAllowIntent, source *model.VMItem, network *model.NetworkItem,
) *results.VMToHostTrafficPolicy {
	return &results.VMToHostTrafficPolicy{
		VMToHostTrafficAllowIntent: *intent,
		SourceVMName:               source.Name, Gateway: network.IPv4Gateway,
		SourceNetworkID: network.ID, SourceNetworkName: network.Name,
	}
}

// NetworkToHostTrafficPolicyCreate applies a root-authorized gateway grant before projecting it locally.
func (op *Operation) NetworkToHostTrafficPolicyCreate(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyCreateInput,
) (*results.NetworkToHostTrafficPolicy, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.NetworkToHostTrafficAllowIntent{
		SourceNetworkID: source.ID, Protocol: protocol,
		DestinationPortStart: start, DestinationPortEnd: end, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.NetworkToHostTrafficPolicyDigest(*intent)
	root, err := op.trafficPolicyClient.ApplyNetworkHostTrafficPolicy(ctx, networkHostTrafficPolicyRequest(intent))
	if err != nil {
		return nil, err
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectNetworkToHost(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.network-host.create", map[string]any{"id": intent.ID}, "")
	return op.NetworkToHostTrafficPolicyInspect(ctx,
		inputs.NetworkToHostTrafficPolicyInput{Identifiers: []string{intent.ID}})
}

// NetworkToHostTrafficPolicyList returns local desired grants, not proof of applied kernel access.
func (op *Operation) NetworkToHostTrafficPolicyList(
	ctx context.Context,
) ([]*results.NetworkToHostTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListNetworkToHost(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list host traffic policies", err)
	}
	items := make([]*results.NetworkToHostTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.networkHostTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// NetworkToHostTrafficPolicyInspect resolves exactly one local grant by canonical identity or unambiguous prefix.
func (op *Operation) NetworkToHostTrafficPolicyInspect(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyInput,
) (*results.NetworkToHostTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one host traffic policy")
	}
	return op.networkHostTrafficPolicyResult(ctx, intents[0])
}

// NetworkToHostTrafficPolicyRemove revokes exact root grants before deleting their local projections.
func (op *Operation) NetworkToHostTrafficPolicyRemove(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		request := networkHostTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(errs.CodePolicyRemoveFailed, "local host traffic policy identity is not canonical",
				errs.WithEntity(intent.ID))
		}
		if _, err := op.trafficPolicyClient.RemoveNetworkHostTrafficPolicy(ctx, request); err != nil {
			return err
		}
		ids = append(ids, intent.ID)
	}
	if err := op.Services.TrafficPolicy.RemoveNetworkToHost(ctx, ids); err != nil {
		return trafficPolicyProjectionError("", true, err)
	}
	for _, id := range ids {
		op.AuditLog.LogOperation("policy.traffic.network-host.remove", map[string]any{"id": id}, "")
	}
	return nil
}

func networkHostTrafficPolicyRequest(
	intent *model.NetworkToHostTrafficAllowIntent,
) networkservice.NetworkHostTrafficPolicyRequest {
	return networkservice.NetworkHostTrafficPolicyRequest{
		SourceNetworkID: intent.SourceNetworkID, Protocol: intent.Protocol,
		PortStart: uint16(intent.DestinationPortStart), PortEnd: uint16(intent.DestinationPortEnd),
	}
}

func (op *Operation) networkHostTrafficPolicyResult(
	ctx context.Context, intent *model.NetworkToHostTrafficAllowIntent,
) (*results.NetworkToHostTrafficPolicy, error) {
	source, err := op.trafficPolicyNetwork(ctx, intent.SourceNetworkID)
	if err != nil {
		return nil, err
	}

	return networkHostTrafficPolicyResult(intent, source), nil
}

func networkHostTrafficPolicyResult(
	intent *model.NetworkToHostTrafficAllowIntent, source *model.NetworkItem,
) *results.NetworkToHostTrafficPolicy {
	return &results.NetworkToHostTrafficPolicy{
		NetworkToHostTrafficAllowIntent: *intent,
		SourceNetworkName:               source.Name, Gateway: source.IPv4Gateway,
	}
}
