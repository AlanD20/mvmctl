package env

import (
	"context"
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// CPState is the persisted state for a copy step.
type CPState struct {
	Source string `yaml:"source"`
	WasRun bool   `yaml:"was_run"`
}

// CopyStep implements workflow.Step for copying files to/from VMs.
// Destroy is a no-op because file copies are ephemeral.
type CopyStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.CPInput
	op       *api.Operation
	saved    *CPState
}

func (s *CopyStep) Type() string { return s.stepType }

func (s *CopyStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *CopyStep) Dependencies() []string { return s.deps }

func (s *CopyStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Copy commands are imperative — always execute on apply.
	// SSH reachability is guaranteed by the DAG (SSH step runs first).
	// No retry loop needed — waitForSSH already confirmed port 22 is open.
	if _, err := s.op.CPCopy(ctx, s.input, nil); err != nil {
		return err
	}

	source := ""
	if len(s.input.Sources) > 0 {
		source = s.input.Sources[0]
	}
	s.saved = &CPState{
		Source: source,
		WasRun: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *CopyStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	// File copies are ephemeral — no teardown needed.
	return nil
}

func (s *CopyStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newCopyStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceSpec,
	op *api.Operation,
) (workflow.Step, error) {
	// Normalize single-string src to a slice for proper YAML unmarshalling into []string.
	// YAML spec allows `src: ./mvm` as a convenience — marshal/unmarshal would fail
	// because a single string cannot be decoded into a []string field.
	norm := make(model.ResourceSpec, len(spec))
	maps.Copy(norm, spec)
	if src, ok := norm["src"]; ok {
		if s, ok := src.(string); ok {
			norm["src"] = []any{s}
		}
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
	target, _ := spec["target"].(string)
	dst, _ := spec["dst"].(string)
	if target != "" && dst != "" {
		input.Dst = target + ":" + dst
	}
	return &CopyStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newCopyStepFromState(
	stepType string,
	name string,
	saved model.ResourceSpec,
	deps []string,
	op *api.Operation,
) (workflow.Step, error) {
	cs := StateFromMap[CPState](saved)
	return &CopyStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    cs,
	}, nil
}
