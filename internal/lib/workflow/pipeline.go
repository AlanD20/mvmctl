package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
)

// Pipeline executes a set of steps in topological order. Steps within the
// same level are executed concurrently. The pipeline has zero domain
// knowledge — it only knows about the Step interface.
type Pipeline struct {
	steps  []Step
	levels [][]Step
}

// NewPipeline creates a Pipeline from a set of steps. The steps are
// topologically sorted using BuildDAG. Returns an error if the steps
// contain a cycle or have invalid dependencies.
func NewPipeline(steps []Step) (*Pipeline, error) {
	levels, err := BuildDAG(steps)
	if err != nil {
		return nil, fmt.Errorf("build DAG: %w", err)
	}
	return &Pipeline{
		steps:  steps,
		levels: levels,
	}, nil
}

// ── Execute options ──

type executeOptions struct {
	onStepComplete func(ctx context.Context, step Step, stateData model.ResourceState) error
}

// ExecuteOption configures the pipeline execution.
type ExecuteOption func(*executeOptions)

// WithOnStepComplete registers a callback that is invoked after each step's
// Apply completes successfully. The callback receives the step and its
// StateData at the time of completion. The callback must be thread-safe.
// The pipeline wraps this into a StateWriter and passes it to each step's
// Apply method so the step can persist its state immediately.
func WithOnStepComplete(cb func(ctx context.Context, step Step, stateData model.ResourceState) error) ExecuteOption {
	return func(opts *executeOptions) {
		opts.onStepComplete = cb
	}
}

// ── Execute ──

// Execute runs all steps in topological order. Steps at the same level
// are executed concurrently. For each step, Apply is called with the
// shared state and any previously-saved state for that step. The first
// step error halts the pipeline and is returned.
//
// The savedResources parameter contains state data from a prior workflow
// execution. Steps can use it to detect re-apply and skip re-execution.
// Pass nil or an empty slice for fresh executions.
//
// The onProgress callback is invoked before each step starts with the
// step name as the phase and "running" as the status, and after each
// step completes with "complete" (or "failed" on error).
//
// Additional options can be passed via ExecuteOption, such as
// WithOnStepComplete to persist state after each step's Apply succeeds.
func (p *Pipeline) Execute(
	ctx context.Context,
	state *SharedState,
	onProgress event.OnProgressCallback,
	savedResources []model.AppliedResource,
	opts ...ExecuteOption,
) error {
	if len(p.levels) == 0 {
		return nil
	}

	var cfg executeOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	// Build a lookup from step name to saved state for re-apply detection.
	savedByStep := make(map[string]model.ResourceState, len(savedResources))
	for _, sr := range savedResources {
		savedByStep[sr.Name] = sr.State
	}

	for _, level := range p.levels {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Concurrent level: run all steps in this level in parallel.
		// errgroup cancels sibling goroutines on first error.
		g, stepCtx := errgroup.WithContext(ctx)

		for _, step := range level {
			g.Go(func() error {
				var write StateWriter
				if cfg.onStepComplete != nil {
					write = func(ctx context.Context, data model.ResourceState) error {
						return cfg.onStepComplete(ctx, step, data)
					}
				}

				emitProgress(onProgress, step.Name(), "running", "starting")
				if err := step.Apply(stepCtx, state, savedByStep[step.Name()], write, onProgress); err != nil {
					emitProgress(onProgress, step.Name(), "failed", err.Error())
					return fmt.Errorf("step %q: %w", step.Name(), err)
				}
				emitProgress(onProgress, step.Name(), "complete", "done")
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return err
		}
	}

	return nil
}

// ── Destroy options ──

type destroyOptions struct {
	onStepComplete func(ctx context.Context, step Step, stateData model.ResourceState) error
}

// DestroyOption configures the pipeline destroy execution.
type DestroyOption func(*destroyOptions)

// WithDestroyOnStepComplete registers a callback that is invoked after each
// step's Destroy completes successfully. The callback receives the step and
// its StateData at the time of completion. The callback must be thread-safe.
// The pipeline wraps this into a StateWriter and passes it to each step's
// Destroy method so the step can persist its state immediately.
func WithDestroyOnStepComplete(cb func(ctx context.Context, step Step, stateData model.ResourceState) error) DestroyOption {
	return func(opts *destroyOptions) {
		opts.onStepComplete = cb
	}
}

// ── Destroy ──

// Destroy runs the Destroy method on each step in reverse topological
// order (deepest level first). This ensures that resources are torn down
// in dependency order — steps that depend on others are destroyed first.
//
// Each saved resource contains the name, type, dependencies, and
// the state data that was persisted after Apply. The step implementation
// decides what to destroy based on its saved state.
//
// The onProgress callback follows the same convention as Execute.
func (p *Pipeline) Destroy(
	ctx context.Context,
	savedResources []model.AppliedResource,
	onProgress event.OnProgressCallback,
	opts ...DestroyOption,
) error {
	if len(p.levels) == 0 {
		return nil
	}

	var cfg destroyOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	// Build a lookup from step name to saved state.
	savedByStep := make(map[string]model.AppliedResource, len(savedResources))
	for _, sr := range savedResources {
		savedByStep[sr.Name] = sr
	}

	// Build a lookup from step name to Step.
	stepByName := make(map[string]Step, len(p.steps))
	for _, s := range p.steps {
		stepByName[s.Name()] = s
	}

	// Walk levels in reverse order.
	var firstErr error
	for levelIdx := len(p.levels) - 1; levelIdx >= 0; levelIdx-- {
		level := p.levels[levelIdx]

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Concurrent destroy for the level.
		// errgroup cancels sibling goroutines on first error within
		// this level, but we continue to the next level regardless.
		g, stepCtx := errgroup.WithContext(ctx)

		for _, step := range level {
			g.Go(func() error {
				var write StateWriter
				if cfg.onStepComplete != nil {
					write = func(ctx context.Context, data model.ResourceState) error {
						return cfg.onStepComplete(ctx, step, data)
					}
				}

				saved := savedByStep[step.Name()]
				emitProgress(onProgress, step.Name(), "running", "destroying")
				if err := step.Destroy(stepCtx, saved.State, write, onProgress); err != nil {
					emitProgress(onProgress, step.Name(), "failed", err.Error())
					return fmt.Errorf("destroy step %q: %w", step.Name(), err)
				}
				emitProgress(onProgress, step.Name(), "complete", "destroyed")
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			slog.Warn("destroy step failed in concurrent level", "level", levelIdx, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// Steps returns the steps in the pipeline.
func (p *Pipeline) Steps() []Step {
	return p.steps
}

// Levels returns the topological levels of the pipeline.
func (p *Pipeline) Levels() [][]Step {
	return p.levels
}

// emitProgress calls the progress callback if non-nil.
func emitProgress(onProgress event.OnProgressCallback, phase, status, msg string) {
	if onProgress == nil {
		return
	}
	onProgress(event.Progress{Phase: phase, Status: status, Message: msg})
}
