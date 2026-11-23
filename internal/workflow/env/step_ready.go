package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// ReadyState records a successful check; apply always checks again.
type ReadyState struct {
	Target  string `yaml:"target"`
	Command string `yaml:"command"`
}

// ReadyStep gates dependent resources on a bounded guest command check.
type ReadyStep struct {
	stepType  string
	name      string
	deps      []string
	removes   []string
	specHash  string
	input     inputs.ReadyInput
	inputSpec model.ResourceMap
	op        api.ReadyAPI
	saved     *ReadyState
	meta      model.ResourceMeta
}

// Type returns the workflow resource type.
func (s *ReadyStep) Type() string { return s.stepType }

// Name returns the qualified step name.
func (s *ReadyStep) Name() string { return FormatStepName(s.stepType, s.name) }

// Dependencies returns steps which must finish first.
func (s *ReadyStep) Dependencies() []string { return s.deps }

// Removes returns post-success cleanup targets.
func (s *ReadyStep) Removes() []string { return s.removes }

// SpecHash returns the declaration hash.
func (s *ReadyStep) SpecHash() string { return s.specHash }

// Apply always reruns the check, including when saved state exists.
func (s *ReadyStep) Apply(ctx context.Context, state *workflow.SharedState, _ model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	input := s.input
	var err error
	input.Identifier, err = resolveResourceReference(state, input.Identifier)
	if err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, s.input.Budget())
	defer cancel()
	if onProgress != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "waiting for guest readiness"})
	}
	var last error
	for {
		if checkCtx.Err() != nil {
			return errs.WrapMsg(
				errs.CodeVsockExecFailed,
				s.Name()+": readiness did not complete",
				errors.Join(checkCtx.Err(), last),
			)
		}
		result, err := s.op.ExecReady(checkCtx, input)
		if checkCtx.Err() != nil {
			return errs.WrapMsg(errs.CodeVsockExecFailed, s.Name()+": readiness did not complete",
				errors.Join(checkCtx.Err(), err, last))
		}
		if err != nil {
			if !readyConnectionRetryable(err) {
				return err
			}
			last = err
		} else {
			if result == nil {
				return errs.New(errs.CodeInternal, "readiness returned no command result")
			}
			if result.ExitCode == 0 {
				break
			}
			last = errs.New(
				errs.CodeVsockExecFailed,
				fmt.Sprintf("guest check exited with code %d; stderr: %s; stdout: %s",
					result.ExitCode, result.Stderr, result.Stdout),
			)
		}
		timer := time.NewTimer(s.input.RetryInterval())
		select {
		case <-checkCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	s.saved = &ReadyState{Target: input.Identifier, Command: input.Command}
	s.meta = model.ResourceMeta{SpecHash: s.specHash}
	state.Set(s.Name(), s.saved)
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist readiness state: %w", err)
	}
	return nil
}

func readyConnectionRetryable(err error) bool {
	var handshakeTransportClosed bool
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if domain, ok := cause.(*errs.DomainError); ok {
			if domain.Class == errs.ClassValidation || domain.Class == errs.ClassNeedsInteraction ||
				domain.Class == errs.ClassConflict || domain.Code == errs.CodeUnauthorized {
				return false
			}
			// A booting guest may close CONNECT before its listener accepts it.
			// Do not retry EOF/reset after command admission: that outcome is unknown.
			if domain.Code == errs.CodeVsockHandshakeFailed {
				handshakeTransportClosed = errors.Is(domain, io.EOF) ||
					errors.Is(domain, syscall.ECONNRESET) || errors.Is(domain, syscall.EPIPE)
			}
		}
	}
	return handshakeTransportClosed || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ENOTCONN)
}

// Destroy has no guest or host resource effect.
func (s *ReadyStep) Destroy(ctx context.Context, _ model.ResourceState, write workflow.StateWriter,
	_ event.OnProgressCallback) error {
	return write(ctx, s.StateData())
}

// StateData returns the declaration and last successful check.
func (s *ReadyStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{Spec: s.inputSpec, Output: StructToMap(s.saved), Meta: s.meta}
}

func newReadyStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var input inputs.ReadyInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	validationInput := input
	if strings.HasPrefix(input.Identifier, "@") {
		name, isVMReference := strings.CutPrefix(input.Identifier, "@vm:")
		if !isVMReference || name == "" || strings.ContainsAny(name, ":@ \t\r\n") {
			return nil, errs.New(errs.CodeValidationFailed, "readiness target requires an @vm:step reference")
		}
		// The graph admits the raw reference and dependency path. Validate the remaining
		// command fields now, then validate the real resolved identity before execution.
		validationInput.Identifier = "00000000000000000000000000000000"
	}
	if err := validationInput.Validate(); err != nil {
		return nil, err
	}
	return &ReadyStep{
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

func newReadyStepFromState(
	stepType, name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	return &ReadyStep{stepType: stepType, name: name, deps: deps, op: op, inputSpec: saved.Spec,
		saved: StateFromMap[ReadyState](saved.Output), meta: saved.Meta, specHash: saved.Meta.SpecHash}, nil
}
