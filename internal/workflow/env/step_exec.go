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
)

// ExecState is the persisted state for an Exec step.
type ExecState struct {
	Command string `yaml:"command"`
}

// ExecStep implements workflow.Step for running commands inside VMs via vsock.
// Destroy is a no-op because exec commands are ephemeral.
type ExecStep struct {
	stepType     string
	name         string
	deps         []string
	removes      []string
	specHash     string
	input        inputs.ExecInput
	inputSpec    model.ResourceMap
	op           api.ExecAPI
	ignoreErrors bool
	saved        *ExecState
	meta         model.ResourceMeta
}

func (s *ExecStep) Type() string { return s.stepType }

func (s *ExecStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *ExecStep) Dependencies() []string { return s.deps }

func (s *ExecStep) SpecHash() string  { return s.specHash }
func (s *ExecStep) Removes() []string { return s.removes }

func (s *ExecStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	// Exec commands are imperative — always execute on apply.
	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "running command"})

	// Command is sent to the guest agent as-is — the agent wraps it in
	// sh -c <command> internally (see agent.handleExec).
	if s.input.Command == "" {
		return fmt.Errorf("%s: empty command", s.Name())
	}

	result, err := s.op.Exec(ctx, s.input)
	if err != nil {
		return err
	}

	if result != nil && result.ExitCode != 0 {
		if s.ignoreErrors {
			slog.Warn("command exited with non-zero code, continuing (ignore_errors=true)",
				"step", s.Name(), "exit_code", result.ExitCode)
		} else {
			return fmt.Errorf("%s: command exited with code %d", s.Name(), result.ExitCode)
		}
	}

	// Print a blank line to visually separate from the step status line.
	// Output is streamed directly by the vsock client during execution.
	fmt.Println()

	s.saved = &ExecState{
		Command: s.input.Command,
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

func (s *ExecStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.saved == nil {
		if saved.Output != nil {
			s.saved = StateFromMap[ExecState](saved.Output)
		}
		if s.saved == nil && saved.Spec != nil {
			s.saved = StateFromMap[ExecState](saved.Spec)
		}
		s.meta = saved.Meta
	}
	// Exec commands are ephemeral — no teardown needed.
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *ExecStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec:   s.inputSpec,
		Output: StructToMap(s.saved),
		Meta:   s.meta,
	}
}

func newExecStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	// Strip "type:" prefix from step reference fields.
	if s, ok := spec["target"].(string); ok {
		spec["target"] = stripBareName(s)
	}

	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var input inputs.ExecInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	return &ExecStep{
		stepType:     stepType,
		name:         name,
		deps:         spec.GetStringList("depends_on"),
		removes:      spec.GetStringList("removes"),
		specHash:     crypto.SHA256(data),
		input:        input,
		inputSpec:    spec,
		op:           op,
		ignoreErrors: spec.GetBool("ignore_errors"),
	}, nil
}

func newExecStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}
	var ss *ExecState
	if saved.Output != nil {
		ss = StateFromMap[ExecState](saved.Output)
	}
	if ss == nil && saved.Spec != nil {
		ss = StateFromMap[ExecState](saved.Spec)
	}
	return &ExecStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		op:       op,
		saved:    ss,
		meta:     saved.Meta,
	}, nil
}
