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

// BinaryState is the persisted state for a binary step.
type BinaryState struct {
	BinaryID string `yaml:"binary_id"`
}

// BinaryStep implements workflow.Step for pulling binaries (firecracker, jailer).
// Destroy is a no-op because binaries persist in the database.
type BinaryStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.BinaryPullInput
	op       *api.Operation
	saved    *BinaryState
	meta     model.ResourceMeta
}

func (s *BinaryStep) Type() string { return s.stepType }

func (s *BinaryStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *BinaryStep) Dependencies() []string { return s.deps }

func (s *BinaryStep) SpecHash() string { return s.specHash }

func (s *BinaryStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Recover WasCreated from saved meta.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.Repos.Binary.GetByTypeAndVersion(ctx, s.input.Type, s.input.Version)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check binary %q: %v", s.input.Type, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &BinaryState{
			BinaryID: existing.ID,
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

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "pulling binary"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	binaries, err := s.op.BinaryPull(ctx, s.input, stepProgress)
	if err != nil {
		return err
	}
	if len(binaries) == 0 {
		return errs.New(errs.CodeInternal, fmt.Sprintf("binary pull returned no items for %q", s.input.Type))
	}

	s.saved = &BinaryState{
		BinaryID: binaries[0].ID,
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

func (s *BinaryStep) Destroy(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[BinaryState](saved.Spec)
		s.meta = saved.Meta
	}
	// Binaries persist in the database — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *BinaryStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

func newBinaryStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op *api.Operation,
) (workflow.Step, error) {
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
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newBinaryStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op *api.Operation,
) (workflow.Step, error) {
	bs := StateFromMap[BinaryState](saved.Spec)
	return &BinaryStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		input: inputs.BinaryPullInput{
			Type: name,
		},
		op:    op,
		saved: bs,
		meta:  saved.Meta,
	}, nil
}
