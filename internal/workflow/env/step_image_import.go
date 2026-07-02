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

// ImageImportState is the persisted state for an image import step.
type ImageImportState struct {
	ImageID string `yaml:"image_id"`
}

// ImageImportStep implements workflow.Step for importing local images.
// Destroy is a no-op because images persist in the database.
type ImageImportStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.ImageImportInput
	op       api.ImageAPI
	saved    *ImageImportState
	meta     model.ResourceMeta
}

func (s *ImageImportStep) Type() string { return s.stepType }

func (s *ImageImportStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *ImageImportStep) Dependencies() []string { return s.deps }

func (s *ImageImportStep) SpecHash() string { return s.specHash }

func (s *ImageImportStep) Apply(
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
	existing, err := s.op.ImageGet(ctx, inputs.ImageInput{Identifiers: []string{s.input.Name}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check image name %q: %v", s.input.Name, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &ImageImportState{
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

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "importing image"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	img, err := s.op.ImageImport(ctx, s.input, stepProgress)
	if err != nil {
		return err
	}

	s.saved = &ImageImportState{
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

func (s *ImageImportStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[ImageImportState](saved.Spec)
		s.meta = saved.Meta
	}

	// Remove the imported image if we have its ID.
	if s.saved != nil && s.saved.ImageID != "" {
		onProgress(event.Progress{Phase: s.Name(), Status: "removing", Message: "removing imported image"})
		result := s.op.ImageRemove(ctx, inputs.ImageInput{
			Identifiers: []string{s.saved.ImageID},
		}, true)
		if result != nil && len(result.Items) > 0 && result.Items[0].Status == "error" {
			slog.Warn("failed to remove image on destroy",
				"image_id", s.saved.ImageID, "error", result.Items[0].Exception)
		}
	}

	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *ImageImportStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

// NewImageImportStep creates an ImageImportStep with the given API interface for testing.
// Only for use in tests.
func NewImageImportStep(op api.ImageAPI, name string, input inputs.ImageImportInput) *ImageImportStep {
	return &ImageImportStep{
		op:       op,
		name:     name,
		stepType: "image_import",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newImageImportStepFromSpec(
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

	var input inputs.ImageImportInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	return &ImageImportStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newImageImportStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	is := StateFromMap[ImageImportState](saved.Spec)
	return &ImageImportStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    is,
		meta:     saved.Meta,
	}, nil
}
