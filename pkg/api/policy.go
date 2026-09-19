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

// PolicyAPI exposes separate traffic allow-intent and directional execution policy families.
type PolicyAPI interface {
	NetworkToVMTrafficPolicyCreate(
		ctx context.Context,
		input inputs.NetworkToVMTrafficPolicyCreateInput,
	) (*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyList(ctx context.Context) ([]*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyInspect(
		ctx context.Context,
		input inputs.NetworkToVMTrafficPolicyInput,
	) (*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyRemove(ctx context.Context, input inputs.NetworkToVMTrafficPolicyInput) error
	VMToVMTrafficPolicyCreate(
		ctx context.Context,
		input inputs.VMToVMTrafficPolicyCreateInput,
	) (*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyList(ctx context.Context) ([]*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyInspect(
		ctx context.Context,
		input inputs.VMToVMTrafficPolicyInput,
	) (*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyRemove(ctx context.Context, input inputs.VMToVMTrafficPolicyInput) error
	ExecPolicyCreate(ctx context.Context, input inputs.VMToVMExecPolicyCreateInput) (*results.VMToVMExecPolicy, error)
	ExecPolicyList(ctx context.Context) ([]*results.VMToVMExecPolicy, error)
	ExecPolicyRemove(ctx context.Context, input inputs.VMToVMExecPolicyInput) error
}

// ExecPolicyCreate persists one directional VM-to-VM non-root execution grant.
func (op *Operation) ExecPolicyCreate(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
) (*results.VMToVMExecPolicy, error) {
	source, destination, err := input.ResolveVMs(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	intent := &model.VMToVMExecAllowIntent{
		SourceVMID: source.ID, DestinationVMID: destination.ID, TargetUser: input.TargetUser,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToVMExecPolicyDigest(*intent)
	if err := op.Services.ExecPolicy.Create(ctx, intent); err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("policy.exec.create", map[string]any{"id": intent.ID}, "")
	return execPolicyResult(intent, source, destination), nil
}

// ExecPolicyList returns directional execution grants with current VM names.
func (op *Operation) ExecPolicyList(ctx context.Context) ([]*results.VMToVMExecPolicy, error) {
	intents, err := op.Services.ExecPolicy.AllowIntents(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*results.VMToVMExecPolicy, 0, len(intents))
	for _, intent := range intents {
		source, err := op.execPolicyVM(ctx, intent.SourceVMID)
		if err != nil {
			return nil, err
		}
		destination, err := op.execPolicyVM(ctx, intent.DestinationVMID)
		if err != nil {
			return nil, err
		}
		items = append(items, execPolicyResult(intent, source, destination))
	}
	return items, nil
}

// ExecPolicyRemove removes only directional execution grants.
func (op *Operation) ExecPolicyRemove(ctx context.Context, input inputs.VMToVMExecPolicyInput) error {
	intents, err := input.Resolve(ctx, op.Repos.ExecPolicy)
	if err != nil {
		return err
	}
	ids := make([]string, len(intents))
	for index, intent := range intents {
		ids[index] = intent.ID
	}
	if err := op.Services.ExecPolicy.Remove(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		op.AuditLog.LogOperation("policy.exec.remove", map[string]any{"id": id}, "")
	}
	return nil
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

func (op *Operation) execPolicyVM(ctx context.Context, id string) (*model.VMItem, error) {
	item, err := op.Repos.VM.Get(ctx, id)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to resolve execution-policy VM", err)
	}
	if item == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "execution-policy VM no longer exists", errs.WithEntity(id))
	}
	return item, nil
}

func execPolicyResult(
	intent *model.VMToVMExecAllowIntent,
	source *model.VMItem,
	destination *model.VMItem,
) *results.VMToVMExecPolicy {
	return &results.VMToVMExecPolicy{
		ID: intent.ID, SourceVMID: source.ID, SourceVMName: source.Name,
		DestinationVMID: destination.ID, DestinationVMName: destination.Name,
		TargetUser: intent.TargetUser, CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
	}
}
