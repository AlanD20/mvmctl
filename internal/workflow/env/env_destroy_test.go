package env

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
)

// NOTE: Tests in this file mutate the package-level Registry. Do NOT add t.Parallel().

// --- Helpers ---

// replaceAndRestoreEntry replaces a Registry entry and registers cleanup to
// restore its original value. If the key did not exist before (zero value),
// it is deleted on cleanup to avoid adding unexpected entries to the map.
func replaceAndRestoreEntry(t *testing.T, key string, factory StepFactory) {
	t.Helper()
	orig, exists := Registry[key]
	Registry[key] = factory
	t.Cleanup(func() {
		if exists {
			Registry[key] = orig
		} else {
			delete(Registry, key)
		}
	})
}

// noopDestroyFactory returns a StepFactory whose FromState creates a no-op step
// that always succeeds on Destroy. FromSpec returns an error since it should
// never be called during destroy operations.
func noopDestroyFactory(stepType string) StepFactory {
	return StepFactory{
		StepType: stepType,
		FromSpec: func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
			return nil, errors.New("FromSpec should not be called during Destroy")
		},
		FromState: func(stepType, name string, saved model.ResourceState, deps []string, op api.API) (workflow.Step, error) {
			stepName := FormatStepName(stepType, name)
			return workflow.NewStepFunc(
				stepType,
				stepName,
				deps,
				nil,
				func(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return nil
				},
				func(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					if write != nil {
						return write(ctx, model.ResourceState{})
					}
					return nil
				},
				func() model.ResourceState { return model.ResourceState{} },
			), nil
		},
	}
}

// errDestroyFactory returns a StepFactory whose Destroy always returns the given error.
func errDestroyFactory(stepType string, destroyErr error) StepFactory {
	return StepFactory{
		StepType: stepType,
		FromSpec: func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
			return nil, errors.New("FromSpec should not be called during Destroy")
		},
		FromState: func(stepType, name string, saved model.ResourceState, deps []string, op api.API) (workflow.Step, error) {
			stepName := FormatStepName(stepType, name)
			return workflow.NewStepFunc(
				stepType,
				stepName,
				deps,
				nil,
				func(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return nil
				},
				func(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					if write != nil {
						write(ctx, model.ResourceState{})
					}
					return destroyErr
				},
				func() model.ResourceState { return model.ResourceState{} },
			), nil
		},
	}
}

// writeDestroyState creates a workflow state directory with the given resources
// and registers cleanup via t.Cleanup so the directory is removed after the
// test completes regardless of pass/fail.
func writeDestroyState(t *testing.T, wfID string, resources []model.AppliedResource) string {
	t.Helper()
	stateDir := infra.GetWorkflowsStateDirByID(wfID)
	state := &model.WorkflowState{
		WorkflowID:    wfID,
		SpecPath:      "/tmp/test-destroy-spec.yaml",
		SchemaVersion: "1.0",
		CreatedAt:     "2025-01-01T00:00:00Z",
		UpdatedAt:     "2025-01-01T00:00:00Z",
		Resources:     resources,
	}
	require.NoError(t, workflow.WriteWorkflowState(stateDir, state))
	t.Cleanup(func() { os.RemoveAll(stateDir) })
	return stateDir
}

// --- Tests ---

// Rationale: A saved state with one resource must be destroyed without error.
// After successful destroy, the workflow state directory must be removed.
func TestDestroy_Success(t *testing.T) {
	replaceAndRestoreEntry(t, "test", noopDestroyFactory("test"))

	wfID := "destroy-success-test"
	stateDir := writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "test:my-resource", Type: "test", State: model.ResourceState{}},
	})

	err := Destroy(context.Background(), nil, wfID, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(stateDir)
	assert.True(t, os.IsNotExist(statErr), "state dir should be removed after destroy")
}

// Rationale: A saved state with multiple independent resources must destroy all
// of them and remove the state directory.
func TestDestroy_MultipleSteps(t *testing.T) {
	replaceAndRestoreEntry(t, "test", noopDestroyFactory("test"))

	wfID := "destroy-multiple-test"
	stateDir := writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "test:resource-a", Type: "test", State: model.ResourceState{}},
		{Name: "test:resource-b", Type: "test", State: model.ResourceState{}},
	})

	err := Destroy(context.Background(), nil, wfID, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(stateDir)
	assert.True(t, os.IsNotExist(statErr), "state dir should be removed after all destroys")
}

// Rationale: When a step's Destroy fails, the error propagates wrapped with
// "env destroy". The state directory must still exist so the user can retry
// the remaining resources.
func TestDestroy_StepFails(t *testing.T) {
	replaceAndRestoreEntry(t, "test", errDestroyFactory("test", errors.New("destroy failed")))

	wfID := "destroy-fail-test"
	stateDir := writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "test:resource-a", Type: "test", State: model.ResourceState{}},
	})

	err := Destroy(context.Background(), nil, wfID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env destroy")

	_, statErr := os.Stat(stateDir)
	assert.NoError(t, statErr, "state dir should exist after failed destroy")
}

// Rationale: A nonexistent workflow ID must produce a "no saved workflow state
// found" error message to help the user understand the workflow must be applied
// before it can be destroyed.
func TestDestroy_NotFound(t *testing.T) {
	err := Destroy(context.Background(), nil, "nonexistent-wf", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no saved workflow state found")
}

// Rationale: An unknown step type in the saved state must be skipped with a
// warning. The remaining known step must still be destroyed and the state
// directory removed.
func TestDestroy_UnknownStepType(t *testing.T) {
	replaceAndRestoreEntry(t, "known", noopDestroyFactory("known"))

	wfID := "destroy-unknown-test"
	stateDir := writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "known:resource-a", Type: "known", State: model.ResourceState{}},
		{Name: "unknown:resource-b", Type: "unknown_type", State: model.ResourceState{}},
	})

	err := Destroy(context.Background(), nil, wfID, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(stateDir)
	assert.True(t, os.IsNotExist(statErr), "state dir should be removed after destroying known step")
}

// Rationale: When all steps in the saved state have types not present in the
// Registry, Destroy must return "no reconstructable steps in saved state"
// because there is nothing it can destroy.
func TestDestroy_NoReconstructableSteps(t *testing.T) {
	wfID := "destroy-no-reconstruct-test"
	_ = writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "unknown:resource-a", Type: "unknown_type", State: model.ResourceState{}},
		{Name: "unknown:resource-b", Type: "other_unknown", State: model.ResourceState{}},
	})

	err := Destroy(context.Background(), nil, wfID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reconstructable steps in saved state")
}

// Rationale: A pre-cancelled context causes pipeline.Destroy to return the
// context error immediately, and Destroy wraps it with "env destroy". The
// state directory must remain intact since no work was done.
func TestDestroy_ContextCancellation(t *testing.T) {
	replaceAndRestoreEntry(t, "test", noopDestroyFactory("test"))

	wfID := "destroy-cancel-test"
	_ = writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "test:resource-a", Type: "test", State: model.ResourceState{}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Destroy(ctx, nil, wfID, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "env destroy")
}

// Rationale: When destroying two dependent steps in reverse topological order
// (step-b depends on step-a, so step-b is destroyed first), the intermediate
// state file after step-b's destroy must contain only step-a. This verifies
// that the onStepComplete callback correctly removes each destroyed resource
// from the accumulated resources list before persisting the updated state.
// We synchronise with step-a so it does not write its state until after the
// test has verified the intermediate state.
func TestDestroy_VerifyStateFileAfterSingleDestroy(t *testing.T) {
	stepBDestroyed := make(chan struct{})
	continueDestroy := make(chan struct{})

	replaceAndRestoreEntry(t, "test", StepFactory{
		StepType: "test",
		FromSpec: func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
			return nil, errors.New("FromSpec should not be called during Destroy")
		},
		FromState: func(stepType, name string, saved model.ResourceState, deps []string, op api.API) (workflow.Step, error) {
			stepName := FormatStepName(stepType, name)
			return workflow.NewStepFunc(
				stepType,
				stepName,
				deps,
				nil,
				func(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return nil
				},
				func(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					// step-b destroys first (reverse topo), writes state, then signals.
					if stepName == "test:step-b" {
						if write != nil {
							if err := write(ctx, model.ResourceState{}); err != nil {
								return err
							}
						}
						close(stepBDestroyed)
						return nil
					}

					// step-a destroys second — wait for verification before writing.
					if stepName == "test:step-a" {
						select {
						case <-continueDestroy:
						case <-ctx.Done():
							return ctx.Err()
						}
						if write != nil {
							if err := write(ctx, model.ResourceState{}); err != nil {
								return err
							}
						}
					}
					return nil
				},
				func() model.ResourceState { return model.ResourceState{} },
			), nil
		},
	})

	wfID := "destroy-intermediate-test"
	stateDir := writeDestroyState(t, wfID, []model.AppliedResource{
		{Name: "test:step-a", Type: "test", State: model.ResourceState{}},
		{Name: "test:step-b", Type: "test", Dependencies: []string{"test:step-a"}, State: model.ResourceState{}},
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- Destroy(context.Background(), nil, wfID, nil)
	}()

	// Wait for step-b to finish its destroy (which updates the state file).
	select {
	case <-stepBDestroyed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for step-b to be destroyed")
	}

	// The intermediate state file should contain only step-a.
	state, err := workflow.ReadWorkflowState(stateDir)
	require.NoError(t, err, "should read intermediate state file after step-b destroy")
	require.Len(t, state.Resources, 1, "should have one remaining resource after step-b destroy")
	assert.Equal(t, "test:step-a", state.Resources[0].Name)
	assert.Equal(t, "test", state.Resources[0].Type)

	// Signal step-a to complete its destroy and write the updated state.
	close(continueDestroy)

	// Wait for Destroy to complete (step-a destroy + state dir removal).
	err = <-errCh
	require.NoError(t, err)

	_, statErr := os.Stat(stateDir)
	assert.True(t, os.IsNotExist(statErr), "state dir should be removed after all destroys")
}
