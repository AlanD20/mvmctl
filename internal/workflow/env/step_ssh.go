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
)

// SSHState is the persisted state for an SSH step.
type SSHState struct {
	Command string `yaml:"command"`
}

// SSHStep implements workflow.Step for running SSH commands on VMs.
// Destroy is a no-op because SSH commands are ephemeral.
type SSHStep struct {
	stepType string
	name     string
	deps     []string
	removes  []string
	specHash string
	input    inputs.SSHInput
	op       api.SSHAPI
	saved    *SSHState
	meta     model.ResourceMeta
}

func (s *SSHStep) Type() string { return s.stepType }

func (s *SSHStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *SSHStep) Dependencies() []string { return s.deps }

func (s *SSHStep) SpecHash() string  { return s.specHash }
func (s *SSHStep) Removes() []string { return s.removes }

func (s *SSHStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	// SSH commands are imperative — always execute on apply.
	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "running command"})
	// Blank line to visually separate output from the step status line.
	fmt.Println()
	// Wrap onProgress to inject step name into SSH output events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	if err := s.op.SSHConnect(ctx, s.input, stepProgress); err != nil {
		return err
	}
	cmd := ""
	if s.input.Cmd != nil {
		cmd = *s.input.Cmd
	}
	s.saved = &SSHState{
		Command: cmd,
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

func (s *SSHStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[SSHState](saved.Spec)
		s.meta = saved.Meta
	}
	// SSH commands are ephemeral — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *SSHStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

func newSSHStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	// Strip "type:" prefix from step reference fields.
	for _, ref := range []string{"target", "key"} {
		if s, ok := spec[ref].(string); ok {
			spec[ref] = stripBareName(s)
		}
	}

	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var input inputs.SSHInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	return &SSHStep{
		stepType: stepType,
		name:     name,
		deps:     spec.GetStringList("depends_on"),
		removes:  spec.GetStringList("removes"),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newSSHStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}
	ss := StateFromMap[SSHState](saved.Spec)
	return &SSHStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    ss,
		meta:     saved.Meta,
	}, nil
}
