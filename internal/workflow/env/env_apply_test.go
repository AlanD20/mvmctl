package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"
)

// NOTE: Tests in this file mutate the package-level Registry. Do NOT add t.Parallel().

// --- Helpers ---

// writeApplySpec writes YAML content to a temp file and returns the absolute path.
func writeApplySpec(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(content), 0644))
	return specPath
}

// noopFactory returns a StepFactory that creates a no-op step whose Apply calls
// write to trigger state persistence. This lets tests verify the state file
// output without performing real provisioning.
func noopFactory(stepType string) StepFactory {
	return StepFactory{
		StepType: stepType,
		FromSpec: func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
			stepName := FormatStepName(stepType, name)
			return workflow.NewStepFunc(
				stepType,
				stepName,
				nil,
				nil,
				func(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					if write != nil {
						return write(ctx, model.ResourceState{
							Spec: spec,
							Meta: model.ResourceMeta{WasCreated: true},
						})
					}
					return nil
				},
				func(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return nil
				},
				func() model.ResourceState {
					return model.ResourceState{Meta: model.ResourceMeta{WasCreated: true}}
				},
			), nil
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
					return nil
				},
				func() model.ResourceState {
					return model.ResourceState{}
				},
			), nil
		},
	}
}

// errFactory returns a StepFactory whose Apply always returns the given error.
// No state write is performed before the error.
func errFactory(stepType string, applyErr error) StepFactory {
	return StepFactory{
		StepType: stepType,
		FromSpec: func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
			stepName := FormatStepName(stepType, name)
			return workflow.NewStepFunc(
				stepType,
				stepName,
				nil,
				nil,
				func(ctx context.Context, state *workflow.SharedState, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return applyErr
				},
				func(ctx context.Context, saved model.ResourceState, write workflow.StateWriter, onProgress event.OnProgressCallback) error {
					return nil
				},
				func() model.ResourceState {
					return model.ResourceState{}
				},
			), nil
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
					return nil
				},
				func() model.ResourceState {
					return model.ResourceState{}
				},
			), nil
		},
	}
}

// replaceAndRestore replaces a Registry entry and registers automatic
// restoration via t.Cleanup.
func replaceAndRestore(t *testing.T, key string, factory StepFactory) {
	t.Helper()
	orig := Registry[key]
	Registry[key] = factory
	t.Cleanup(func() { Registry[key] = orig })
}

// readStateFile reads the WorkflowState persisted by Apply for the given spec path.
func readStateFile(t *testing.T, specPath string) *model.WorkflowState {
	t.Helper()
	wfID := crypto.WorkflowID(specPath)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)
	statePath := filepath.Join(stateDir, "state.yaml")
	require.FileExists(t, statePath, "state file should exist at %s", statePath)
	state, err := workflow.ReadWorkflowState(stateDir)
	require.NoError(t, err, "should read workflow state")
	return state
}

// cleanupState removes the workflow state directory for the given spec path.
func cleanupState(t *testing.T, specPath string) {
	t.Helper()
	wfID := crypto.WorkflowID(specPath)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)
	os.RemoveAll(stateDir)
}

// --- Tests ---

// Rationale: A valid YAML spec with one resource must produce a workflow state
// file containing the correct step metadata (name, type, schema version).
func TestApply_Success(t *testing.T) {
	replaceAndRestore(t, "network", noopFactory("network"))

	specContent := `version: "1"
network:
  - name: test-net
    subnet: 10.0.0.0/24
`
	specPath := writeApplySpec(t, specContent)
	defer cleanupState(t, specPath)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.NoError(t, err)

	state := readStateFile(t, specPath)
	assert.Equal(t, crypto.WorkflowID(specPath), state.WorkflowID)
	assert.Equal(t, specPath, state.SpecPath)
	assert.Equal(t, "1.0", state.SchemaVersion)
	require.Len(t, state.Resources, 1)
	assert.Equal(t, "network:test-net", state.Resources[0].Name)
	assert.Equal(t, "network", state.Resources[0].Type)
	assert.NotEmpty(t, state.CreatedAt)
	assert.NotEmpty(t, state.UpdatedAt)
}

// Rationale: A spec with two independent resource types must produce a state
// file with both resources, irrespective of execution order.
func TestApply_MultipleSteps(t *testing.T) {
	replaceAndRestore(t, "network", noopFactory("network"))
	replaceAndRestore(t, "key", noopFactory("key"))

	specContent := `version: "1"
network:
  - name: test-net
key:
  - name: my-key
`
	specPath := writeApplySpec(t, specContent)
	defer cleanupState(t, specPath)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.NoError(t, err)

	state := readStateFile(t, specPath)
	require.Len(t, state.Resources, 2)

	gotNames := []string{state.Resources[0].Name, state.Resources[1].Name}
	sort.Strings(gotNames)
	wantNames := []string{"key:my-key", "network:test-net"}
	sort.Strings(wantNames)
	if diff := cmp.Diff(wantNames, gotNames); diff != "" {
		t.Errorf("step names mismatch (-want +got):\n%s", diff)
	}

	gotTypes := []string{state.Resources[0].Type, state.Resources[1].Type}
	sort.Strings(gotTypes)
	wantTypes := []string{"key", "network"}
	sort.Strings(wantTypes)
	if diff := cmp.Diff(wantTypes, gotTypes); diff != "" {
		t.Errorf("step types mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A spec with version but no resource sections must return a
// "contains no resources" error, not succeed silently.
func TestApply_EmptySpec(t *testing.T) {
	specContent := `version: "1"`
	specPath := writeApplySpec(t, specContent)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env spec contains no resources")
}

// Rationale: A nonexistent spec path must produce an error from ResolveSpec
// that is wrapped with "resolve env spec".
func TestApply_SpecFileNotFound(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "nonexistent.yaml")

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve env spec")
	assert.Contains(t, err.Error(), "not found")
}

// Rationale: A spec file with invalid YAML must produce an error from
// ResolveSpec wrapped with "resolve env spec".
func TestApply_InvalidYAML(t *testing.T) {
	specPath := writeApplySpec(t, `version: "1"
network:
  - invalid_yaml: [`)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve env spec")
}

// Rationale: Calling Apply twice with the same spec must succeed both times.
// The second call detects the previous state file and handles it without error.
func TestApply_Reapply(t *testing.T) {
	replaceAndRestore(t, "network", noopFactory("network"))
	replaceAndRestore(t, "key", noopFactory("key"))

	specContent := `version: "1"
network:
  - name: test-net
key:
  - name: my-key
`
	specPath := writeApplySpec(t, specContent)
	defer cleanupState(t, specPath)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.NoError(t, err)

	err = Apply(context.Background(), nil, specPath, nil, nil)
	require.NoError(t, err)

	state := readStateFile(t, specPath)
	require.Len(t, state.Resources, 2)
	gotNames := []string{state.Resources[0].Name, state.Resources[1].Name}
	sort.Strings(gotNames)
	wantNames := []string{"key:my-key", "network:test-net"}
	sort.Strings(wantNames)
	if diff := cmp.Diff(wantNames, gotNames); diff != "" {
		t.Errorf("step names mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: When a step's Apply returns an error, the pipeline stops and
// propagates the error. It must be wrapped with "env apply". No state file
// should be persisted since the step failed before calling write.
func TestApply_StepFails(t *testing.T) {
	applyErr := errors.New("mock step failure")
	replaceAndRestore(t, "network", errFactory("network", applyErr))

	specContent := `version: "1"
network:
  - name: test-net
    subnet: 10.0.0.0/24
`
	specPath := writeApplySpec(t, specContent)
	defer cleanupState(t, specPath)

	err := Apply(context.Background(), nil, specPath, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env apply")
	assert.ErrorIs(t, err, applyErr)

	wfID := crypto.WorkflowID(specPath)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)
	statePath := filepath.Join(stateDir, "state.yaml")
	_, statErr := os.Stat(statePath)
	assert.True(t, os.IsNotExist(statErr), "state file should not exist after failed apply")
}

// Rationale: A cancelled context before Apply must return the context error
// immediately. The cancellation is caught at ResolveSpec (ctx.Err() check
// before file I/O), wrapping the error as "resolve env spec".
func TestApply_ContextCancellation(t *testing.T) {
	replaceAndRestore(t, "network", noopFactory("network"))

	specContent := `version: "1"
network:
  - name: test-net
    subnet: 10.0.0.0/24
`
	specPath := writeApplySpec(t, specContent)
	defer cleanupState(t, specPath)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Apply(ctx, nil, specPath, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	var de *errs.DomainError
	assert.True(t, errors.As(err, &de), "error should be a *errs.DomainError")
	assert.Contains(t, err.Error(), "resolve env spec")
}
