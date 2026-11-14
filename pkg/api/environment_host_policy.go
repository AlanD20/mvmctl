package api

import (
	"context"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// VMToHostTrafficPolicyEnsure binds workflow ownership to a root-admitted host grant incarnation.
func (op *Operation) VMToHostTrafficPolicyEnsure(
	ctx context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentVMToHostIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := vmHostTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyVMHostTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID: root.PolicyID, Incarnation: root.Incarnation, WasCreated: root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectVMToHost(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// VMToHostTrafficPolicyRemoveOwned cannot revoke another workflow's or a recreated grant's incarnation.
func (op *Operation) VMToHostTrafficPolicyRemoveOwned(
	ctx context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	intent, err := op.environmentVMToHostIntent(ctx, input)
	if err != nil {
		return err
	}
	request := vmHostTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveVMHostTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedVMToHost(
		ctx, intent.ID, ownership.ClaimID, ownership.Incarnation,
	); err != nil {
		return trafficPolicyProjectionError(intent.ID, true, err)
	}
	op.AuditLog.LogOperation("policy.environment.remove", map[string]any{"id": intent.ID}, "")
	return nil
}

func (op *Operation) environmentVMToHostIntent(
	ctx context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
) (*model.VMToHostTrafficAllowIntent, error) {
	source, err := input.ResolveSourceVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID)); err != nil {
		return nil, err
	}
	protocol, first, last, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToHostTrafficAllowIntent{
		SourceVMID: source.ID, Protocol: protocol, DestinationPortStart: first, DestinationPortEnd: last,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToHostTrafficPolicyDigest(*intent)
	return intent, nil
}

// NetworkToHostTrafficPolicyEnsure binds workflow ownership to a root-admitted host grant incarnation.
func (op *Operation) NetworkToHostTrafficPolicyEnsure(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentNetworkToHostIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := networkHostTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyNetworkHostTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID: root.PolicyID, Incarnation: root.Incarnation, WasCreated: root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectNetworkToHost(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// NetworkToHostTrafficPolicyRemoveOwned cannot revoke another workflow's or a recreated grant's incarnation.
func (op *Operation) NetworkToHostTrafficPolicyRemoveOwned(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	intent, err := op.environmentNetworkToHostIntent(ctx, input)
	if err != nil {
		return err
	}
	request := networkHostTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveNetworkHostTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedNetworkToHost(
		ctx, intent.ID, ownership.ClaimID, ownership.Incarnation,
	); err != nil {
		return trafficPolicyProjectionError(intent.ID, true, err)
	}
	op.AuditLog.LogOperation("policy.environment.remove", map[string]any{"id": intent.ID}, "")
	return nil
}

func (op *Operation) environmentNetworkToHostIntent(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyCreateInput,
) (*model.NetworkToHostTrafficAllowIntent, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}

	protocol, first, last, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.NetworkToHostTrafficAllowIntent{
		SourceNetworkID: source.ID, Protocol: protocol, DestinationPortStart: first, DestinationPortEnd: last,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.NetworkToHostTrafficPolicyDigest(*intent)
	return intent, nil
}
