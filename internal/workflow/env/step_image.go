package env

import (
	"context"
	"errors"
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

// ImageState is the persisted state for an image step.
type ImageState struct {
	ImageID string `yaml:"image_id"`
}

// ImageStep implements workflow.Step for pulling images.
// Destroy is a no-op because images persist in the database.
type ImageStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.ImagePullInput
	op       api.ImageAPI
	saved    *ImageState
	meta     model.ResourceMeta
}

func (s *ImageStep) Type() string { return s.stepType }

func (s *ImageStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *ImageStep) Dependencies() []string { return s.deps }

func (s *ImageStep) SpecHash() string { return s.specHash }

func (s *ImageStep) Apply(
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
	existing, err := s.op.ImageGet(ctx, inputs.ImageInput{Identifiers: []string{s.input.Type}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check image type %q: %v", s.input.Type, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &ImageState{
			ImageID: existing.ID,
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

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "pulling image"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	img, err := s.op.ImagePull(ctx, s.input, stepProgress)
	if err != nil {
		return err
	}

	s.saved = &ImageState{
		ImageID: img.ID,
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

func (s *ImageStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[ImageState](saved.Spec)
		s.meta = saved.Meta
	}
	// Images persist in the database — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *ImageStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

// NewImageStep creates an ImageStep with the given API interface for testing.
// Only for use in tests.
func NewImageStep(op api.ImageAPI, name string, input inputs.ImagePullInput) *ImageStep {
	return &ImageStep{
		op:       op,
		name:     name,
		stepType: "image",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newImageStepFromSpec(
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

	var input inputs.ImagePullInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	return &ImageStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newImageStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	is := StateFromMap[ImageState](saved.Spec)
	return &ImageStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    is,
		meta:     saved.Meta,
	}, nil
}
