package env

import (
	"context"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// VolumeState binds a volume step to the exact acquired disk.
type VolumeState struct {
	VolumeID  string `yaml:"volume_id"`
	CreatedAt string `yaml:"created_at"`
}

type volumeDeclaration struct {
	Name      string `yaml:"name"`
	Size      string `yaml:"size"`
	ReadOnly  *bool  `yaml:"read_only"`
	Shareable *bool  `yaml:"shareable"`
	Writeback *bool  `yaml:"writeback"`
	Existing  string `yaml:"existing"`
	Retain    *bool  `yaml:"retain"`
}

// VolumeStep creates raw data disks and retains them unless deletion is explicitly selected.
type VolumeStep struct {
	resourceStepRecord
	input volumeDeclaration
	op    api.VolumeAPI
}

func (s *VolumeStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if err := s.restore(saved); err != nil {
		return err
	}
	if s.record.Output != nil {
		output := StateFromMap[VolumeState](s.record.Output)
		if output == nil || output.VolumeID == "" {
			return resourceIdentityMismatch(s.Name())
		}
		item, err := s.op.VolumeGet(ctx, inputs.VolumeInput{Identifiers: []string{output.VolumeID}})
		if err != nil {
			return err
		}
		if item == nil || item.ID != output.VolumeID || item.CreatedAt != output.CreatedAt {
			return resourceIdentityMismatch(s.Name())
		}
		state.Set(s.Name(), output)
		return s.persist(ctx, write)
	}
	var item *model.VolumeItem
	var err error
	if s.input.Existing != "" {
		selector, refErr := resourceStepReference(state, s.input.Existing, "volume")
		if refErr != nil {
			return refErr
		}
		item, err = s.op.VolumeGet(ctx, inputs.VolumeInput{Identifiers: []string{selector}})
	} else {
		item, err = s.op.VolumeCreate(ctx, inputs.VolumeCreateInput{Name: s.input.Name, Size: s.input.Size,
			ReadOnly: s.input.ReadOnly, Shareable: s.input.Shareable, Writeback: s.input.Writeback})
	}
	if err != nil {
		return err
	}
	if item == nil || item.ID == "" {
		return errs.New(errs.CodeInternal, "volume operation returned no identity")
	}
	output := &VolumeState{VolumeID: item.ID, CreatedAt: item.CreatedAt}
	s.record.Output = StructToMap(output)
	s.record.Meta.WasCreated = s.input.Existing == ""
	state.Set(s.Name(), output)
	return s.persist(ctx, write)
}

func (s *VolumeStep) Destroy(ctx context.Context, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if saved.Output != nil {
		s.record = saved
	}
	if !s.record.Meta.WasCreated {
		return s.persist(ctx, write)
	}
	output := StateFromMap[VolumeState](s.record.Output)
	if output == nil || output.VolumeID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	item, err := s.op.VolumeGet(ctx, inputs.VolumeInput{Identifiers: []string{output.VolumeID}})
	if errs.IsNotFound(err) {
		return s.persist(ctx, write)
	}
	if err != nil {
		return err
	}
	if item == nil {
		return s.persist(ctx, write)
	}
	if item.ID != output.VolumeID || item.CreatedAt != output.CreatedAt {
		return resourceIdentityMismatch(s.Name())
	}
	if s.input.Retain == nil || *s.input.Retain {
		// Keep live retained data inventoried; a proven external removal clears the record above.
		return nil
	}
	// CRITICAL: Never force detach a remaining consumer; protected backing removal remains API-owned.
	result := s.op.VolumeRemove(ctx, inputs.VolumeInput{Identifiers: []string{output.VolumeID}}, false)
	if result == nil {
		return errs.New(errs.CodeInternal, "volume removal returned no result")
	}
	for _, failure := range result.Errors() {
		if err := failure.ToError(); err != nil {
			return err
		}
	}
	return s.persist(ctx, write)
}

func newVolumeStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	record, data, err := newResourceStepRecord(stepType, name, spec)
	if err != nil {
		return nil, err
	}
	var input volumeDeclaration
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	if input.Name == "" {
		input.Name = name
	}
	if input.Existing != "" {
		for _, field := range []string{"name", "size", "read_only", "shareable", "writeback"} {
			if _, exists := spec[field]; exists {
				return nil, errs.New(errs.CodeValidationFailed, "existing volume cannot include create settings")
			}
		}
		if input.Retain != nil && !*input.Retain {
			return nil, errs.New(errs.CodeValidationFailed, "existing volumes must be retained")
		}
	} else if input.Size == "" {
		return nil, errs.New(errs.CodeValidationFailed, "volume size is required")
	}
	if input.Existing == "" {
		if err := validators.VolumeName(input.Name); err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "invalid volume name", err)
		}
		size, err := disk.ParseDiskSizeToBytes(input.Size)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "invalid volume size", err)
		}
		if size <= 0 {
			return nil, errs.New(errs.CodeValidationFailed, "volume size must be greater than zero")
		}
	}
	return &VolumeStep{resourceStepRecord: record, input: input, op: op}, nil
}

func newVolumeStepFromState(
	stepType, name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	step, err := newVolumeStepFromSpec(stepType, name, saved.Spec, op)
	if err != nil {
		return nil, err
	}
	s := step.(*VolumeStep)
	s.record, s.deps = saved, deps
	return s, nil
}
