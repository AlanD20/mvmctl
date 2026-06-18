// Package env_test black-box tests the env.Diff() and env.List() functions.
package env_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	envpkg "mvmctl/internal/workflow/env"
)

// --- Diff Tests ---

// Rationale: Diff must identify resources present in the spec but not in the
// saved state. When no state exists, all spec steps are New.
func TestDiff_NewResources(t *testing.T) {
	specContent := `version: "1"
network:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"network:my-net"}, result.New); diff != "" {
		t.Errorf("New mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, result.Removed)
	assert.Empty(t, result.Existing)
	assert.Empty(t, result.Drifted)
}

// Rationale: Diff must identify resources present in the saved state but not
// in the spec. These are reported as Removed.
func TestDiff_RemovedResources(t *testing.T) {
	specContent := `version: "1"
`
	specPath := writeSpec(t, specContent)
	createWorkflowState(t, specPath, []model.AppliedResource{
		{
			Name: "network:my-net",
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: "abc123"},
			},
		},
	})

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	assert.Empty(t, result.New)
	if diff := cmp.Diff([]string{"network:my-net"}, result.Removed); diff != "" {
		t.Errorf("Removed mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, result.Existing)
	assert.Empty(t, result.Drifted)
}

// Rationale: Diff must report resources as Existing when the spec hash matches
// the saved state hash.
func TestDiff_ExistingUnchanged(t *testing.T) {
	specContent := `version: "1"
network:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	// Resolve spec to get the actual hash produced by FromSpec.
	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	specHash := steps[0].SpecHash()
	stepName := steps[0].Name()

	createWorkflowState(t, specPath, []model.AppliedResource{
		{
			Name: stepName,
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: specHash},
			},
		},
	})

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	assert.Empty(t, result.New)
	assert.Empty(t, result.Removed)
	if diff := cmp.Diff([]string{stepName}, result.Existing); diff != "" {
		t.Errorf("Existing mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, result.Drifted)
}

// Rationale: Diff must report resources as Drifted when the spec hash differs
// from the saved state hash (both non-empty).
func TestDiff_DriftedResources(t *testing.T) {
	specContent := `version: "1"
network:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	stepName := steps[0].Name()

	createWorkflowState(t, specPath, []model.AppliedResource{
		{
			Name: stepName,
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: "different-hash-value"},
			},
		},
	})

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	assert.Empty(t, result.New)
	assert.Empty(t, result.Removed)
	assert.Empty(t, result.Existing)
	if diff := cmp.Diff([]string{stepName}, result.Drifted); diff != "" {
		t.Errorf("Drifted mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: An empty spec (only version, no resources) with no saved state
// must produce an empty DiffResult with no errors.
func TestDiff_EmptySpecNoState(t *testing.T) {
	specContent := `version: "1"
`
	specPath := writeSpec(t, specContent)

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	assert.Empty(t, result.New)
	assert.Empty(t, result.Removed)
	assert.Empty(t, result.Existing)
	assert.Empty(t, result.Drifted)
}

// Rationale: When the saved state's SpecHash is empty, a matching spec step
// must be reported as Existing, not Drifted. Drift requires both hashes to
// be non-empty and different.
func TestDiff_StateSpecHashEmpty(t *testing.T) {
	specContent := `version: "1"
network:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	stepName := steps[0].Name()

	// Saved state has empty SpecHash — should be Existing, not Drifted.
	createWorkflowState(t, specPath, []model.AppliedResource{
		{
			Name: stepName,
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: ""},
			},
		},
	})

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	assert.Empty(t, result.New)
	assert.Empty(t, result.Removed)
	if diff := cmp.Diff([]string{stepName}, result.Existing); diff != "" {
		t.Errorf("Existing mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, result.Drifted)
}

// Rationale: Diff must correctly report all four categories (New, Removed,
// Existing, Drifted) when the spec and state differ in multiple ways.
func TestDiff_MixedResult(t *testing.T) {
	specContent := `version: "1"
network:
  - name: net-existing
    subnet: 10.0.0.0/24
key:
  - name: key-drifted
    algorithm: ed25519
image:
  - name: img-new
    type: alpine
    version: "3.21"
`
	specPath := writeSpec(t, specContent)

	// Resolve spec to get actual hashes.
	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)

	specHashes := make(map[string]string, len(steps))
	for _, s := range steps {
		specHashes[s.Name()] = s.SpecHash()
	}

	// State has:
	//   network:net-existing → Existing (matching hash)
	//   key:key-drifted      → Drifted (different hash)
	//   binary:firecracker   → Removed (not in spec)
	createWorkflowState(t, specPath, []model.AppliedResource{
		{
			Name: "network:net-existing",
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: specHashes["network:net-existing"]},
			},
		},
		{
			Name: "key:key-drifted",
			Type: "key",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: "drifted-hash"},
			},
		},
		{
			Name: "binary:firecracker",
			Type: "binary",
			State: model.ResourceState{
				Meta: model.ResourceMeta{SpecHash: "removed-hash"},
			},
		},
	})

	result, err := envpkg.Diff(context.Background(), specPath)
	require.NoError(t, err)
	require.NotNil(t, result)

	if diff := cmp.Diff([]string{"image:img-new"}, result.New); diff != "" {
		t.Errorf("New mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"binary:firecracker"}, result.Removed); diff != "" {
		t.Errorf("Removed mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"network:net-existing"}, result.Existing); diff != "" {
		t.Errorf("Existing mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"key:key-drifted"}, result.Drifted); diff != "" {
		t.Errorf("Drifted mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Diff must return an error when the spec file does not exist.
func TestDiff_SpecFileNotFound(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "nonexistent.yaml")

	_, err := envpkg.Diff(context.Background(), specPath)
	require.Error(t, err)
}

// Rationale: Diff must honour context cancellation. ResolveSpec checks ctx.Err()
// before file I/O, so a cancelled context must propagate through Diff.
func TestDiff_ContextCancelled(t *testing.T) {
	specContent := `version: "1"
network:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := envpkg.Diff(ctx, specPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- List Tests ---

// Rationale: List must return a non-nil slice without error even when the
// workflow states directory is empty or contains only unrelated entries.
// The function must not panic or return an error for an empty result.
func TestList_EmptyResult(t *testing.T) {
	result, err := envpkg.List(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)
}

// Rationale: List must return one summary with correct fields when a single
// workflow state exists.
func TestList_OneState(t *testing.T) {
	specPath := "/tmp/env-test-spec-a.yaml"
	createdAt := infra.Now()
	createListState(t, "aaaaaaaaaaaaaaaa", specPath, 3, createdAt)

	result, err := envpkg.List(context.Background())
	require.NoError(t, err)

	// Find our entry (in case other test artifacts exist).
	var found bool
	for _, s := range result {
		if s.WorkflowID == "aaaaaaaaaaaaaaaa" {
			found = true
			assert.Equal(t, specPath, s.SpecPath)
			assert.Equal(t, createdAt, s.CreatedAt)
			assert.Equal(t, 3, s.Resources)
			break
		}
	}
	assert.True(t, found, "expected workflow aaaaaaaaaaaaaaaa in list results")
}

// Rationale: List must return summaries for all workflow states. The results
// are sorted by directory name (os.ReadDir sorts by name).
func TestList_MultipleStates(t *testing.T) {
	createListState(t, "bbbbbbbbbbbbbbbb", "/tmp/env-test-spec-b.yaml", 1, infra.Now())
	createListState(t, "aaaaaaaaaaaaaaaa", "/tmp/env-test-spec-a.yaml", 2, infra.Now())

	result, err := envpkg.List(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result), 2)

	// Find both entries and check ordering.
	var foundA, foundB bool
	for _, s := range result {
		if s.WorkflowID == "aaaaaaaaaaaaaaaa" {
			foundA = true
			assert.Equal(t, 2, s.Resources)
		}
		if s.WorkflowID == "bbbbbbbbbbbbbbbb" {
			foundB = true
			assert.Equal(t, 1, s.Resources)
		}
	}
	assert.True(t, foundA, "expected workflow aaaaaaaaaaaaaaaa in list results")
	assert.True(t, foundB, "expected workflow bbbbbbbbbbbbbbbb in list results")
}

// Rationale: List must skip non-directory entries in the workflow states
// directory and only return directory-based state summaries.
func TestList_IgnoresNonDirEntries(t *testing.T) {
	statesDir := infra.GetWorkflowsStateDir()

	// Create a file (not a directory) in the states dir.
	filePath := filepath.Join(statesDir, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("garbage"), 0644))
	t.Cleanup(func() { os.Remove(filePath) })

	createListState(t, "cccccccccccccccc", "/tmp/env-test-spec-c.yaml", 1, infra.Now())

	result, err := envpkg.List(context.Background())
	require.NoError(t, err)

	var found bool
	for _, s := range result {
		if s.WorkflowID == "cccccccccccccccc" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected workflow cccccccccccccccc in list results")
}

// Rationale: List must honour context cancellation and return ctx.Err() when
// the context is cancelled during iteration over state directories.
func TestList_ContextCancelled(t *testing.T) {
	createListState(t, "dddddddddddddddd", "/tmp/env-test-spec-d.yaml", 1, infra.Now())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := envpkg.List(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- Helpers ---

// createWorkflowState creates a workflow state dir for the given spec path
// and resources. The specPath is used to derive the workflow ID via
// crypto.WorkflowID. The state dir is registered for cleanup via t.Cleanup.
func createWorkflowState(t *testing.T, specPath string, resources []model.AppliedResource) {
	t.Helper()
	wfID := crypto.WorkflowID(specPath)
	stateDir := infra.GetWorkflowsStateDirByID(wfID)

	// Remove any pre-existing state dir for this wfID.
	os.RemoveAll(stateDir)
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	state := &model.WorkflowState{
		WorkflowID:    wfID,
		SpecPath:      specPath,
		SchemaVersion: "1.0",
		CreatedAt:     infra.Now(),
		UpdatedAt:     infra.Now(),
		Resources:     resources,
	}
	require.NoError(t, workflow.WriteWorkflowState(stateDir, state))
}

// createListState creates a workflow state dir with the given wfID and
// resourceCount dummy resources. The state dir is registered for cleanup
// via t.Cleanup.
func createListState(t *testing.T, wfID, specPath string, resourceCount int, createdAt string) {
	t.Helper()
	stateDir := infra.GetWorkflowsStateDirByID(wfID)

	// Remove any pre-existing state dir for this wfID.
	os.RemoveAll(stateDir)
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	resources := make([]model.AppliedResource, resourceCount)
	for i := 0; i < resourceCount; i++ {
		resources[i] = model.AppliedResource{
			Name: fmt.Sprintf("network:net-%d", i),
			Type: "network",
			State: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
		}
	}

	state := &model.WorkflowState{
		WorkflowID:    wfID,
		SpecPath:      specPath,
		SchemaVersion: "1.0",
		CreatedAt:     createdAt,
		UpdatedAt:     infra.Now(),
		Resources:     resources,
	}
	require.NoError(t, workflow.WriteWorkflowState(stateDir, state))
}
