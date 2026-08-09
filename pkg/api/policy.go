package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// PolicyAPI defines the public interface for routed service-access policies.
type PolicyAPI interface {
	PolicyCreate(ctx context.Context, input inputs.PolicyCreateInput) (*results.Policy, error)
	PolicyList(ctx context.Context) ([]*results.Policy, error)
	PolicyInspect(ctx context.Context, input inputs.PolicyInput) (*results.Policy, error)
	PolicyRemove(ctx context.Context, input inputs.PolicyInput) error
	PolicySync(ctx context.Context) (*results.PolicySync, error)
}

// PolicyCreate resolves resource identities, persists typed intent, and reconciles firewall state.
func (op *Operation) PolicyCreate(ctx context.Context, input inputs.PolicyCreateInput) (*results.Policy, error) {
	source, err := input.ResolveSourceNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	destination, err := input.ResolveDestinationVM(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	if source.ID == destination.NetworkID {
		return nil, errs.New(errs.CodePolicySameNetwork,
			"source network and destination VM network must differ; same-network enforcement is not available")
	}
	protocol, portStart, portEnd, err := input.ResolvedPort()
	if err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339)
	policy := &model.ServiceAccessPolicy{
		ID: crypto.ContentHash(source.ID, destination.ID, string(protocol),
			fmt.Sprintf("%d", portStart), fmt.Sprintf("%d", portEnd), now),
		SourceNetworkID:      source.ID,
		DestinationVMID:      destination.ID,
		Protocol:             protocol,
		DestinationPortStart: portStart,
		DestinationPortEnd:   portEnd,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := op.Services.Network.CreateServiceAccessPolicy(ctx, policy); err != nil {
		return nil, err
	}
	if reconcileErr := op.reconcileServiceAccessPolicies(ctx); reconcileErr != nil {
		rollbackErr := op.Services.Network.RemoveServiceAccessPolicies(ctx, []string{policy.ID})
		recoveryErr := op.reconcileServiceAccessPolicies(ctx)
		causes := []error{reconcileErr}
		details := map[string]any{"reconciliation_error": reconcileErr.Error()}
		if rollbackErr != nil {
			causes = append(causes, rollbackErr)
			details["rollback_error"] = rollbackErr.Error()
		}
		if recoveryErr != nil {
			causes = append(causes, recoveryErr)
			details["recovery_error"] = recoveryErr.Error()
		}
		return nil, errs.WrapMsg(errs.CodePolicyCreateFailed, reconcileErr.Error(), errors.Join(causes...),
			errs.WithDetails(details))
	}
	op.AuditLog.LogOperation("policy.create", map[string]any{"id": policy.ID}, "")
	return op.policyResult(ctx, policy)
}

// PolicyList returns all policy intent with current resource names.
func (op *Operation) PolicyList(ctx context.Context) ([]*results.Policy, error) {
	policies, err := op.Repos.Policy.List(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list service-access policies", err)
	}
	items := make([]*results.Policy, 0, len(policies))
	for _, policy := range policies {
		item, err := op.policyResult(ctx, policy)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// PolicyInspect returns one policy resolved by exact ID or unambiguous prefix.
func (op *Operation) PolicyInspect(ctx context.Context, input inputs.PolicyInput) (*results.Policy, error) {
	policies, err := input.Resolve(ctx, op.Repos.Policy)
	if err != nil {
		return nil, err
	}
	if len(policies) != 1 {
		return nil, errs.New(errs.CodePolicyAmbiguous, "expected exactly one service-access policy")
	}
	return op.policyResult(ctx, policies[0])
}

// PolicyRemove deletes policy intent and reconciles firewall state immediately.
func (op *Operation) PolicyRemove(ctx context.Context, input inputs.PolicyInput) error {
	policies, err := input.Resolve(ctx, op.Repos.Policy)
	if err != nil {
		return err
	}
	policyIDs := make([]string, len(policies))
	for i, policy := range policies {
		policyIDs[i] = policy.ID
	}
	if err := op.Services.Network.RemoveServiceAccessPolicies(ctx, policyIDs); err != nil {
		return err
	}
	for _, policy := range policies {
		op.AuditLog.LogOperation("policy.remove", map[string]any{"id": policy.ID}, "")
	}
	return op.reconcileServiceAccessPolicies(ctx)
}

// PolicySync recompiles all intent from current resource identities and reconciles firewall state.
func (op *Operation) PolicySync(ctx context.Context) (*results.PolicySync, error) {
	if err := op.reconcileServiceAccessPolicies(ctx); err != nil {
		return nil, err
	}
	policies, err := op.Repos.Policy.List(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodePolicySyncFailed, "failed to count service-access policies", err)
	}
	return &results.PolicySync{Policies: len(policies)}, nil
}

func (op *Operation) reconcileServiceAccessPolicies(ctx context.Context) error {
	policies, networks, err := op.resolveServiceAccessPolicyState(ctx)
	if err != nil {
		return err
	}
	return op.Services.Network.ReconcileServiceAccessPolicies(ctx, policies, networks)
}

func (op *Operation) resolveServiceAccessPolicyState(
	ctx context.Context,
) ([]*model.ResolvedServiceAccessPolicy, []*model.NetworkItem, error) {
	policies, err := op.Repos.Policy.List(ctx)
	if err != nil {
		return nil, nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to list service-access policies", err)
	}
	networks, err := op.Repos.Network.ListAll(ctx)
	if err != nil {
		return nil, nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to list policy networks", err)
	}
	activeNetworks := make([]*model.NetworkItem, 0, len(networks))
	networkByID := make(map[string]*model.NetworkItem, len(networks))
	for _, network := range networks {
		if network.DeletedAt != nil {
			continue
		}
		activeNetworks = append(activeNetworks, network)
		networkByID[network.ID] = network
	}
	resolved := make([]*model.ResolvedServiceAccessPolicy, 0, len(policies))
	for _, policy := range policies {
		source := networkByID[policy.SourceNetworkID]
		if source == nil {
			return nil, nil, errs.NotFound(errs.CodeNetworkNotFound,
				"policy source network no longer exists", errs.WithEntity(policy.SourceNetworkID))
		}
		destination, err := op.Repos.VM.Get(ctx, policy.DestinationVMID)
		if err != nil {
			return nil, nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve policy destination VM", err)
		}
		if destination == nil {
			return nil, nil, errs.NotFound(errs.CodeVMNotFound,
				"policy destination VM no longer exists", errs.WithEntity(policy.DestinationVMID))
		}
		destinationNetwork := networkByID[destination.NetworkID]
		if destinationNetwork == nil {
			return nil, nil, errs.NotFound(errs.CodeNetworkNotFound,
				"policy destination network no longer exists", errs.WithEntity(destination.NetworkID))
		}
		if source.ID == destinationNetwork.ID {
			return nil, nil, errs.New(errs.CodePolicySameNetwork,
				"persisted policy source and current destination network must differ",
				errs.WithEntity(policy.ID))
		}
		resolved = append(resolved, &model.ResolvedServiceAccessPolicy{
			ServiceAccessPolicy:      *policy,
			SourceNetworkName:        source.Name,
			SourceNetworkBridge:      source.Bridge,
			DestinationVMName:        destination.Name,
			DestinationIPv4:          destination.IPv4,
			DestinationNetworkID:     destinationNetwork.ID,
			DestinationNetworkName:   destinationNetwork.Name,
			DestinationNetworkBridge: destinationNetwork.Bridge,
		})
	}
	return resolved, activeNetworks, nil
}

func (op *Operation) policyResult(ctx context.Context, policy *model.ServiceAccessPolicy) (*results.Policy, error) {
	source, err := op.Repos.Network.Get(ctx, policy.SourceNetworkID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve policy source network", err)
	}
	if source == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "policy source network no longer exists")
	}
	destination, err := op.Repos.VM.Get(ctx, policy.DestinationVMID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve policy destination VM", err)
	}
	if destination == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "policy destination VM no longer exists")
	}
	destinationNetwork, err := op.Repos.Network.Get(ctx, destination.NetworkID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve policy destination network", err)
	}
	if destinationNetwork == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "policy destination network no longer exists")
	}
	if source.ID == destinationNetwork.ID {
		return nil, errs.New(errs.CodePolicySameNetwork,
			"persisted policy source and current destination network must differ", errs.WithEntity(policy.ID))
	}
	return &results.Policy{
		ID:                     policy.ID,
		SourceNetworkID:        source.ID,
		SourceNetworkName:      source.Name,
		DestinationVMID:        destination.ID,
		DestinationVMName:      destination.Name,
		DestinationNetworkID:   destinationNetwork.ID,
		DestinationNetworkName: destinationNetwork.Name,
		Protocol:               policy.Protocol,
		DestinationPortStart:   policy.DestinationPortStart,
		DestinationPortEnd:     policy.DestinationPortEnd,
		CreatedAt:              policy.CreatedAt,
		UpdatedAt:              policy.UpdatedAt,
	}, nil
}
