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

// BinaryState is the persisted state for a binary step.
type BinaryState struct {
	BinaryID   string `yaml:"binary_id"`
	WasCreated bool   `yaml:"was_created"`
}

// BinaryStep implements workflow.Step for pulling binaries (firecracker, jailer).
// Destroy is a no-op because binaries persist in the database.
type BinaryStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.BinaryPullInput
	op       *api.Operation
	saved    *BinaryState
}

func (s *BinaryStep) Type() string { return s.stepType }

func (s *BinaryStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *BinaryStep) Dependencies() []string { return s.deps }

func (s *BinaryStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	var prev *BinaryState
	if saved != nil {
		prev = StateFromMap[BinaryState](saved)
	}
	existing, err := s.op.Repos.Binary.GetByNameAndVersion(ctx, s.input.Name, s.input.Version)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check binary %q: %v", s.input.Name, err),
			err,
		)
	}
	if existing != nil {
		wasCreated := prev != nil && prev.WasCreated
		s.saved = &BinaryState{
			BinaryID:   existing.ID,
			WasCreated: wasCreated,
		}
		state.Set(s.Name(), s.saved)
		return nil
	}

	binaries, err := s.op.BinaryPull(ctx, s.input, nil)
	if err != nil {
		return err
	}
	if len(binaries) == 0 {
		return errs.New(errs.CodeInternal, fmt.Sprintf("binary pull returned no items for %q", s.input.Name))
	}

	s.saved = &BinaryState{
		BinaryID:   binaries[0].ID,
		WasCreated: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *BinaryStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	// Binaries persist in the database — no teardown needed.
	return nil
}

func (s *BinaryStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newBinaryStepFromSpec(stepType string, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.BinaryPullInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}

	return &BinaryStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newBinaryStepFromState(stepType string, name string, saved model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	bs := StateFromMap[BinaryState](saved)
	return &BinaryStep{
		stepType: stepType,
		name:     name,
		input: inputs.BinaryPullInput{
			Name: name,
		},
		op:    op,
		saved: bs,
	}, nil
}
