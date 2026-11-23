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

// VMToFloatingIPTrafficPolicyCreate installs root-authorized intent before recording its receipt locally.
func (op *Operation) VMToFloatingIPTrafficPolicyCreate(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyCreateInput,
) (*results.VMToFloatingIPTrafficPolicy, error) {
	source, err := input.ResolveSourceVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	destination, err := op.FloatingIPGet(ctx, inputs.FloatingIPInput{
		Network:    input.DestinationNetwork,
		Identifier: input.DestinationFloatingIP,
	})
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := op.trafficPolicyNetwork(ctx, destination.NetworkID)
	if err != nil {
		return nil, err
	}
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID))
	if err != nil {
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
	receipt, err := op.trafficPolicyClient.ApplyVMFloatingIPTrafficPolicy(ctx, vmFloatingIPTrafficPolicyRequest(intent))
	if err != nil {
		return nil, err
	}
	intent.ClaimID, intent.Incarnation = receipt.ClaimID, receipt.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectVMToFloatingIP(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.vm.floating_ip.create", map[string]any{"id": intent.ID}, "")
	return vmFloatingIPTrafficPolicyResult(intent, source, destination, sourceNetwork, destinationNetwork), nil
}

// VMToFloatingIPTrafficPolicyList returns desired intent, independent of the destination's current VM owner.
func (op *Operation) VMToFloatingIPTrafficPolicyList(
	ctx context.Context,
) ([]*results.VMToFloatingIPTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListVMToFloatingIP(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list floating-IP traffic policies", err)
	}
	items := make([]*results.VMToFloatingIPTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.vmFloatingIPTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// VMToFloatingIPTrafficPolicyInspect resolves a reservation-destination policy in its own family.
func (op *Operation) VMToFloatingIPTrafficPolicyInspect(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyInput,
) (*results.VMToFloatingIPTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one floating-IP traffic policy")
	}
	return op.vmFloatingIPTrafficPolicyResult(ctx, intents[0])
}

// VMToFloatingIPTrafficPolicyRemove revokes an exact root incarnation before deleting its matching local receipt.
func (op *Operation) VMToFloatingIPTrafficPolicyRemove(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		request := vmFloatingIPTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(
				errs.CodePolicyRemoveFailed,
				"local floating-IP policy identity is not canonical",
				errs.WithEntity(intent.ID),
			)
		}
		if _, err := op.trafficPolicyClient.RemoveVMFloatingIPTrafficPolicy(ctx, request); err != nil {
			return err
		}
		if err := op.Repos.TrafficPolicy.DeleteOwnedVMToFloatingIP(
			ctx,
			intent.ID,
			intent.ClaimID,
			intent.Incarnation,
		); err != nil {
			return trafficPolicyProjectionError(intent.ID, true, err)
		}
		op.AuditLog.LogOperation("policy.traffic.vm.floating_ip.remove", map[string]any{"id": intent.ID}, "")
	}
	return nil
}
func vmFloatingIPTrafficPolicyRequest(
	intent *model.VMToFloatingIPTrafficAllowIntent,
) networkservice.VMFloatingIPTrafficPolicyRequest {
	return networkservice.VMFloatingIPTrafficPolicyRequest{
		ClaimID:                 intent.ClaimID,
		Incarnation:             intent.Incarnation,
		SourceVMID:              intent.SourceVMID,
		DestinationFloatingIPID: intent.DestinationFloatingIPID,
		Protocol:                intent.Protocol,
		PortStart:               uint16(intent.DestinationPortStart),
		PortEnd:                 uint16(intent.DestinationPortEnd),
	}
}
func (op *Operation) vmFloatingIPTrafficPolicyResult(
	ctx context.Context,
	intent *model.VMToFloatingIPTrafficAllowIntent,
) (*results.VMToFloatingIPTrafficPolicy, error) {
	source, err := op.trafficPolicyVM(ctx, intent.SourceVMID)
	if err != nil {
		return nil, err
	}
	sourceNetwork, err := op.trafficPolicyNetwork(ctx, string(source.NetworkID))
	if err != nil {
		return nil, err
	}
	destination, destinationNetwork, err := op.trafficPolicyFloatingIP(ctx, intent.DestinationFloatingIPID)
	if err != nil {
		return nil, err
	}
	return vmFloatingIPTrafficPolicyResult(intent, source, destination, sourceNetwork, destinationNetwork), nil
}

// NetworkToFloatingIPTrafficPolicyCreate installs root-authorized intent before recording its receipt locally.
func (op *Operation) NetworkToFloatingIPTrafficPolicyCreate(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
) (*results.NetworkToFloatingIPTrafficPolicy, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	destination, err := op.FloatingIPGet(ctx, inputs.FloatingIPInput{
		Network:    input.DestinationNetwork,
		Identifier: input.DestinationFloatingIP,
	})
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
	receipt, err := op.trafficPolicyClient.ApplyNetworkFloatingIPTrafficPolicy(
		ctx,
		networkFloatingIPTrafficPolicyRequest(intent),
	)
	if err != nil {
		return nil, err
	}
	intent.ClaimID, intent.Incarnation = receipt.ClaimID, receipt.Incarnation
	if err := op.Repos.TrafficPolicy.ProjectNetworkToFloatingIP(ctx, intent); err != nil {
		return nil, trafficPolicyProjectionError(intent.ID, false, err)
	}
	op.AuditLog.LogOperation("policy.traffic.network.floating_ip.create", map[string]any{"id": intent.ID}, "")
	return networkFloatingIPTrafficPolicyResult(intent, source, destination, destinationNetwork), nil
}

// NetworkToFloatingIPTrafficPolicyList returns desired intent, independent of the destination's current VM owner.
func (op *Operation) NetworkToFloatingIPTrafficPolicyList(
	ctx context.Context,
) ([]*results.NetworkToFloatingIPTrafficPolicy, error) {
	intents, err := op.Repos.TrafficPolicy.ListNetworkToFloatingIP(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list floating-IP traffic policies", err)
	}
	items := make([]*results.NetworkToFloatingIPTrafficPolicy, 0, len(intents))
	for _, intent := range intents {
		item, err := op.networkFloatingIPTrafficPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// NetworkToFloatingIPTrafficPolicyInspect resolves a reservation-destination policy in its own family.
func (op *Operation) NetworkToFloatingIPTrafficPolicyInspect(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyInput,
) (*results.NetworkToFloatingIPTrafficPolicy, error) {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return nil, err
	}
	if len(intents) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one floating-IP traffic policy")
	}
	return op.networkFloatingIPTrafficPolicyResult(ctx, intents[0])
}

// NetworkToFloatingIPTrafficPolicyRemove revokes an exact root incarnation before deleting its matching local receipt.
func (op *Operation) NetworkToFloatingIPTrafficPolicyRemove(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyInput,
) error {
	intents, err := input.Resolve(ctx, op.Repos.TrafficPolicy)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		request := networkFloatingIPTrafficPolicyRequest(intent)
		if request.PolicyID() != intent.ID {
			return errs.New(
				errs.CodePolicyRemoveFailed,
				"local floating-IP policy identity is not canonical",
				errs.WithEntity(intent.ID),
			)
		}
		if _, err := op.trafficPolicyClient.RemoveNetworkFloatingIPTrafficPolicy(ctx, request); err != nil {
			return err
		}
		if err := op.Repos.TrafficPolicy.DeleteOwnedNetworkToFloatingIP(
			ctx,
			intent.ID,
			intent.ClaimID,
			intent.Incarnation,
		); err != nil {
			return trafficPolicyProjectionError(intent.ID, true, err)
		}
		op.AuditLog.LogOperation("policy.traffic.network.floating_ip.remove", map[string]any{"id": intent.ID}, "")
	}
	return nil
}
func networkFloatingIPTrafficPolicyRequest(
	intent *model.NetworkToFloatingIPTrafficAllowIntent,
) networkservice.NetworkFloatingIPTrafficPolicyRequest {
	return networkservice.NetworkFloatingIPTrafficPolicyRequest{
		ClaimID:                 intent.ClaimID,
		Incarnation:             intent.Incarnation,
		SourceNetworkID:         intent.SourceNetworkID,
		DestinationFloatingIPID: intent.DestinationFloatingIPID,
		Protocol:                intent.Protocol,
		PortStart:               uint16(intent.DestinationPortStart),
		PortEnd:                 uint16(intent.DestinationPortEnd),
	}
}
func (op *Operation) networkFloatingIPTrafficPolicyResult(
	ctx context.Context,
	intent *model.NetworkToFloatingIPTrafficAllowIntent,
) (*results.NetworkToFloatingIPTrafficPolicy, error) {
	source, err := op.trafficPolicyNetwork(ctx, intent.SourceNetworkID)
	if err != nil {
		return nil, err
	}
	destination, destinationNetwork, err := op.trafficPolicyFloatingIP(ctx, intent.DestinationFloatingIPID)
	if err != nil {
		return nil, err
	}
	return networkFloatingIPTrafficPolicyResult(intent, source, destination, destinationNetwork), nil
}

// trafficPolicyFloatingIP resolves display facts from root inventory, never from a policy's caller-controlled address.
func (op *Operation) trafficPolicyFloatingIP(
	ctx context.Context,
	id string,
) (*model.FloatingIPItem, *model.NetworkItem, error) {
	networks, err := op.Repos.Network.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, network := range networks {
		if network.DeletedAt != nil {
			continue
		}
		items, err := op.Services.Network.ListFloatingIPs(ctx, network.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range items {
			if item.ID == id {
				return item, network, nil
			}
		}
	}
	return nil, nil, errs.NotFound(
		errs.CodeNetworkNotFound,
		"floating-IP policy destination no longer exists",
		errs.WithEntity(id),
	)
}

func vmFloatingIPTrafficPolicyResult(
	intent *model.VMToFloatingIPTrafficAllowIntent,
	source *model.VMItem,
	destination *model.FloatingIPItem,
	sourceNetwork,
	destinationNetwork *model.NetworkItem,
) *results.VMToFloatingIPTrafficPolicy {
	return &results.VMToFloatingIPTrafficPolicy{
		VMToFloatingIPTrafficAllowIntent: *intent,
		SourceVMName:                     source.Name,
		SourceNetworkID:                  sourceNetwork.ID,
		SourceNetworkName:                sourceNetwork.Name,
		DestinationFloatingIPName:        destination.Name,
		DestinationIPv4:                  destination.IPv4,
		DestinationNetworkID:             destinationNetwork.ID,
		DestinationNetworkName:           destinationNetwork.Name,
		NetworkRelation:                  trafficNetworkRelation(sourceNetwork.ID, destinationNetwork.ID),
	}
}

func networkFloatingIPTrafficPolicyResult(
	intent *model.NetworkToFloatingIPTrafficAllowIntent,
	source *model.NetworkItem,
	destination *model.FloatingIPItem,
	destinationNetwork *model.NetworkItem,
) *results.NetworkToFloatingIPTrafficPolicy {
	return &results.NetworkToFloatingIPTrafficPolicy{
		NetworkToFloatingIPTrafficAllowIntent: *intent,
		SourceNetworkName:                     source.Name,
		DestinationFloatingIPName:             destination.Name,
		DestinationIPv4:                       destination.IPv4,
		DestinationNetworkID:                  destinationNetwork.ID,
		DestinationNetworkName:                destinationNetwork.Name,
		NetworkRelation:                       trafficNetworkRelation(source.ID, destinationNetwork.ID),
	}
}
