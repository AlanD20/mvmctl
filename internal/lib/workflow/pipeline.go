package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

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
	onStepComplete func(stepName string, stateData model.ResourceSpec)
}

// ExecuteOption configures the pipeline execution.
type ExecuteOption func(*executeOptions)

// WithStepCompleteCallback registers a callback that is invoked after each
// step's Apply completes successfully. The callback receives the step name
// and its StateData at the time of completion. This is called from the
// step's goroutine so the callback must be thread-safe.
func WithStepCompleteCallback(cb func(stepName string, stateData model.ResourceSpec)) ExecuteOption {
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
// WithStepCompleteCallback to collect state data incrementally.
func (p *Pipeline) Execute(
	ctx context.Context,
	state *SharedState,
	onProgress func(phase, status, msg string),
	savedResources []model.SavedResource,
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
	savedByStep := make(map[string]model.ResourceSpec, len(savedResources))
	for _, sr := range savedResources {
		savedByStep[sr.StepName] = sr.State
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
			step := step // capture
			g.Go(func() error {
				emitP(onProgress, step.Name(), "running", "")
				if err := step.Apply(stepCtx, state, savedByStep[step.Name()]); err != nil {
					emitP(onProgress, step.Name(), "failed", err.Error())
					return fmt.Errorf("step %q: %w", step.Name(), err)
				}
				emitP(onProgress, step.Name(), "complete", "")

				if cfg.onStepComplete != nil {
					cfg.onStepComplete(step.Name(), step.StateData())
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return err
		}
	}

	return nil
}

// ── Destroy ──

// Destroy runs the Destroy method on each step in reverse topological
// order (deepest level first). This ensures that resources are torn down
// in dependency order — steps that depend on others are destroyed first.
//
// Each saved resource contains the step name, type, dependencies, and
// the state data that was persisted after Apply. The step implementation
// decides what to destroy based on its saved state.
//
// The onProgress callback follows the same convention as Execute.
func (p *Pipeline) Destroy(
	ctx context.Context,
	savedResources []model.SavedResource,
	onProgress func(phase, status, msg string),
) error {
	if len(p.levels) == 0 {
		return nil
	}

	// Build a lookup from step name to saved state.
	savedByStep := make(map[string]model.SavedResource, len(savedResources))
	for _, sr := range savedResources {
		savedByStep[sr.StepName] = sr
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
			step := step
			g.Go(func() error {
				saved := savedByStep[step.Name()]
				emitP(onProgress, step.Name(), "running", "destroying")
				if err := step.Destroy(stepCtx, saved.State); err != nil {
					emitP(onProgress, step.Name(), "failed", err.Error())
					return fmt.Errorf("destroy step %q: %w", step.Name(), err)
				}
				emitP(onProgress, step.Name(), "complete", "destroyed")
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

// emitP calls the progress callback if non-nil.
func emitP(onProgress func(phase, status, msg string), phase, status, msg string) {
	if onProgress == nil {
		return
	}
	onProgress(phase, status, msg)
}
