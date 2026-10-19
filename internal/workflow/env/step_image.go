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
	stepType  string
	name      string
	deps      []string
	removes   []string
	specHash  string
	input     inputs.ImagePullInput
	inputSpec model.ResourceMap
	op        api.ImageAPI
	saved     *ImageState
	meta      model.ResourceMeta
}

func (s *ImageStep) Type() string { return s.stepType }

func (s *ImageStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *ImageStep) Dependencies() []string { return s.deps }

func (s *ImageStep) SpecHash() string  { return s.specHash }
func (s *ImageStep) Removes() []string { return s.removes }

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

	lookup := s.input.Type
	prior := StateFromMap[ImageState](saved.Output)
	if saved.Output != nil {
		if prior == nil || prior.ImageID == "" {
			return fmt.Errorf("%s: saved resource identity is invalid", s.Name())
		}
		if saved.Meta.SpecHash != s.specHash {
			return fmt.Errorf(
				"%s: declaration changed; destroy the saved resource before changing its declaration",
				s.Name(),
			)
		}
		lookup = prior.ImageID
	} else if selected := s.inputSpec.GetString("existing"); selected != "" {
		lookup = selected
	}
	// Recover WasCreated from saved meta.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.ImageGet(ctx, inputs.ImageInput{Identifiers: []string{lookup}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check image type %q: %v", s.input.Type, err),
			err,
		)
	}
	if prior != nil && (existing == nil || existing.ID != prior.ImageID) {
		return fmt.Errorf("%s: saved resource %s is missing; refusing replacement", s.Name(), prior.ImageID)
	}
	if s.inputSpec.GetString("existing") != "" && existing == nil {
		return fmt.Errorf("%s: selected existing resource was not found", s.Name())
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
	result, err := s.op.ImagePullWithResult(ctx, s.input, stepProgress)
	if result == nil || result.Image == nil || result.Image.ID == "" {
		if err != nil {
			return err
		}
		return errs.New(errs.CodeInternal, "image pull returned no identity")
	}

	s.saved = &ImageState{
		ImageID: result.Image.ID,
	}
	s.meta = model.ResourceMeta{
		WasCreated: result.Created,
		SpecHash:   s.specHash,
	}
	state.Set(s.Name(), s.saved)
	// Preserve committed ownership even when a later acquisition effect failed.
	if writeErr := write(context.WithoutCancel(ctx), s.StateData()); writeErr != nil {
		return errors.Join(err, fmt.Errorf("persist step state: %w", writeErr))
	}
	return err
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
	if s.saved == nil {
		if saved.Output != nil {
			s.saved = StateFromMap[ImageState](saved.Output)
		}
		if s.saved == nil && saved.Spec != nil {
			s.saved = StateFromMap[ImageState](saved.Spec)
		}
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
		Spec:   s.inputSpec,
		Output: StructToMap(s.saved),
		Meta:   s.meta,
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
		stepType:  stepType,
		name:      name,
		deps:      spec.GetStringList("depends_on"),
		removes:   spec.GetStringList("removes"),
		specHash:  crypto.SHA256(data),
		input:     input,
		inputSpec: spec,
		op:        op,
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

	var is *ImageState
	if saved.Output != nil {
		is = StateFromMap[ImageState](saved.Output)
	}
	if is == nil && saved.Spec != nil {
		is = StateFromMap[ImageState](saved.Spec)
	}
	return &ImageStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    is,
		meta:     saved.Meta,
	}, nil
}
