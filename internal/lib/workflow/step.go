// Package workflow provides a generic DAG-based pipeline engine for
// provisioning resources from YAML specs. The pipeline is domain-agnostic —
// step implementations live in internal/workflow/env.
package workflow

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
)

// StateWriter persists a step's state after a successful Apply or Destroy.
// The step calls this after its API operation succeeds but before returning.
// If it returns an error, the step should abort.
type StateWriter func(ctx context.Context, stateData model.ResourceState) error

// Step defines a single unit of work in a provisioning pipeline.
// Each step is responsible for its own lifecycle (apply + destroy), declares
// its dependencies, and exposes state data for persistence.
type Step interface {
	// Name returns a unique identifier for this step within the pipeline.
	Name() string

	// Type returns the resource type for this step (e.g. "network", "vm").
	// This is the singular step type identifier (e.g. "network", "vm", "key").
	Type() string

	// Dependencies returns the names of steps that must complete before
	// this step can run. Return nil for steps with no dependencies.
	Dependencies() []string

	// Apply provisions the resource. The step receives a shared state that
	// it can read from (dependency outputs) and write to (its own output).
	// Apply returns an error if the operation should halt the pipeline.
	// The saved parameter contains previously persisted state for this
	// step (from a prior workflow execution). Steps can use it to detect
	// re-apply and skip re-execution. If the zero value, this is a fresh execution.
	// The write parameter is a StateWriter that the step calls after a successful
	// API operation to persist its state immediately.
	// The onProgress callback reports granular progress events — the same type
	// used by pkg/api operations. Steps emit events like:
	//   onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "pulling image"})
	Apply(
		ctx context.Context,
		state *SharedState,
		saved model.ResourceState,
		write StateWriter,
		onProgress event.OnProgressCallback,
	) error

	// Destroy tears down the resource using previously saved state.
	// The saved resource state contains whatever was returned by StateData()
	// when the workflow state was persisted. It is the step's responsibility
	// to determine what to destroy from this data.
	// The write parameter is a StateWriter that the step calls after a successful
	// destroy to update the persisted state.
	Destroy(
		ctx context.Context,
		saved model.ResourceState,
		write StateWriter,
		onProgress event.OnProgressCallback,
	) error

	// StateData returns a snapshot of the step's state for persistence.
	// This is called after Apply completes successfully. The returned
	// ResourceState is saved to the workflow state file and passed back
	// to Destroy.
	StateData() model.ResourceState

	// SpecHash returns a content hash of the step's input specification.
	// This is used for drift detection: if the spec hash changes between
	// apply runs, the resource is considered "drifted" and may need re-apply.
	// Steps that don't support drift detection return an empty string.
	SpecHash() string
}

// StepFunc is an adapter that creates a Step from individual function
// literals. This allows creating ad-hoc steps without defining a full struct.
type StepFunc struct {
	stepType    string
	name        string
	deps        []string
	specHash    string
	applyFn     func(ctx context.Context, state *SharedState, saved model.ResourceState, write StateWriter, onProgress event.OnProgressCallback) error
	destroyFn   func(ctx context.Context, saved model.ResourceState, write StateWriter, onProgress event.OnProgressCallback) error
	stateDataFn func() model.ResourceState
}

// NewStepFunc creates a StepFunc adapter from function literals.
func NewStepFunc(
	stepType string,
	name string,
	deps []string,
	apply func(ctx context.Context, state *SharedState, saved model.ResourceState, write StateWriter, onProgress event.OnProgressCallback) error,
	destroy func(ctx context.Context, saved model.ResourceState, write StateWriter, onProgress event.OnProgressCallback) error,
	stateData func() model.ResourceState,
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

func (s *StepFunc) Name() string           { return s.name }
func (s *StepFunc) Type() string           { return s.stepType }
func (s *StepFunc) Dependencies() []string { return s.deps }

func (s *StepFunc) Apply(
	ctx context.Context,
	state *SharedState,
	saved model.ResourceState,
	write StateWriter,
	onProgress event.OnProgressCallback,
) error {
	return s.applyFn(ctx, state, saved, write, onProgress)
}

func (s *StepFunc) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write StateWriter,
	onProgress event.OnProgressCallback,
) error {
	return s.destroyFn(ctx, saved, write, onProgress)
}
func (s *StepFunc) StateData() model.ResourceState { return s.stateDataFn() }
func (s *StepFunc) SpecHash() string               { return s.specHash }
