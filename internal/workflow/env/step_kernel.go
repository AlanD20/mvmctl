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

// KernelState is the persisted state for a kernel step.
type KernelState struct {
	KernelID string `yaml:"kernel_id"`
}

// KernelStep implements workflow.Step for pulling kernels.
// Destroy is a no-op because kernels persist in the database.
type KernelStep struct {
	stepType string
	name     string
	deps     []string
	removes  []string
	specHash string
	input    inputs.KernelPullInput
	op       api.KernelAPI
	saved    *KernelState
	meta     model.ResourceMeta
}

func (s *KernelStep) Type() string { return s.stepType }

func (s *KernelStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *KernelStep) Dependencies() []string { return s.deps }

func (s *KernelStep) SpecHash() string  { return s.specHash }
func (s *KernelStep) Removes() []string { return s.removes }

func (s *KernelStep) Apply(
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
	existing, err := s.op.KernelGet(ctx, s.input.KernelType)
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check kernel type %q: %v", s.input.KernelType, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &KernelState{
			KernelID: existing.ID,
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

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "pulling kernel"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	krnl, err := s.op.KernelPull(ctx, s.input, stepProgress)
	if err != nil {
		return err
	}

	s.saved = &KernelState{
		KernelID: krnl.ID,
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

func (s *KernelStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[KernelState](saved.Spec)
		s.meta = saved.Meta
	}
	// Kernels persist in the database — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *KernelStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

// NewKernelStep creates a KernelStep with the given API interface for testing.
// Only for use in tests.
func NewKernelStep(op api.KernelAPI, name string, input inputs.KernelPullInput) *KernelStep {
	return &KernelStep{
		op:       op,
		name:     name,
		stepType: "kernel",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newKernelStepFromSpec(
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

	var input inputs.KernelPullInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}

	// Canonical spec key is "type". The "kernel_type" key is also accepted via
	// the YAML struct tag — selecting both with different values is undefined.
	if kernelType := spec.GetString("type"); kernelType != "" {
		input.KernelType = kernelType
	}

	return &KernelStep{
		stepType: stepType,
		name:     name,
		deps:     spec.GetStringList("depends_on"),
		removes:  spec.GetStringList("removes"),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newKernelStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	ks := StateFromMap[KernelState](saved.Spec)
	return &KernelStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    ks,
		meta:     saved.Meta,
	}, nil
}
