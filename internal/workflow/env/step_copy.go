package env

import (
	"context"
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// CPState is the persisted state for a copy step.
type CPState struct {
	Source string `yaml:"source"`
}

// CopyStep implements workflow.Step for copying files to/from VMs.
// Destroy is a no-op because file copies are ephemeral.
type CopyStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.CPInput
	op       *api.Operation
	saved    *CPState
	meta     model.ResourceMeta
}

func (s *CopyStep) Type() string { return s.stepType }

func (s *CopyStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *CopyStep) Dependencies() []string { return s.deps }

func (s *CopyStep) SpecHash() string { return s.specHash }

func (s *CopyStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Copy commands are imperative — always execute on apply.
	// SSH reachability is guaranteed by the DAG (SSH step runs first).
	// No retry loop needed — waitForSSH already confirmed port 22 is open.
	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "copying files"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	if _, err := s.op.CPCopy(ctx, s.input, event.FormatProgress(stepProgress)); err != nil {
		return err
	}

	source := ""
	if len(s.input.Sources) > 0 {
		source = s.input.Sources[0]
	}
	s.saved = &CPState{
		Source: source,
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

func (s *CopyStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[CPState](saved.Spec)
		s.meta = saved.Meta
	}
	// File copies are ephemeral — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *CopyStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

func newCopyStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op *api.Operation,
) (workflow.Step, error) {
	// Normalize single-string src to a slice for proper YAML unmarshalling into []string.
	// YAML spec allows `src: ./mvm` as a convenience — marshal/unmarshal would fail
	// because a single string cannot be decoded into a []string field.
	norm := make(model.ResourceMap, len(spec))
	maps.Copy(norm, spec)
	if s := norm.GetString("src"); s != "" {
		norm["src"] = []any{s}
	}
	data, err := yaml.Marshal(norm)
	if err != nil {
		return nil, err
	}
	var input inputs.CPInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	// Build Dst from target + ":" + dst
	target := spec.GetString("target")
	dst := spec.GetString("dst")
	if target != "" && dst != "" {
		input.Dst = target + ":" + dst
	}
	// Hash the original spec (not normalized) for drift detection.
	specData, _ := yaml.Marshal(spec)
	return &CopyStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		specHash: crypto.SHA256(specData),
		input:    input,
		op:       op,
	}, nil
}

func newCopyStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op *api.Operation,
) (workflow.Step, error) {
	cs := StateFromMap[CPState](saved.Spec)
	return &CopyStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    cs,
		meta:     saved.Meta,
	}, nil
}
