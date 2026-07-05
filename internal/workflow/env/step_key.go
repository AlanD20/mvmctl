package env

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	removes  []string
	specHash string
	input    inputs.KeyCreateInput
	op       api.KeyAPI
	saved    *KeyState
	meta     model.ResourceMeta
}

func (s *KeyStep) Type() string { return s.stepType }

func (s *KeyStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *KeyStep) Dependencies() []string { return s.deps }

func (s *KeyStep) SpecHash() string  { return s.specHash }
func (s *KeyStep) Removes() []string { return s.removes }

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
	existing, err := s.op.KeyGet(ctx, inputs.KeyInput{Identifiers: []string{s.input.Name}})
	if err != nil && !errs.IsNotFound(err) {
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
				err := r.ToError()
				if errs.IsNotFound(err) {
					slog.Debug("key already removed, skipping destroy", "key", s.saved.KeyID)
					if err := write(ctx, s.StateData()); err != nil {
						return fmt.Errorf("persist step state after destroy skip: %w", err)
					}
					return nil
				}
				return err
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

// NewKeyStep creates a KeyStep with the given API interface for testing.
// Only for use in tests.
func NewKeyStep(op api.KeyAPI, name string, input inputs.KeyCreateInput) *KeyStep {
	return &KeyStep{
		op:       op,
		name:     name,
		stepType: "key",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newKeyStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.KeyCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	// Resource name: spec "name" overrides step name.
	if input.Name == "" {
		input.Name = name
	}

	return &KeyStep{
		stepType: stepType,
		name:     name,
		deps:     spec.GetStringList("depends_on"),
		removes:  spec.GetStringList("removes"),
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
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

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
