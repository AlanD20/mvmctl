// Package workflow provides a generic DAG-based pipeline engine for
// provisioning resources from YAML specs. The pipeline is domain-agnostic —
// step implementations live in internal/workflow/env.
package workflow

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Step defines a single unit of work in a provisioning pipeline.
// Each step is responsible for its own lifecycle (apply + destroy), declares
// its dependencies, and exposes state data for persistence.
type Step interface {
	// Name returns a unique identifier for this step within the pipeline.
	Name() string

	// Type returns the resource type for this step (e.g. "networks", "vms").
	// This is the plural Registry key used to classify the step.
	Type() string

	// Dependencies returns the names of steps that must complete before
	// this step can run. Return nil for steps with no dependencies.
	Dependencies() []string

	// Apply provisions the resource. The step receives a shared state that
	// it can read from (dependency outputs) and write to (its own output).
	// Apply returns an error if the operation should halt the pipeline.
	// The saved parameter contains previously persisted state data for this
	// step (from a prior workflow execution). Steps can use it to detect
	// re-apply and skip re-execution. If nil, this is a fresh execution.
	Apply(ctx context.Context, state *SharedState, saved model.ResourceSpec) error

	// Destroy tears down the resource using previously saved state data.
	// The saved resource spec contains whatever was returned by StateData()
	// when the workflow state was persisted. It is the step's responsibility
	// to determine what to destroy from this data.
	Destroy(ctx context.Context, saved model.ResourceSpec) error

	// StateData returns a snapshot of the step's state for persistence.
	// This is called after Apply completes successfully. The returned
	// ResourceSpec is saved to the workflow state file and passed back
	// to Destroy.
	StateData() model.ResourceSpec
}

// StepFunc is an adapter that creates a Step from individual function
// literals. This allows creating ad-hoc steps without defining a full struct.
type StepFunc struct {
	stepType    string
	name         string
	deps         []string
	applyFn      func(ctx context.Context, state *SharedState, saved model.ResourceSpec) error
	destroyFn    func(ctx context.Context, saved model.ResourceSpec) error
	stateDataFn  func() model.ResourceSpec
}

// NewStepFunc creates a StepFunc adapter from function literals.
func NewStepFunc(
	stepType string,
	name string,
	deps []string,
	apply func(ctx context.Context, state *SharedState, saved model.ResourceSpec) error,
	destroy func(ctx context.Context, saved model.ResourceSpec) error,
	stateData func() model.ResourceSpec,
) *StepFunc {
	return &StepFunc{
		stepType:    stepType,
		name:        name,
		deps:        deps,
		applyFn:     apply,
		destroyFn:   destroy,
		stateDataFn: stateData,
	}
}

func (s *StepFunc) Name() string                       { return s.name }
func (s *StepFunc) Type() string                       { return s.stepType }
func (s *StepFunc) Dependencies() []string              { return s.deps }
func (s *StepFunc) Apply(ctx context.Context, state *SharedState, saved model.ResourceSpec) error { return s.applyFn(ctx, state, saved) }
func (s *StepFunc) Destroy(ctx context.Context, saved model.ResourceSpec) error { return s.destroyFn(ctx, saved) }
func (s *StepFunc) StateData() model.ResourceSpec             { return s.stateDataFn() }
