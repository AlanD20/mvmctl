// Package workflow white-box tests — tests unexported functions and has full
// access to internal DAG, pipeline, and state persistence implementation.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── mockStep ──────────────────────────────────────────────────────────────────
// A minimal Step implementation for testing DAG and pipeline behavior.

type mockStep struct {
	name        string
	stepType    string
	deps        []string
	applyFn     func(ctx context.Context, state *SharedState, saved model.ResourceSpec) error
	destroyFn   func(ctx context.Context, saved model.ResourceSpec) error
	stateDataFn func() model.ResourceSpec
}

func (s *mockStep) Name() string           { return s.name }
func (s *mockStep) Type() string           { return s.stepType }
func (s *mockStep) Dependencies() []string { return s.deps }
func (s *mockStep) Apply(ctx context.Context, state *SharedState, saved model.ResourceSpec) error {
	if s.applyFn != nil {
		return s.applyFn(ctx, state, saved)
	}
	return nil
}
func (s *mockStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.destroyFn != nil {
		return s.destroyFn(ctx, saved)
	}
	return nil
}
func (s *mockStep) StateData() model.ResourceSpec {
	if s.stateDataFn != nil {
		return s.stateDataFn()
	}
	return nil
}

// errSentinel is a reusable test error.
var errSentinel = errors.New("step failed")

// ─── BuildDAG ─────────────────────────────────────────────────────────────────

// Rationale: Verify BuildDAG handles the simplest case — a single step with no dependencies.
func TestBuildDAG_SingleStep(t *testing.T) {
	steps := []Step{&mockStep{stepType: "", name: "a"}}

	levels, err := BuildDAG(steps)
	require.NoError(t, err)
	require.Len(t, levels, 1)
	require.Len(t, levels[0], 1)
	assert.Equal(t, "a", levels[0][0].Name())
}

// Rationale: Verify BuildDAG correctly orders a linear chain a→b→c into 3 topological levels.
func TestBuildDAG_LinearChain(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
		&mockStep{stepType: "", name: "c", deps: []string{"b"}},
	}

	levels, err := BuildDAG(steps)
	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Equal(t, "a", levels[0][0].Name())
	assert.Equal(t, "b", levels[1][0].Name())
	assert.Equal(t, "c", levels[2][0].Name())
}

// Rationale: Verify BuildDAG correctly handles diamond dependencies (a has two dependents b and c,
// which merge into d). Level 1 must contain both b and c.
func TestBuildDAG_DiamondDependency(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
		&mockStep{stepType: "", name: "c", deps: []string{"a"}},
		&mockStep{stepType: "", name: "d", deps: []string{"b", "c"}},
	}

	levels, err := BuildDAG(steps)
	require.NoError(t, err)
	require.Len(t, levels, 3)
	require.Len(t, levels[0], 1)
	assert.Equal(t, "a", levels[0][0].Name())
	require.Len(t, levels[1], 2)
	assert.ElementsMatch(t, []string{"b", "c"}, []string{levels[1][0].Name(), levels[1][1].Name()})
	require.Len(t, levels[2], 1)
	assert.Equal(t, "d", levels[2][0].Name())
}

// Rationale: Verify BuildDAG places all independent steps in the same level (level 0).
func TestBuildDAG_IndependentSteps(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b"},
		&mockStep{stepType: "", name: "c"},
	}

	levels, err := BuildDAG(steps)
	require.NoError(t, err)
	require.Len(t, levels, 1)
	require.Len(t, levels[0], 3)
}

// Rationale: Verify BuildDAG detects cycles and returns an actionable error that includes
// the cycle path with step names.
func TestBuildDAG_CycleDetection(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a", deps: []string{"b"}},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
	}

	_, err := BuildDAG(steps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle detected")
	assert.Contains(t, err.Error(), "a")
	assert.Contains(t, err.Error(), "b")
}

// Rationale: Verify BuildDAG rejects duplicate step names with a clear error message.
func TestBuildDAG_DuplicateStepName(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "dup"},
		&mockStep{stepType: "", name: "dup"},
	}

	_, err := BuildDAG(steps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate step name")
}

// Rationale: Verify BuildDAG rejects steps that reference missing dependencies.
func TestBuildDAG_MissingDependency(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a", deps: []string{"nonexistent"}},
	}

	_, err := BuildDAG(steps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depends on")
	assert.Contains(t, err.Error(), "nonexistent")
}

// Rationale: Verify BuildDAG handles nil and empty step slices gracefully,
// returning nil levels rather than panicking or returning an error.
func TestBuildDAG_EmptySteps(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		levels, err := BuildDAG(nil)
		require.NoError(t, err)
		assert.Nil(t, levels)
	})
	t.Run("empty_slice", func(t *testing.T) {
		levels, err := BuildDAG([]Step{})
		require.NoError(t, err)
		assert.Nil(t, levels)
	})
}

// ─── SharedState ──────────────────────────────────────────────────────────────

// Rationale: SharedState must correctly round-trip Set/Get for both string and int values.
func TestSharedState_SetGet_RoundTrip(t *testing.T) {
	state := NewSharedState()
	state.Set("step-a", "value-a")
	state.Set("step-b", 42)

	v, ok := state.Get("step-a")
	assert.True(t, ok, "expected step-a to be found")
	assert.Equal(t, "value-a", v)

	v, ok = state.Get("step-b")
	assert.True(t, ok, "expected step-b to be found")
	assert.Equal(t, 42, v)
}

// Rationale: SharedState.Get must return (nil, false) for missing keys, not panic or return garbage.
func TestSharedState_Get_MissingKey(t *testing.T) {
	state := NewSharedState()
	v, ok := state.Get("nonexistent")
	assert.False(t, ok, "expected false for missing key")
	assert.Nil(t, v, "expected nil for missing key")
}

// Rationale: SharedState.Keys must return all stored keys, not just a subset.
func TestSharedState_Keys(t *testing.T) {
	state := NewSharedState()
	state.Set("a", 1)
	state.Set("b", 2)
	state.Set("c", 3)

	keys := state.Keys()
	require.Len(t, keys, 3)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, keys)
}

// Rationale: SharedState must be safe for concurrent goroutine access — concurrent
// Set and Get calls must not race.
func TestSharedState_ConcurrentAccess(t *testing.T) {
	state := NewSharedState()
	var wg sync.WaitGroup

	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			state.Set(fmt.Sprintf("key-%d", n), n)
		}(i)
	}

	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			state.Get(fmt.Sprintf("key-%d", n))
		}(i)
	}

	wg.Wait()

	keys := state.Keys()
	assert.Len(t, keys, 20, "expected 20 keys after concurrent Set/Get")
}

// ─── Pipeline ─────────────────────────────────────────────────────────────────

// Rationale: NewPipeline must accept valid linear-chain steps and compute the correct level count.
func TestNewPipeline_ValidSteps(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
	}
	p, err := NewPipeline(steps)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, 2, len(p.Levels()))
}

// Rationale: NewPipeline must reject cyclic steps with an error mentioning "cycle".
func TestNewPipeline_CyclicSteps(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a", deps: []string{"b"}},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
	}
	_, err := NewPipeline(steps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

// Rationale: Execute must run all steps in topological order — a must complete before b and c.
func TestPipeline_Execute_AllSucceed(t *testing.T) {
	var mu sync.Mutex
	var execOrder []string

	steps := []Step{
		&mockStep{stepType: "",
			name: "a",
			applyFn: func(_ context.Context, _ *SharedState, _ model.ResourceSpec) error {
				mu.Lock()
				execOrder = append(execOrder, "a")
				mu.Unlock()
				return nil
			},
		},
		&mockStep{stepType: "",
			name: "b",
			deps: []string{"a"},
			applyFn: func(_ context.Context, _ *SharedState, _ model.ResourceSpec) error {
				mu.Lock()
				execOrder = append(execOrder, "b")
				mu.Unlock()
				return nil
			},
		},
		&mockStep{stepType: "",
			name: "c",
			deps: []string{"a"},
			applyFn: func(_ context.Context, _ *SharedState, _ model.ResourceSpec) error {
				mu.Lock()
				execOrder = append(execOrder, "c")
				mu.Unlock()
				return nil
			},
		},
	}

	ctx := context.Background()
	p, err := NewPipeline(steps)
	require.NoError(t, err)

	state := NewSharedState()
	require.NoError(t, p.Execute(ctx, state, nil, nil))

	require.Len(t, execOrder, 3)
	// a must come first; b and c order may vary
	assert.Equal(t, "a", execOrder[0])
}

// Rationale: Execute must propagate step errors — if a step returns an error, Execute
// must return it, including the failed step name.
func TestPipeline_Execute_StepFails(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "",
			name: "b",
			deps: []string{"a"},
			applyFn: func(_ context.Context, _ *SharedState, _ model.ResourceSpec) error {
				return errSentinel
			},
		},
	}

	ctx := context.Background()
	p, err := NewPipeline(steps)
	require.NoError(t, err)

	state := NewSharedState()
	err = p.Execute(ctx, state, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), errSentinel.Error())
	assert.Contains(t, err.Error(), "b")
}

// Rationale: Execute on an empty pipeline (no steps) must return nil, not an error.
func TestPipeline_Execute_EmptyLevels(t *testing.T) {
	p := &Pipeline{}
	ctx := context.Background()
	assert.NoError(t, p.Execute(ctx, NewSharedState(), nil, nil))
}

// Rationale: Execute must invoke the onProgress callback with the correct phase/status
// sequence for each step (running → complete for success).
func TestPipeline_Execute_ProgressCallback(t *testing.T) {
	var mu sync.Mutex
	var progressEvents []string

	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
	}

	onProgress := func(phase, status, msg string) {
		mu.Lock()
		progressEvents = append(progressEvents, phase+":"+status)
		mu.Unlock()
	}

	ctx := context.Background()
	p, err := NewPipeline(steps)
	require.NoError(t, err)

	state := NewSharedState()
	require.NoError(t, p.Execute(ctx, state, onProgress, nil))

	require.Len(t, progressEvents, 4)
	want := []string{"a:running", "a:complete", "b:running", "b:complete"}
	assert.Equal(t, want, progressEvents)
}

// Rationale: Execute must respect context cancellation — if context is cancelled before
// Execute starts, the function must return the context error.
func TestPipeline_Execute_ContextCancellation(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "",
			name: "blocked",
			applyFn: func(ctx context.Context, _ *SharedState, _ model.ResourceSpec) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	p, err := NewPipeline(steps)
	require.NoError(t, err)

	cancel() // cancel before Execute starts

	state := NewSharedState()
	err = p.Execute(ctx, state, nil, nil)
	require.Error(t, err)
}

// Rationale: Destroy must run steps in reverse topological order — c (deepest) before b before a.
func TestPipeline_Destroy_ReverseOrder(t *testing.T) {
	var mu sync.Mutex
	var destroyOrder []string

	steps := []Step{
		&mockStep{stepType: "",
			name: "a",
			destroyFn: func(_ context.Context, _ model.ResourceSpec) error {
				mu.Lock()
				destroyOrder = append(destroyOrder, "a")
				mu.Unlock()
				return nil
			},
		},
		&mockStep{stepType: "",
			name: "b",
			deps: []string{"a"},
			destroyFn: func(_ context.Context, _ model.ResourceSpec) error {
				mu.Lock()
				destroyOrder = append(destroyOrder, "b")
				mu.Unlock()
				return nil
			},
		},
		&mockStep{stepType: "",
			name: "c",
			deps: []string{"b"},
			destroyFn: func(_ context.Context, _ model.ResourceSpec) error {
				mu.Lock()
				destroyOrder = append(destroyOrder, "c")
				mu.Unlock()
				return nil
			},
		},
	}

	p, err := NewPipeline(steps)
	require.NoError(t, err)

	saved := []model.SavedResource{
		{StepName: "a", StepType: "mock", State: model.ResourceSpec{}},
		{StepName: "b", StepType: "mock", State: model.ResourceSpec{}},
		{StepName: "c", StepType: "mock", State: model.ResourceSpec{}},
	}

	ctx := context.Background()
	require.NoError(t, p.Destroy(ctx, saved, nil))

	require.Len(t, destroyOrder, 3)
	assert.Equal(t, []string{"c", "b", "a"}, destroyOrder)
}

// Rationale: Destroy must pass the saved state from model.SavedResource.State to the step's Destroy
// method so the step knows what resources to tear down.
func TestPipeline_Destroy_WithSavedState(t *testing.T) {
	var receivedState model.ResourceSpec

	steps := []Step{
		&mockStep{stepType: "",
			name: "a",
			destroyFn: func(_ context.Context, saved model.ResourceSpec) error {
				receivedState = saved
				return nil
			},
		},
	}

	p, err := NewPipeline(steps)
	require.NoError(t, err)

	expectedState := model.ResourceSpec{"key": "value", "num": 42}
	saved := []model.SavedResource{
		{StepName: "a", StepType: "mock", State: expectedState},
	}

	ctx := context.Background()
	require.NoError(t, p.Destroy(ctx, saved, nil))

	require.NotNil(t, receivedState, "Destroy should receive saved state")
	if diff := cmp.Diff(expectedState, receivedState); diff != "" {
		t.Errorf("Destroy() saved state mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Destroy must propagate step errors — if a step's Destroy returns an error,
// the pipeline must return it to the caller.
func TestPipeline_Destroy_StepFails(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "",
			name: "a",
			destroyFn: func(_ context.Context, _ model.ResourceSpec) error {
				return errSentinel
			},
		},
	}

	p, err := NewPipeline(steps)
	require.NoError(t, err)

	saved := []model.SavedResource{
		{StepName: "a", StepType: "mock", State: model.ResourceSpec{}},
	}

	ctx := context.Background()
	err = p.Destroy(ctx, saved, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), errSentinel.Error())
}

// Rationale: Destroy must respect context cancellation — if context is cancelled before
// Destroy starts, the step must see the cancelled context and the pipeline must propagate
// the cancellation error.
func TestPipeline_Destroy_ContextCancellation(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "",
			name: "blocked",
			destroyFn: func(ctx context.Context, _ model.ResourceSpec) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Destroy starts

	p, err := NewPipeline(steps)
	require.NoError(t, err)

	saved := []model.SavedResource{
		{StepName: "blocked", StepType: "mock", State: model.ResourceSpec{}},
	}

	err = p.Destroy(ctx, saved, nil)
	require.Error(t, err)
}

// Rationale: Destroy on an empty pipeline (no steps) must return nil, not an error.
func TestPipeline_Destroy_EmptyLevels(t *testing.T) {
	p := &Pipeline{}
	ctx := context.Background()
	assert.NoError(t, p.Destroy(ctx, nil, nil))
}

// Rationale: Steps and Levels accessors must return the same data that was computed during construction.
func TestPipeline_StepsAndLevels(t *testing.T) {
	steps := []Step{
		&mockStep{stepType: "", name: "a"},
		&mockStep{stepType: "", name: "b", deps: []string{"a"}},
	}
	p, err := NewPipeline(steps)
	require.NoError(t, err)

	gotSteps := p.Steps()
	require.Len(t, gotSteps, 2)

	levels := p.Levels()
	require.Len(t, levels, 2)
}

// ─── State Persistence ────────────────────────────────────────────────────────

// Rationale: WriteWorkflowState must create a state.yaml file, and ReadWorkflowState must
// return identical data. Field-by-field comparison via cmp.Diff ensures no field is missed.
func TestStatePersistence_WriteRead(t *testing.T) {
	dir := t.TempDir()

	state := &model.WorkflowState{
		WorkflowID:    "wf-test-123",
		SpecPath:      "/home/user/spec.yaml",
		SchemaVersion: "1.0",
		CreatedAt:     "2025-06-01T12:00:00Z",
		UpdatedAt:     "2025-06-01T12:00:00Z",
		Resources: []model.SavedResource{
			{
				StepName: "network:my-net",
				StepType: "network",
				State:    model.ResourceSpec{"network_id": "net-abc", "was_created": true},
			},
		},
	}

	require.NoError(t, WriteWorkflowState(dir, state))

	// Verify the state.yaml file was created
	statePath := filepath.Join(dir, "state.yaml")
	_, err := os.Stat(statePath)
	require.NoError(t, err, "state.yaml was not created")

	readState, err := ReadWorkflowState(dir)
	require.NoError(t, err)

	if diff := cmp.Diff(state, readState); diff != "" {
		t.Errorf("model.WorkflowState round-trip mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: WriteWorkflowState and ReadWorkflowState must round-trip all fields correctly
// for a complex state with multiple resources and dependencies — not just simple cases.
func TestStatePersistence_RoundTripPreservesAllFields(t *testing.T) {
	dir := t.TempDir()

	state := &model.WorkflowState{
		WorkflowID:    "wf-full-test",
		SpecPath:      "/tmp/test-spec.yaml",
		SchemaVersion: "2.0",
		CreatedAt:     "2025-01-15T08:30:00Z",
		UpdatedAt:     "2025-01-15T09:45:00Z",
		Resources: []model.SavedResource{
			{
				StepName:     "network:primary",
				StepType:     "network",
				Dependencies: nil,
				State:        model.ResourceSpec{"network_id": "net-001", "subnet": "10.0.0.0/24", "was_created": true},
			},
			{
				StepName:     "key:admin",
				StepType:     "key",
				Dependencies: nil,
				State:        model.ResourceSpec{"key_id": "key-001", "was_created": true},
			},
			{
				StepName:     "vm:web-server",
				StepType:     "vm",
				Dependencies: []string{"network:primary", "key:admin"},
				State:        model.ResourceSpec{"vm_id": "vm-001", "vm_dir": "/mnt/vms/vm-001", "was_created": true},
			},
		},
	}

	require.NoError(t, WriteWorkflowState(dir, state))

	readState, err := ReadWorkflowState(dir)
	require.NoError(t, err)

	if diff := cmp.Diff(state, readState); diff != "" {
		t.Errorf("model.WorkflowState round-trip mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: RemoveWorkflowState must delete the entire state directory and all its contents.
func TestStatePersistence_RemoveWorkflowState(t *testing.T) {
	wfID := "to-be-removed"
	dir := infra.GetWorkflowsStateDirByID(wfID)

	state := &model.WorkflowState{
		WorkflowID: wfID,
		SpecPath:   "/tmp/remove-me.yaml",
		Resources:  []model.SavedResource{{StepName: "network:test", StepType: "network"}},
	}

	require.NoError(t, WriteWorkflowState(dir, state))

	// Confirm it exists
	_, err := os.Stat(dir)
	require.NoError(t, err, "state dir should exist after write")

	// Remove by workflow ID
	require.NoError(t, RemoveWorkflowState(wfID))

	// Confirm it's gone
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "state dir should be removed after RemoveWorkflowState")
}

// Rationale: ReadWorkflowState must return an error when no state.yaml exists in the directory,
// rather than panicking or returning a zero-value state.
func TestStatePersistence_ReadWorkflowState_NonExistent(t *testing.T) {
	dir := t.TempDir()
	// No state.yaml written
	_, err := ReadWorkflowState(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read state file")
}

// Rationale: ReadWorkflowState must return an error when state.yaml contains invalid YAML,
// rather than silently returning a zero-value or partial state.
func TestStatePersistence_ReadWorkflowState_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	require.NoError(t, os.WriteFile(statePath, []byte("invalid: [yaml: broken"), 0644))

	_, err := ReadWorkflowState(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// Rationale: RemoveWorkflowState on a non-existent workflow ID must return nil (os.RemoveAll does
// not error on missing paths), not a spurious error.
func TestStatePersistence_RemoveWorkflowState_NonExistentDir(t *testing.T) {
	err := RemoveWorkflowState("nonexistent-workflow-id")
	assert.NoError(t, err)
}

// Rationale: WriteWorkflowState with an invalid directory path (where a file component exists
// but prevents directory creation) must return an error, not panic.
func TestStatePersistence_WriteWorkflowState_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	// Create a file that blocks MkdirAll
	filePath := filepath.Join(dir, "a_file")
	require.NoError(t, os.WriteFile(filePath, []byte("hi"), 0644))

	state := &model.WorkflowState{WorkflowID: "test"}
	err := WriteWorkflowState(filePath, state)
	require.Error(t, err)
}

// Rationale: WriteWorkflowState must create intermediate directories when they do not exist.
func TestStatePersistence_WriteCreatesDirectory(t *testing.T) {
	baseDir := t.TempDir()
	nestedDir := filepath.Join(baseDir, "sub", "nested", "dir")

	state := &model.WorkflowState{
		WorkflowID: "creates-dir",
		SpecPath:   "/tmp/test.yaml",
		Resources:  nil,
	}

	require.NoError(t, WriteWorkflowState(nestedDir, state))

	// Verify the file exists
	_, err := os.Stat(filepath.Join(nestedDir, "state.yaml"))
	require.NoError(t, err, "state.yaml should exist after WriteWorkflowState created dirs")
}

// ─── WorkflowIDFromPath ───────────────────────────────────────────────────────

// Rationale: WorkflowIDFromPath must produce stable, deterministic IDs. Same path →
// same ID. Different paths → different IDs.
func TestStatePersistence_WorkflowIDFromPath(t *testing.T) {
	id := crypto.WorkflowID("/tmp/spec.yaml")
	require.NotEmpty(t, id)
	assert.Len(t, id, 16, "expected 16-char hex ID")

	// Same path should produce the same ID
	id2 := crypto.WorkflowID("/tmp/spec.yaml")
	assert.Equal(t, id, id2, "same path gives different IDs")

	// Different path should produce a different ID
	id3 := crypto.WorkflowID("/tmp/other.yaml")
	assert.NotEqual(t, id, id3, "different paths should give different IDs")
}

// ─── Now ──────────────────────────────────────────────────────────────────────

// Rationale: Now must return a valid RFC3339 timestamp so workflow state timestamps
// are consistently formatted and parseable.
func TestNow_ReturnsRFC3339(t *testing.T) {
	ts := infra.Now()
	require.NotEmpty(t, ts)
	_, err := time.Parse(time.RFC3339, ts)
	assert.NoError(t, err, "Now() returned invalid RFC3339 timestamp %q", ts)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// stepNames extracts names from a slice of steps for readable test output.
func stepNames(steps []Step) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name()
	}
	return names
}
