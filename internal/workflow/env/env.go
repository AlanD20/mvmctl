package env

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"
)

// ── Constants ──

const envStateSchemaVersion = "1.0"

// ── Apply ──

// Apply reads a YAML spec file, resolves it into a DAG of provisioning
// steps, and executes them in topological order. The result is persisted as
// a workflow state file in the cache directory.
//
// State data is collected per-step during execution via an
// onStepComplete callback, so even if execution fails partway through,
// the state from completed steps is persisted.
func Apply(
	ctx context.Context,
	op *api.Operation,
	specPath string,
	onProgress event.OnProgressCallback,
) error {
	// Resolve YAML spec into steps.
	steps, err := ResolveSpec(ctx, specPath, op)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("resolve env spec %s: %v", specPath, err),
			err,
		)
	}

	if len(steps) == 0 {
		return errs.New(errs.CodeValidationFailed, "env spec contains no resources")
	}

	// Build the pipeline.
	pipeline, err := workflow.NewPipeline(steps)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("build pipeline for %s: %v", specPath, err),
			err,
		)
	}

	// Derive workflow ID and state dir.
	wfID := crypto.WorkflowID(specPath)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)

	// Create shared state and run the pipeline.
	state := workflow.NewSharedState()

	// Read existing workflow state for re-apply detection.
	var prevResources []model.SavedResource
	prevState, rErr := workflow.ReadWorkflowState(stateDir)
	if rErr == nil && prevState != nil {
		prevResources = prevState.Resources
	}

	// Bridge between event.OnProgressCallback and pipeline callback.
	progressFn := toPipelineProgress(onProgress)

	// Collect saved resources per-step during execution.
	// The callback is invoked from step goroutines and must be thread-safe.
	var (
		mu        sync.Mutex
		resources []model.SavedResource
		createdAt string
	)

	// Build step type and dependency lookups for the callback.
	stepTypeByStepName := make(map[string]string, len(steps))
	stepDeps := make(map[string][]string, len(steps))
	for _, s := range steps {
		stepTypeByStepName[s.Name()] = s.Type()
		stepDeps[s.Name()] = s.Dependencies()
	}

	onStepComplete := func(stepName string, stateData model.ResourceSpec) {
		mu.Lock()
		// createdAt is set once, on the first step completion. The check-then-set
		// is inside the mutex — do NOT move this outside the lock.
		if createdAt == "" {
			createdAt = infra.Now()
		}
		resources = append(resources, model.SavedResource{
			StepName:     stepName,
			StepType:     stepTypeByStepName[stepName],
			Dependencies: stepDeps[stepName],
			State:        stateData,
		})
		mu.Unlock()
	}

	err = pipeline.Execute(ctx, state, progressFn, prevResources, workflow.WithStepCompleteCallback(onStepComplete))

	// ═ Persist whatever state was collected (partial or full) ═
	mu.Lock()
	if createdAt == "" {
		createdAt = infra.Now()
	}
	if len(resources) > 0 {
		wfState := &model.WorkflowState{
			WorkflowID:    wfID,
			SpecPath:      specPath,
			SchemaVersion: envStateSchemaVersion,
			CreatedAt:     createdAt,
			UpdatedAt:     infra.Now(),
			Resources:     resources,
		}
		if pErr := workflow.WriteWorkflowState(stateDir, wfState); pErr != nil {
			slog.Warn("failed to persist workflow state", "wf_id", wfID, "error", pErr)
		} else {
			slog.Info("workflow state persisted", "wf_id", wfID, "dir", stateDir)
		}
	}
	mu.Unlock()

	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("env apply %s failed: %v", specPath, err),
			err,
		)
	}

	return nil
}

// ── Destroy ──

// Destroy tears down all resources created by a previous env apply.
// The identifier can be either a workflow ID (short hash) or a path to
// a spec file. If it's a path, the workflow ID is derived from it.
func Destroy(
	ctx context.Context,
	op *api.Operation,
	specOrID string,
	onProgress event.OnProgressCallback,
) error {
	// Determine the workflow ID and state directory.
	wfID := ResolveWorkflowID(specOrID)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)

	// Read the saved workflow state.
	wfState, err := workflow.ReadWorkflowState(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return errs.NotFound(
				errs.CodeValidationFailed,
				fmt.Sprintf("no saved workflow state found for %q", specOrID),
			)
		}
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("read workflow state %s: %v", stateDir, err),
			err,
		)
	}

	// Reconstruct steps from saved resources.
	var steps []workflow.Step
	for _, res := range wfState.Resources {
		factory, ok := Registry[res.StepType]
		if !ok {
			slog.Warn("unknown step type in saved state, skipping", "type", res.StepType, "name", res.StepName)
			continue
		}
		// Extract the bare name from the full step name (format: "type:name").
		bareName := BareStepName(res.StepName, res.StepType)

		step, err := factory.FromState(factory.StepType, bareName, res.State, res.Dependencies, op)
		if err != nil {
			return errs.WrapMsg(
				errs.CodeInternal,
				fmt.Sprintf("reconstruct step %q: %v", res.StepName, err),
				err,
			)
		}
		steps = append(steps, step)
	}

	if len(steps) == 0 {
		return errs.New(errs.CodeValidationFailed, "no reconstructable steps in saved state")
	}

	// Build the pipeline and destroy.
	pipeline, err := workflow.NewPipeline(steps)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("build destroy pipeline for %s: %v", specOrID, err),
			err,
		)
	}

	progressFn := toPipelineProgress(onProgress)

	if err := pipeline.Destroy(ctx, wfState.Resources, progressFn); err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("env destroy %s failed: %v", specOrID, err),
			err,
		)
	}

	// Remove the workflow state.
	if err := workflow.RemoveWorkflowState(wfID); err != nil {
		slog.Warn("failed to remove workflow state", "wf_id", wfID, "error", err)
	}

	return nil
}

// ── List ──

// ListSummary is a summary of a saved workflow.
type ListSummary struct {
	WorkflowID string `json:"workflow_id"`
	SpecPath   string `json:"spec_path"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Resources  int    `json:"resources"`
}

// List lists all saved workflow states.
func List(ctx context.Context) ([]ListSummary, error) {
	statesDir := infra.GetWorkflowsStateDir()
	entries, err := os.ReadDir(statesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ListSummary{}, nil
		}
		return nil, errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("list env states: %v", err),
			err,
		)
	}

	var summaries []ListSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wfID := entry.Name()
		wfState, err := workflow.ReadWorkflowState(filepath.Join(statesDir, wfID))
		if err != nil {
			slog.Warn("skipping unreadable workflow state", "id", wfID, "error", err)
			continue
		}
		summary := ListSummary{
			WorkflowID: wfID,
			SpecPath:   wfState.SpecPath,
			CreatedAt:  wfState.CreatedAt,
			UpdatedAt:  wfState.UpdatedAt,
			Resources:  len(wfState.Resources),
		}
		summaries = append(summaries, summary)
	}

	_ = ctx
	return summaries, nil
}

// ── Helpers ──

// toPipelineProgress bridges event.OnProgressCallback to the pipeline's
// func(phase, status, msg string) callback.
func toPipelineProgress(onProgress event.OnProgressCallback) func(string, string, string) {
	if onProgress == nil {
		return nil
	}
	return func(phase, status, msg string) {
		onProgress(event.Progress{
			Phase:   phase,
			Status:  status,
			Message: msg,
		})
	}
}

// ResolveWorkflowID returns the workflow ID for a given spec path or ID.
// If the input looks like a file path (contains / or .), it's treated as
// a spec path and hashed. Otherwise it tries exact match, then prefix match
// against existing workflow state directories.
func ResolveWorkflowID(specOrID string) string {
	if strings.Contains(specOrID, "/") || strings.Contains(specOrID, "\\") || strings.Contains(specOrID, ".") {
		return crypto.WorkflowID(specOrID)
	}

	statesDir := infra.GetWorkflowsStateDir()
	// Try exact match first
	if _, err := os.Stat(filepath.Join(statesDir, specOrID)); err == nil {
		return specOrID
	}
	// Prefix match: scan directories for one starting with the given prefix
	entries, err := os.ReadDir(statesDir)
	if err != nil {
		return specOrID
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), specOrID) {
			return entry.Name()
		}
	}
	return specOrID
}

// BareStepName extracts the resource name from the full step name.
// For steps named "type:name", returns "name".
func BareStepName(stepName, stepType string) string {
	prefix := stepType + ":"
	if after, found := strings.CutPrefix(stepName, prefix); found && after != "" {
		return after
	}
	return stepName
}

// ── Diff ──

// DiffResult holds the result of comparing a spec against a saved workflow state.
type DiffResult struct {
	// New contains step names present in the spec but not in the state.
	New []string `json:"new"`
	// Removed contains step names present in the state but not in the spec.
	Removed []string `json:"removed"`
	// Existing contains step names present in both the spec and the state.
	Existing []string `json:"existing"`
}

// Diff compares the step names from a resolved spec against the step names
// from a saved workflow state and returns the set differences.
// If stateDir is empty, all spec steps are considered new.
func Diff(ctx context.Context, specPath string, stateDir string) (*DiffResult, error) {
	// Resolve spec step names.
	steps, err := ResolveSpec(ctx, specPath, nil)
	if err != nil {
		return nil, err
	}

	specNames := make(map[string]struct{}, len(steps))
	for _, s := range steps {
		specNames[s.Name()] = struct{}{}
	}

	// Read state step names.
	stateNames := make(map[string]struct{})
	if stateDir != "" {
		wfState, rErr := workflow.ReadWorkflowState(stateDir)
		if rErr == nil && wfState != nil {
			for _, res := range wfState.Resources {
				stateNames[res.StepName] = struct{}{}
			}
		}
	}

	result := &DiffResult{}
	for name := range specNames {
		if _, inState := stateNames[name]; inState {
			result.Existing = append(result.Existing, name)
		} else {
			result.New = append(result.New, name)
		}
	}
	for name := range stateNames {
		if _, inSpec := specNames[name]; !inSpec {
			result.Removed = append(result.Removed, name)
		}
	}

	// Sort for deterministic output.
	sort.Strings(result.New)
	sort.Strings(result.Removed)
	sort.Strings(result.Existing)

	return result, nil
}
