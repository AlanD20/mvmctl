package api

import (
	"context"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"time"
)

// VMToFloatingIPTrafficPolicyEnsure retains root ownership even if local projection fails.
func (op *Operation) VMToFloatingIPTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentVMToFloatingIPIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := vmFloatingIPTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyVMFloatingIPTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID:    root.PolicyID,
		Incarnation: root.Incarnation,
		WasCreated:  root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectVMToFloatingIP(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// VMToFloatingIPTrafficPolicyRemoveOwned revokes only the exact workflow claim and incarnation.
func (op *Operation) VMToFloatingIPTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	if err := input.ValidateOwnedRemoval(); err != nil {
		return err
	}
	protocol, first, last, err := input.ResolvedTraffic()
	if err != nil {
		return err
	}
	intent := &model.VMToFloatingIPTrafficAllowIntent{SourceVMID: input.SourceVM,
		DestinationFloatingIPID: input.DestinationFloatingIP, Protocol: protocol,
		DestinationPortStart: first, DestinationPortEnd: last}
	intent.ID = crypto.VMToFloatingIPTrafficPolicyDigest(*intent)
	request := vmFloatingIPTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveVMFloatingIPTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedVMToFloatingIP(
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

func (op *Operation) environmentVMToFloatingIPIntent(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyCreateInput,
) (*model.VMToFloatingIPTrafficAllowIntent, error) {
	source, err := input.ResolveSourceVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	destination, err := op.FloatingIPGet(
		ctx,
		inputs.FloatingIPInput{Network: input.DestinationNetwork, Identifier: input.DestinationFloatingIP},
	)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID)); err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, destination.NetworkID); err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToFloatingIPTrafficAllowIntent{
		SourceVMID:              source.ID,
		DestinationFloatingIPID: destination.ID,
		Protocol:                protocol,
		DestinationPortStart:    start,
		DestinationPortEnd:      end,
		CreatedAt:               timestamp,
		UpdatedAt:               timestamp,
	}
	intent.ID = crypto.VMToFloatingIPTrafficPolicyDigest(*intent)
	return intent, nil
}

// NetworkToFloatingIPTrafficPolicyEnsure retains root ownership even if local projection fails.
func (op *Operation) NetworkToFloatingIPTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	intent, err := op.environmentNetworkToFloatingIPIntent(ctx, input)
	if err != nil {
		return nil, err
	}
	request := networkFloatingIPTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	root, err := op.trafficPolicyClient.ApplyNetworkFloatingIPTrafficPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID:    root.PolicyID,
		Incarnation: root.Incarnation,
		WasCreated:  root.WasCreated,
	}
	intent.ClaimID, intent.Incarnation = root.ClaimID, root.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectNetworkToFloatingIP(ctx, intent); err != nil {
		return receipt, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.environment.ensure", map[string]any{"id": intent.ID}, "")
	return receipt, nil
}

// NetworkToFloatingIPTrafficPolicyRemoveOwned revokes only the exact workflow claim and incarnation.
func (op *Operation) NetworkToFloatingIPTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if err := ownership.Validate(); err != nil {
		return err
	}
	if err := input.ValidateOwnedRemoval(); err != nil {
		return err
	}
	protocol, first, last, err := input.ResolvedTraffic()
	if err != nil {
		return err
	}
	intent := &model.NetworkToFloatingIPTrafficAllowIntent{SourceNetworkID: input.SourceNetwork,
		DestinationFloatingIPID: input.DestinationFloatingIP, Protocol: protocol,
		DestinationPortStart: first, DestinationPortEnd: last}
	intent.ID = crypto.NetworkToFloatingIPTrafficPolicyDigest(*intent)
	request := networkFloatingIPTrafficPolicyRequest(intent)
	request.ClaimID, request.Incarnation = ownership.ClaimID, ownership.Incarnation
	if _, err := op.trafficPolicyClient.RemoveNetworkFloatingIPTrafficPolicy(ctx, request); err != nil {
		return err
	}
	if err := op.Repos.TrafficPolicy.DeleteOwnedNetworkToFloatingIP(
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

func (op *Operation) environmentNetworkToFloatingIPIntent(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
) (*model.NetworkToFloatingIPTrafficAllowIntent, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	destination, err := op.FloatingIPGet(
		ctx,
		inputs.FloatingIPInput{Network: input.DestinationNetwork, Identifier: input.DestinationFloatingIP},
	)
	if err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, source.ID); err != nil {
		return nil, err
	}
	if _, err := op.trafficPolicyNetwork(ctx, destination.NetworkID); err != nil {
		return nil, err
	}
	protocol, start, end, err := input.ResolvedTraffic()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.NetworkToFloatingIPTrafficAllowIntent{
		SourceNetworkID:         source.ID,
		DestinationFloatingIPID: destination.ID,
		Protocol:                protocol,
		DestinationPortStart:    start,
		DestinationPortEnd:      end,
		CreatedAt:               timestamp,
		UpdatedAt:               timestamp,
	}
	intent.ID = crypto.NetworkToFloatingIPTrafficPolicyDigest(*intent)
	return intent, nil
}
