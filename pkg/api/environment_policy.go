package api

import (
	"context"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// EnvironmentPolicyAPI provides atomic receipt-based ownership for workflow policy steps.
type EnvironmentPolicyAPI interface {
	NetworkToVMTrafficPolicyEnsure(
		context.Context,
		inputs.NetworkToVMTrafficPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) (*results.EnvironmentPolicyReceipt, error)
	NetworkToVMTrafficPolicyRemoveOwned(
		context.Context,
		inputs.NetworkToVMTrafficPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) error
	VMToVMTrafficPolicyEnsure(
		context.Context,
		inputs.VMToVMTrafficPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) (*results.EnvironmentPolicyReceipt, error)
	VMToVMTrafficPolicyRemoveOwned(
		context.Context,
		inputs.VMToVMTrafficPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) error
	ExecPolicyEnsure(
		context.Context,
		inputs.VMToVMExecPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) (*results.EnvironmentPolicyReceipt, error)
	ExecPolicyRemoveOwned(
		context.Context,
		inputs.VMToVMExecPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput,
	) error
}

// NetworkToVMTrafficPolicyEnsure returns ownership only after the root grant mutation is acknowledged.
func (op *Operation) NetworkToVMTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentNetworkToVMIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := networkTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyNetworkTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID:    root.PolicyID,
		Incarnation: root.Incarnation,
		WasCreated:  root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectNetworkToVM(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// NetworkToVMTrafficPolicyRemoveOwned revokes only the durable workflow claim and recorded incarnation.
func (op *Operation) NetworkToVMTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	intent, err := op.environmentNetworkToVMIntent(ctx, input)
	if err != nil {
		return err
	}
	request := networkTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveNetworkTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedNetworkToVM(
		ctx,
		intent.ID,
		ownership.ClaimID,
		ownership.Incarnation,
	); err != nil {
		return trafficPolicyProjectionError(intent.ID, true, err)
	}
	op.AuditLog.LogOperation("policy.environment.remove", map[string]any{"id": intent.ID}, "")
	return nil
}

func (op *Operation) environmentNetworkToVMIntent(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyCreateInput,
) (*model.NetworkToVMTrafficAllowIntent, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	destination, err := input.ResolveDestinationVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID)); err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID:      source.ID,
		DestinationVMID:      destination.ID,
		Protocol:             protocol,
		DestinationPortStart: start,
		DestinationPortEnd:   end,
		CreatedAt:            timestamp,
		UpdatedAt:            timestamp,
	}
	intent.ID = crypto.NetworkToVMTrafficPolicyDigest(*intent)
	return intent, nil
}

// VMToVMTrafficPolicyEnsure returns ownership only after the root grant mutation is acknowledged.
func (op *Operation) VMToVMTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.VMToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentVMToVMIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := vmTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyVMTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID:    root.PolicyID,
		Incarnation: root.Incarnation,
		WasCreated:  root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectVMToVM(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// VMToVMTrafficPolicyRemoveOwned revokes only the durable workflow claim and recorded incarnation.
func (op *Operation) VMToVMTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	intent, err := op.environmentVMToVMIntent(ctx, input)
	if err != nil {
		return err
	}
	request := vmTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveVMTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedVMToVM(
		ctx,
		intent.ID,
		ownership.ClaimID,
		ownership.Incarnation,
	); err != nil {
		return trafficPolicyProjectionError(intent.ID, true, err)
	}
	op.AuditLog.LogOperation("policy.environment.remove", map[string]any{"id": intent.ID}, "")
	return nil
}

func (op *Operation) environmentVMToVMIntent(
	ctx context.Context,
	input inputs.VMToVMTrafficPolicyCreateInput,
) (*model.VMToVMTrafficAllowIntent, error) {
	source, destination, err := input.ResolveVMs(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID)); err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(destination.NetworkID)); err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToVMTrafficAllowIntent{
		SourceVMID:           source.ID,
		DestinationVMID:      destination.ID,
		Protocol:             protocol,
		DestinationPortStart: start,
		DestinationPortEnd:   end,
		CreatedAt:            timestamp,
		UpdatedAt:            timestamp,
	}
	intent.ID = crypto.VMToVMTrafficPolicyDigest(*intent)
	return intent, nil
}

// ExecPolicyEnsure atomically inserts or recovers the exact directional grant and claim.
func (op *Operation) ExecPolicyEnsure(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	source, destination, err := input.ResolveVMs(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToVMExecAllowIntent{SourceVMID: source.ID, DestinationVMID: destination.ID,
		TargetUser: input.TargetUser, CreatedAt: timestamp, UpdatedAt: timestamp, ClaimID: ownership.ClaimID}
	intent.ID = crypto.VMToVMExecPolicyDigest(*intent)
	existing, err := op.Services.ExecPolicy.EnsureOwned(ctx, intent, ownership.Incarnation)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("policy.exec.environment.ensure", map[string]any{"id": existing.ID}, "")
	return &results.EnvironmentPolicyReceipt{
		PolicyID:    existing.ID,
		Incarnation: existing.Incarnation,
		WasCreated:  existing.ClaimID == ownership.ClaimID,
	}, nil
}

// ExecPolicyRemoveOwned removes only the exact directional workflow-owned grant.
func (op *Operation) ExecPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	if err := input.ValidateOwnedRemoval(); err != nil {
		return err
	}
	id := crypto.VMToVMExecPolicyDigest(
		model.VMToVMExecAllowIntent{
			SourceVMID:      input.SourceVM,
			DestinationVMID: input.DestinationVM,
			TargetUser:      input.TargetUser,
		},
	)
	if err := op.Services.ExecPolicy.RemoveOwned(ctx, id, ownership.ClaimID, ownership.Incarnation); err != nil {
		return err
	}
	op.AuditLog.LogOperation("policy.exec.environment.remove", map[string]any{"id": id}, "")
	return nil
}
