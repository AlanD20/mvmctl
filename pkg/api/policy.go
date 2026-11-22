package api

import (
	"context"
	"strings"

	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/jailer"
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

// ExecPolicyCreate publishes a directional root-owned execution grant.
func (op *Operation) ExecPolicyCreate(
	ctx context.Context, input inputs.VMToVMExecPolicyCreateInput,
) (*results.VMToVMExecPolicy, error) {
	request, err := op.resolveExecPolicy(ctx, input)
	if err != nil {
		return nil, err
	}
	result, err := jailer.ApplyVMExecPolicy(ctx, request)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("policy.exec.create", map[string]any{"id": result.Policy.ID}, "")
	return op.execPolicyResult(ctx, result.Policy)
}

func (op *Operation) resolveExecPolicy(
	ctx context.Context, input inputs.VMToVMExecPolicyCreateInput,
) (jailer.VMExecPolicyApplyRequest, error) {
	if err := input.Validate(); err != nil {
		return jailer.VMExecPolicyApplyRequest{}, err
	}
	source, err := op.resolveVMPolicySelector(
		ctx,
		inputs.VMPolicySelectorInput{VM: input.SourceVM, Labels: input.SourceLabels},
	)
	if err != nil {
		return jailer.VMExecPolicyApplyRequest{}, err
	}
	destination, err := op.resolveVMPolicySelector(ctx, inputs.VMPolicySelectorInput{
		VM: input.DestinationVM, Labels: input.DestinationLabels,
	})
	if err != nil {
		return jailer.VMExecPolicyApplyRequest{}, err
	}
	return jailer.VMExecPolicyApplyRequest{
		CacheRoot: op.CacheDir, Source: source, Destination: destination, TargetUser: input.TargetUser,
	}, nil
}

// ExecPolicyList returns the committed root inventory with current VM names where available.
func (op *Operation) ExecPolicyList(ctx context.Context) ([]*results.VMToVMExecPolicy, error) {
	intents, err := jailer.ListVMExecPolicies(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*results.VMToVMExecPolicy, 0, len(intents))
	for _, intent := range intents {
		result, err := op.execPolicyResult(ctx, intent)
		if err != nil {
			return nil, err
		}
		items = append(items, result)
	}
	return items, nil
}

func (op *Operation) ExecPolicyRemove(ctx context.Context, input inputs.VMToVMExecPolicyInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	policies, err := jailer.ListVMExecPolicies(ctx)
	if err != nil {
		return err
	}
	resolved := make(map[string]firewall.ExecPolicyIntent, len(input.Identifiers))
	for _, identifier := range input.Identifiers {
		var match firewall.ExecPolicyIntent
		for _, policy := range policies {
			if !strings.HasPrefix(policy.ID, identifier) {
				continue
			}
			if match.ID != "" {
				return errs.New(errs.CodePolicyAmbiguous, "ambiguous exec policy", errs.WithEntity(identifier))
			}
			match = policy
		}
		if match.ID == "" {
			return errs.NotFound(errs.CodePolicyNotFound, "exec policy not found", errs.WithEntity(identifier))
		}
		resolved[match.ID] = match
	}
	for _, policy := range policies {
		if _, found := resolved[policy.ID]; !found {
			continue
		}
		err := jailer.RemoveVMExecPolicy(ctx, jailer.VMExecPolicyRemoveRequest{
			PolicyID: policy.ID, Incarnation: policy.Incarnation,
		})
		if err != nil {
			return err
		}
		op.AuditLog.LogOperation("policy.exec.remove", map[string]any{"id": policy.ID}, "")
	}
	return nil
}

func (op *Operation) execPolicyResult(
	ctx context.Context, policy firewall.ExecPolicyIntent,
) (*results.VMToVMExecPolicy, error) {
	name := func(id string) (string, error) {
		if id == "" {
			return "", nil
		}
		item, err := op.Repos.VM.Get(ctx, id)
		if err != nil {
			return "", errs.WrapMsg(errs.CodeDatabaseError, "failed to find exec policy VM name", err)
		}
		if item == nil {
			return "", nil
		}
		return item.Name, nil
	}
	source, err := name(policy.Source.VMID)
	if err != nil {
		return nil, err
	}
	destination, err := name(policy.Destination.VMID)
	if err != nil {
		return nil, err
	}
	return &results.VMToVMExecPolicy{
		ID: policy.ID, TargetUser: policy.TargetUser,
		SourceVMID: policy.Source.VMID, SourceVMName: source, SourceLabels: policy.Source.Labels,
		DestinationVMID: policy.Destination.VMID, DestinationVMName: destination,
		DestinationLabels: policy.Destination.Labels,
	}, nil
}
