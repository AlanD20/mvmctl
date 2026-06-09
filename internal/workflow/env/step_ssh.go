package env

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// SSHState is the persisted state for an SSH step.
type SSHState struct {
	Command string `yaml:"command"`
	WasRun  bool   `yaml:"was_run"`
}

// SSHStep implements workflow.Step for running SSH commands on VMs.
// Destroy is a no-op because SSH commands are ephemeral.
type SSHStep struct {
	stepType string
	name     string
	deps     []string
	input    inputs.SSHInput
	op       *api.Operation
	saved    *SSHState
}

func (s *SSHStep) Type() string { return s.stepType }

func (s *SSHStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *SSHStep) Dependencies() []string { return s.deps }

func (s *SSHStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// SSH commands are imperative — always execute on apply.
	if err := s.op.SSHConnect(ctx, s.input); err != nil {
		return err
	}
	cmd := ""
	if s.input.Cmd != nil {
		cmd = *s.input.Cmd
	}
	s.saved = &SSHState{
		Command: cmd,
		WasRun:  true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *SSHStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	// SSH commands are ephemeral — no teardown needed.
	return nil
}

func (s *SSHStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newSSHStepFromSpec(stepType string, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
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
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newSSHStepFromState(stepType string, name string, saved model.ResourceSpec, op *api.Operation) (workflow.Step, error) {
	ss := StateFromMap[SSHState](saved)
	return &SSHStep{
		stepType: stepType,
		name:     name,
		op:       op,
		saved:    ss,
	}, nil
}
