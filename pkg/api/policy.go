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
	LabelTrafficPolicyAPI
	VMToFloatingIPTrafficPolicyCreate(
		context.Context,
		inputs.VMToFloatingIPTrafficPolicyCreateInput,
	) (*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyList(context.Context) ([]*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyInspect(
		context.Context,
		inputs.VMToFloatingIPTrafficPolicyInput,
	) (*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyRemove(context.Context, inputs.VMToFloatingIPTrafficPolicyInput) error
	NetworkToFloatingIPTrafficPolicyCreate(
		context.Context,
		inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	) (*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyList(context.Context) ([]*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyInspect(
		context.Context,
		inputs.NetworkToFloatingIPTrafficPolicyInput,
	) (*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyRemove(context.Context, inputs.NetworkToFloatingIPTrafficPolicyInput) error

	VMToHostTrafficPolicyCreate(
		ctx context.Context,
		input inputs.VMToHostTrafficPolicyCreateInput,
	) (*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyList(ctx context.Context) ([]*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyInspect(
		ctx context.Context,
		input inputs.VMToHostTrafficPolicyInput,
	) (*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyRemove(ctx context.Context, input inputs.VMToHostTrafficPolicyInput) error
	NetworkToHostTrafficPolicyCreate(
		ctx context.Context,
		input inputs.NetworkToHostTrafficPolicyCreateInput,
	) (*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyList(ctx context.Context) ([]*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyInspect(
		ctx context.Context,
		input inputs.NetworkToHostTrafficPolicyInput,
	) (*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyRemove(ctx context.Context, input inputs.NetworkToHostTrafficPolicyInput) error
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
