package env

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// KeyState is the persisted state for a key step.
type KeyState struct {
	KeyID string `yaml:"key_id"`
}

// KeyStep implements workflow.Step for creating SSH key pairs.
type KeyStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.KeyCreateInput
	op       *api.Operation
	saved    *KeyState
	meta     model.ResourceMeta
}

func (s *KeyStep) Type() string { return s.stepType }

func (s *KeyStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *KeyStep) Dependencies() []string { return s.deps }

func (s *KeyStep) SpecHash() string { return s.specHash }

func (s *KeyStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Recover WasCreated from saved meta.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.Repos.Key.GetByName(ctx, s.input.Name)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check key %q: %v", s.input.Name, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &KeyState{
			KeyID: existing.ID,
		}
		s.meta = model.ResourceMeta{
			WasCreated: wasCreated,
			SpecHash:   s.specHash,
		}
		state.Set(s.Name(), s.saved)
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after skip: %w", err)
		}
		return nil
	}

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "creating key"})
	key, err := s.op.KeyCreate(ctx, s.input)
	if err != nil {
		return err
	}

	s.saved = &KeyState{
		KeyID: key.ID,
	}
	s.meta = model.ResourceMeta{
		WasCreated: true,
		SpecHash:   s.specHash,
	}
	state.Set(s.Name(), s.saved)
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state: %w", err)
	}
	return nil
}

func (s *KeyStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[KeyState](saved.Spec)
		s.meta = saved.Meta
	}

	if s.saved == nil || !s.meta.WasCreated {
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after destroy skip: %w", err)
		}
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

	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *KeyStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

func newKeyStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op *api.Operation,
) (workflow.Step, error) {
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
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newKeyStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op *api.Operation,
) (workflow.Step, error) {
	ks := StateFromMap[KeyState](saved.Spec)
	return &KeyStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		input: inputs.KeyCreateInput{
			Name: name,
		},
		op:    op,
		saved: ks,
		meta:  saved.Meta,
	}, nil
}
