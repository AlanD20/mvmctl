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

// KeyState is the persisted state for a key step.
type KeyState struct {
	KeyID      string `yaml:"key_id"`
	WasCreated bool   `yaml:"was_created"`
}

// KeyStep implements workflow.Step for creating SSH key pairs.
type KeyStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.KeyCreateInput
	op       *api.Operation
	saved    *KeyState
}

func (s *KeyStep) Type() string { return s.stepType }

func (s *KeyStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *KeyStep) Dependencies() []string { return s.deps }

func (s *KeyStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	var prev *KeyState
	if saved != nil {
		prev = StateFromMap[KeyState](saved)
	}
	existing, err := s.op.Repos.Key.GetByName(ctx, s.input.Name)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check key %q: %v", s.input.Name, err),
			err,
		)
	}
	if existing != nil {
		wasCreated := prev != nil && prev.WasCreated
		s.saved = &KeyState{
			KeyID:      existing.ID,
			WasCreated: wasCreated,
		}
		state.Set(s.Name(), s.saved)
		return nil
	}

	key, err := s.op.KeyCreate(ctx, s.input)
	if err != nil {
		return err
	}

	s.saved = &KeyState{
		KeyID:      key.ID,
		WasCreated: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *KeyStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved != nil {
		s.saved = StateFromMap[KeyState](saved)
	}

	if s.saved == nil || !s.saved.WasCreated {
		return nil
	}

	result := s.op.KeyRemove(ctx, inputs.KeyInput{
		Identifiers: []string{s.saved.KeyID},
	}, true)
	if result.HasErrors() {
		for _, r := range result.Errors() {
			if r.ToError() != nil {
				return r.ToError()
			}
		}
	}
	return nil
}

func (s *KeyStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newKeyStepFromSpec(stepType string, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.KeyCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	input.Name = name

	return &KeyStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newKeyStepFromState(stepType string, name string, saved model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	ks := StateFromMap[KeyState](saved)
	return &KeyStep{
		stepType: stepType,
		name:     name,
		input: inputs.KeyCreateInput{
			Name: name,
		},
		op:    op,
		saved: ks,
	}, nil
}
