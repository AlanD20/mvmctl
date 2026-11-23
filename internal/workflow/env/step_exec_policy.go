package env

import (
	"context"
	"errors"
	"maps"
	"strings"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// ExecPolicyState retains the directional grant and its creation claim before any effect.
type ExecPolicyState struct {
	SourceLabels      model.VMLabelSelector `yaml:"source_labels,omitempty"`
	DestinationLabels model.VMLabelSelector `yaml:"destination_labels,omitempty"`

	PolicyID        string `yaml:"policy_id"`
	ClaimID         string `yaml:"claim_id"`
	Incarnation     string `yaml:"incarnation"`
	SourceVMID      string `yaml:"source_vm_id"`
	DestinationVMID string `yaml:"destination_vm_id"`
	TargetUser      string `yaml:"target_user"`
}

type execPolicyDeclaration struct {
	SourceLabels      model.VMLabelSelector `yaml:"source_labels,omitempty"`
	DestinationLabels model.VMLabelSelector `yaml:"destination_labels,omitempty"`

	SourceVM      string `yaml:"source_vm"`
	DestinationVM string `yaml:"destination_vm"`
	TargetUser    string `yaml:"target_user"`
	Existing      string `yaml:"existing"`
}

type execPolicyStepAPI interface {
	api.PolicyAPI
	api.EnvironmentPolicyAPI
	api.VMAPI
}

// ExecPolicyStep grants directional non-root execution without enabling VM exec itself.
type ExecPolicyStep struct {
	resourceStepRecord
	input execPolicyDeclaration
	op    execPolicyStepAPI
}

func (s *ExecPolicyStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if err := s.restore(saved); err != nil {
		return err
	}
	output := StateFromMap[ExecPolicyState](s.record.Output)
	if output == nil {
		source, err := s.resolveExactVM(ctx, state, s.input.SourceVM)
		if err != nil {
			return err
		}
		destination, err := s.resolveExactVM(ctx, state, s.input.DestinationVM)
		if err != nil {
			return err
		}
		output = &ExecPolicyState{
			SourceVMID: source, DestinationVMID: destination, TargetUser: s.input.TargetUser,
			SourceLabels: s.input.SourceLabels, DestinationLabels: s.input.DestinationLabels,
		}
		if s.input.Existing == "" {
			output.ClaimID = crypto.UUIDV4()
		}
		s.record.Output = StructToMap(output)
		// CRITICAL: Persist the claim before effects so an interrupted response cannot orphan ownership.
		if err := s.persist(ctx, write); err != nil {
			return err
		}
	}
	if s.input.Existing != "" {
		items, err := s.op.ExecPolicyList(ctx)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.ID != s.input.Existing {
				continue
			}
			if item.SourceVMID != output.SourceVMID || item.DestinationVMID != output.DestinationVMID ||
				item.TargetUser != output.TargetUser || !maps.Equal(item.SourceLabels, output.SourceLabels) ||
				!maps.Equal(item.DestinationLabels, output.DestinationLabels) {
				return resourceIdentityMismatch(s.Name())
			}
			output.PolicyID = item.ID
			s.record.Output = StructToMap(output)
			s.record.Meta.WasCreated = false
			state.Set(s.Name(), output)
			return s.persist(ctx, write)
		}
		return resourceIdentityMismatch(s.Name())
	}
	if output.ClaimID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	receipt, applyErr := s.op.ExecPolicyEnsure(
		ctx,
		output.policyInput(),
		inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation},
	)
	if receipt != nil {
		output.PolicyID, output.Incarnation = receipt.PolicyID, receipt.Incarnation
		s.record.Meta.WasCreated = receipt.WasCreated
		s.record.Output = StructToMap(output)
	}
	if applyErr != nil {
		return errors.Join(applyErr, s.persist(ctx, write))
	}
	if receipt == nil || receipt.PolicyID == "" || receipt.Incarnation == "" {
		return errs.New(errs.CodeInternal, "execution policy ensure returned no incarnation receipt")
	}
	state.Set(s.Name(), output)
	return s.persist(ctx, write)
}

func (s *ExecPolicyStep) Destroy(ctx context.Context, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if saved.Output != nil {
		s.record = saved
	}
	output := StateFromMap[ExecPolicyState](s.record.Output)
	if output == nil || output.ClaimID == "" || (output.Incarnation != "" && !s.record.Meta.WasCreated) {
		return s.persist(ctx, write)
	}
	// A pending claim has no receipt yet; conditional removal still cannot revoke a foreign grant.
	if err := s.op.ExecPolicyRemoveOwned(ctx, output.policyInput(), inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation}); err != nil {
		return err
	}
	return s.persist(ctx, write)
}

func (output *ExecPolicyState) policyInput() inputs.VMToVMExecPolicyCreateInput {
	return inputs.VMToVMExecPolicyCreateInput{
		SourceVM: output.SourceVMID, DestinationVM: output.DestinationVMID, TargetUser: output.TargetUser,
		SourceLabels: output.SourceLabels, DestinationLabels: output.DestinationLabels,
	}
}

func (s *ExecPolicyStep) resolveExactVM(
	ctx context.Context,
	state *workflow.SharedState,
	value string,
) (string, error) {
	if value == "" {
		return "", nil
	}
	resolved, err := resourceStepReference(state, value, "vm")
	if err != nil {
		return "", err
	}
	vm, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{resolved}})
	if err != nil {
		return "", err
	}
	if vm == nil || (strings.HasPrefix(value, "@") && vm.ID != resolved) {
		return "", resourceIdentityMismatch(s.Name())
	}
	return vm.ID, nil
}

func newExecPolicyStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	record, data, err := newResourceStepRecord(stepType, name, spec)
	if err != nil {
		return nil, err
	}
	var input execPolicyDeclaration
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	validation := inputs.VMToVMExecPolicyCreateInput{
		SourceVM: input.SourceVM, DestinationVM: input.DestinationVM, TargetUser: input.TargetUser,
		SourceLabels: input.SourceLabels, DestinationLabels: input.DestinationLabels,
	}
	if err := validation.Validate(); err != nil {
		return nil, err
	}
	if input.Existing != "" && len(input.Existing) != 64 {
		return nil, errs.New(errs.CodeValidationFailed, "existing execution policy requires its exact 64-character ID")
	}
	if input.Existing != "" {
		selector := inputs.VMToVMExecPolicyInput{Identifiers: []string{input.Existing}}
		if err := selector.Validate(); err != nil {
			return nil, err
		}
	}
	return &ExecPolicyStep{resourceStepRecord: record, input: input, op: op}, nil
}

func newExecPolicyStepFromState(
	stepType, name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	step, err := newExecPolicyStepFromSpec(stepType, name, saved.Spec, op)
	if err != nil {
		return nil, err
	}
	s := step.(*ExecPolicyStep)
	s.record, s.deps = saved, deps
	return s, nil
}
