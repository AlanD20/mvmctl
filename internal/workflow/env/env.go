package env

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// --- Constants ---

const envStateSchemaVersion = "1.0"

// --- Apply ---

// Apply reads a YAML spec file, resolves it into a DAG of provisioning
// steps, and executes them in topological order. The result is persisted as
// a workflow state file in the cache directory.
//
// State data is collected per-step during execution via an
// onStepComplete callback, so even if execution fails partway through,
// the state from completed steps is persisted.
func Apply(
	ctx context.Context,
	op api.API,
	specPath string,
	onProgress event.OnProgressCallback,
	extraEnv map[string]string,
) error {
	// Resolve YAML spec into steps.
	spec, steps, err := ResolveSpec(ctx, specPath, op)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("resolve env spec %s: %v", specPath, err),
			err,
		)
	}

	// Merge --env variables into exec steps. CLI values take precedence
	// over any env: declared in the spec itself.
	for i := range steps {
		if es, ok := steps[i].(*ExecStep); ok {
			es.MergeEnv(extraEnv)
		}
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
	var prevResources []model.AppliedResource
	prevState, rErr := workflow.ReadWorkflowState(stateDir)
	if rErr == nil && prevState != nil {
		prevResources = prevState.Resources
	}

	// Register a callback that persists state after each step's Apply.
	// Each step writes its own state immediately after a successful API call.
	var (
		mu        sync.Mutex
		resources []model.AppliedResource
		createdAt string
	)

	onStepComplete := func(ctx context.Context, step workflow.Step, stateData model.ResourceState) error {
		mu.Lock()
		defer mu.Unlock()
		if createdAt == "" {
			createdAt = infra.Now()
		}
		resources = append(resources, model.AppliedResource{
			Name:         step.Name(),
			Type:         step.Type(),
			Dependencies: step.Dependencies(),
			State:        stateData,
		})
		wfState := &model.WorkflowState{
			WorkflowID:    wfID,
			SpecPath:      specPath,
			SchemaVersion: envStateSchemaVersion,
			CreatedAt:     createdAt,
			UpdatedAt:     infra.Now(),
			Resources:     resources,
		}
		if pErr := workflow.WriteWorkflowState(stateDir, wfState); pErr != nil {
			slog.Debug("failed to persist workflow state", "wf_id", wfID, "step", step.Name(), "error", pErr)
			return fmt.Errorf("persist workflow state after step %q: %w", step.Name(), pErr)
	}

	// Ephemeral: destroy everything and remove state after successful apply.
	if spec.Ephemeral {
		slog.Info("ephemeral spec — destroying resources after successful apply", "spec", specPath)
		if err := Destroy(ctx, op, specPath, onProgress); err != nil {
			return errs.WrapMsg(
				errs.CodeInternal,
				fmt.Sprintf("ephemeral destroy failed after apply: %v", err),
				err,
			)
		}
		slog.Info("ephemeral spec destroyed", "spec", specPath)
	}

	return nil
}

	err = pipeline.Execute(ctx, state, onProgress, prevResources, workflow.WithOnStepComplete(onStepComplete))

	if err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("env apply %s failed: %v", specPath, err),
			err,
		)
	}

	// Handle step removals — destroy resources listed in removes.
	for _, step := range steps {
		for _, target := range step.Removes() {
			stepType := InferStepType(target)
			name := BareStepName(target, stepType)
			switch stepType {
			case "vm":
				result := op.VMRemove(ctx, inputs.VMInput{Identifiers: []string{name}})
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "image", "image_import":
				result := op.ImageRemove(ctx, inputs.ImageInput{Identifiers: []string{name}}, true)
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "network":
				if err := op.NetworkRemove(
					ctx,
					inputs.NetworkInput{Identifiers: []string{name}, Force: true},
					true,
				); err != nil {
					slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", err)
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "volume":
				result := op.VolumeRemove(ctx, inputs.VolumeInput{Identifiers: []string{name}}, true)
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "key":
				result := op.KeyRemove(ctx, inputs.KeyInput{Identifiers: []string{name}}, true)
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "kernel":
				result := op.KernelRemove(ctx, inputs.KernelInput{Identifiers: []string{name}})
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "binary":
				result := op.BinaryRemove(ctx, inputs.BinaryInput{Identifiers: []string{name}}, true)
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			case "snapshot":
				result := op.SnapshotRemove(ctx, inputs.SnapshotInput{Identifiers: []string{name}})
				if result.HasErrors() {
					for _, r := range result.Errors() {
						if r.ToError() != nil {
							slog.Warn("cleanup remove failed", "type", stepType, "target", target, "error", r.ToError())
						}
					}
				} else {
					slog.Info("resource removed after step", "step", step.Name(), "target", target)
				}
			default:
				slog.Warn("unsupported resource type for removal in env workflow", "type", stepType, "target", target)
			}
		}
	}

	return nil
}

// --- Destroy ---

// Destroy tears down all resources created by a previous env apply.
// The identifier can be either a workflow ID (short hash) or a path to
// a spec file. If it's a path, the workflow ID is derived from it.
func Destroy(
	ctx context.Context,
	op api.API,
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
		factory, ok := Registry[res.Type]
		if !ok {
			slog.Debug("unknown step type in saved state, skipping", "type", res.Type, "name", res.Name)
			continue
		}
		// Extract the bare name from the full step name (format: "type:name").
		bareName := BareStepName(res.Name, res.Type)

		step, err := factory.FromState(factory.StepType, bareName, res.State, res.Dependencies, op)
		if err != nil {
			return errs.WrapMsg(
				errs.CodeInternal,
				fmt.Sprintf("reconstruct step %q: %v", res.Name, err),
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

	// Register a callback that persists state after each step's Destroy.
	// Each step removes itself from the accumulated resources list after a
	// successful destroy, so re-running destroy picks up from remaining steps.
	var (
		mu        sync.Mutex
		resources = wfState.Resources // copy the full list from saved state
	)

	onStepComplete := func(ctx context.Context, step workflow.Step, stateData model.ResourceState) error {
		mu.Lock()
		defer mu.Unlock()

		// Remove this step from the accumulated resources.
		filtered := make([]model.AppliedResource, 0, len(resources))
		for _, r := range resources {
			if r.Name != step.Name() {
				filtered = append(filtered, r)
			}
		}
		resources = filtered

		updatedState := &model.WorkflowState{
			WorkflowID:    wfID,
			SpecPath:      wfState.SpecPath,
			SchemaVersion: envStateSchemaVersion,
			CreatedAt:     wfState.CreatedAt,
			UpdatedAt:     infra.Now(),
			Resources:     resources,
		}
		if pErr := workflow.WriteWorkflowState(stateDir, updatedState); pErr != nil {
			slog.Debug(
				"failed to persist workflow state after destroy",
				"wf_id",
				wfID,
				"step",
				step.Name(),
				"error",
				pErr,
			)
			return fmt.Errorf("persist workflow state after destroy step %q: %w", step.Name(), pErr)
		}
		slog.Debug("workflow state persisted after destroy", "wf_id", wfID, "step", step.Name(), "dir", stateDir)
		return nil
	}

	if err := pipeline.Destroy(
		ctx,
		wfState.Resources,
		onProgress,
		workflow.WithDestroyOnStepComplete(onStepComplete),
	); err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("env destroy %s failed: %v", specOrID, err),
			err,
		)
	}

	// Remove the workflow state after all destroys succeed.
	if err := workflow.RemoveWorkflowState(wfID); err != nil {
		slog.Debug("failed to remove workflow state", "wf_id", wfID, "error", err)
	}

	return nil
}

// --- List ---

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

	summaries := make([]ListSummary, 0)
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !entry.IsDir() {
			continue
		}
		wfID := entry.Name()
		wfState, err := workflow.ReadWorkflowState(filepath.Join(statesDir, wfID))
		if err != nil {
			slog.Debug("skipping unreadable workflow state", "id", wfID, "error", err)
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

	return summaries, nil
}

// --- Diff ---

// DiffResult holds the result of comparing a spec against a saved workflow state.
type DiffResult struct {
	// New contains step names present in the spec but not in the state.
	New []string `json:"new"`
	// Removed contains step names present in the state but not in the spec.
	Removed []string `json:"removed"`
	// Existing contains step names present in both the spec and the state
	// with unchanged spec hashes.
	Existing []string `json:"existing"`
	// Drifted contains step names present in both the spec and the state
	// but with different spec hashes (the spec has changed since last apply).
	Drifted []string `json:"drifted"`
}

// Diff compares the step names from a resolved spec against the step names
// from a saved workflow state and returns the set differences.
// specOrID is either a spec file path or a workflow ID (supports prefix matching).
// Drift detection compares spec hashes: if a step exists in both spec and state
// but the spec hash has changed, it is marked as "drifted".
func Diff(ctx context.Context, specOrID string) (*DiffResult, error) {
	// Determine spec path and state directory from the input.
	specPath, stateDir := resolveDiffInput(specOrID)

	// Resolve spec step names and hashes.
	_, steps, err := ResolveSpec(ctx, specPath, nil)
	if err != nil {
		return nil, err
	}

	type specEntry struct {
		hash string
	}
	specByName := make(map[string]specEntry, len(steps))
	for _, s := range steps {
		specByName[s.Name()] = specEntry{hash: s.SpecHash()}
	}

	// Read state step names and hashes from ResourceState.Meta.SpecHash.
	type stateEntry struct {
		hash string
	}
	stateByName := make(map[string]stateEntry)
	if stateDir != "" {
		wfState, rErr := workflow.ReadWorkflowState(stateDir)
		if rErr == nil && wfState != nil {
			for _, res := range wfState.Resources {
				stateByName[res.Name] = stateEntry{hash: res.State.Meta.SpecHash}
			}
		}
	}

	result := &DiffResult{}
	for name, spec := range specByName {
		if st, inState := stateByName[name]; inState {
			if spec.hash != "" && st.hash != "" && spec.hash != st.hash {
				result.Drifted = append(result.Drifted, name)
			} else {
				result.Existing = append(result.Existing, name)
			}
		} else {
			result.New = append(result.New, name)
		}
	}
	for name := range stateByName {
		if _, inSpec := specByName[name]; !inSpec {
			result.Removed = append(result.Removed, name)
		}
	}

	// Sort for deterministic output.
	sort.Strings(result.New)
	sort.Strings(result.Removed)
	sort.Strings(result.Existing)
	sort.Strings(result.Drifted)

	return result, nil
}
