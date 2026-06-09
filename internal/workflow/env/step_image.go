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

// ImageState is the persisted state for an image step.
type ImageState struct {
	ImageID    string `yaml:"image_id"`
	WasCreated bool   `yaml:"was_created"`
}

// ImageStep implements workflow.Step for pulling images.
// Destroy is a no-op because images persist in the database.
type ImageStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.ImagePullInput
	op       *api.Operation
	saved    *ImageState
}

func (s *ImageStep) Type() string { return s.stepType }

func (s *ImageStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *ImageStep) Dependencies() []string { return s.deps }

func (s *ImageStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	var prev *ImageState
	if saved != nil {
		prev = StateFromMap[ImageState](saved)
	}
	existing, err := s.op.Repos.Image.GetByType(ctx, s.input.Type)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check image type %q: %v", s.input.Type, err),
			err,
		)
	}
	if existing != nil {
		wasCreated := prev != nil && prev.WasCreated
		s.saved = &ImageState{
			ImageID:    existing.ID,
			WasCreated: wasCreated,
		}
		state.Set(s.Name(), s.saved)
		return nil
	}

	img, err := s.op.ImagePull(ctx, s.input, nil)
	if err != nil {
		return err
	}

	s.saved = &ImageState{
		ImageID:    img.ID,
		WasCreated: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *ImageStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	// Images persist in the database — no teardown needed.
	return nil
}

func (s *ImageStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newImageStepFromSpec(stepType string, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
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
		input:    input,
		op:       op,
	}, nil
}

func newImageStepFromState(stepType string, name string, saved model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	is := StateFromMap[ImageState](saved)
	return &ImageStep{
		stepType: stepType,
		name:     name,
		op:       op,
		saved:    is,
	}, nil
}
