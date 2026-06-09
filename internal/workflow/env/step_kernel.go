package env

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// KernelState is the persisted state for a kernel step.
type KernelState struct {
	KernelID   string `yaml:"kernel_id"`
	WasCreated bool   `yaml:"was_created"`
}

// KernelStep implements workflow.Step for pulling kernels.
// Destroy is a no-op because kernels persist in the database.
type KernelStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.KernelPullInput
	op       *api.Operation
	saved    *KernelState
}

func (s *KernelStep) Type() string { return s.stepType }

func (s *KernelStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *KernelStep) Dependencies() []string { return s.deps }

func (s *KernelStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	var prev *KernelState
	if saved != nil {
		prev = StateFromMap[KernelState](saved)
	}
	existing, err := s.op.Repos.Kernel.GetByType(ctx, s.input.KernelType)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check kernel type %q: %v", s.input.KernelType, err),
			err,
		)
	}
	if existing != nil {
		wasCreated := prev != nil && prev.WasCreated
		s.saved = &KernelState{
			KernelID:   existing.ID,
			WasCreated: wasCreated,
		}
		state.Set(s.Name(), s.saved)
		return nil
	}

	krnl, err := s.op.KernelPull(ctx, s.input, nil)
	if err != nil {
		return err
	}

	s.saved = &KernelState{
		KernelID:   krnl.ID,
		WasCreated: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *KernelStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	// Kernels persist in the database — no teardown needed.
	return nil
}

func (s *KernelStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newKernelStepFromSpec(stepType string, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.KernelPullInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}

	// Canonical spec key is "type". The "kernel_type" key is also accepted via
	// the YAML struct tag — selecting both with different values is undefined.
	if kernelType := spec.GetString("type"); kernelType != "" {
		input.KernelType = kernelType
	}

	return &KernelStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newKernelStepFromState(stepType string, name string, saved model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	ks := StateFromMap[KernelState](saved)
	return &KernelStep{
		stepType: stepType,
		name:     name,
		op:       op,
		saved:    ks,
	}, nil
}
