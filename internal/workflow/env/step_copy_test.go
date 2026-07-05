package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
)

// --- Test helpers ---

// newCopyStep creates a CopyStep via the registry with nil op (for tests
// that don't exercise the Apply path).
func newCopyStep(t *testing.T, op api.API) workflow.Step {
	t.Helper()
	spec := map[string]any{
		"name":   "copy-binary",
		"target": "rc-vm",
		"user":   "root",
		"src":    "./mvm",
		"dst":    "/root/",
	}
	step, err := envpkg.Registry["copy"].FromSpec("copy", "copy-binary", spec, op)
	require.NoError(t, err, "FromSpec must succeed")
	return step
}

// newCopyStepFromState creates a CopyStep from previously persisted state.
func newCopyStepFromState(t *testing.T, saved model.ResourceState, op api.API) workflow.Step {
	t.Helper()
	step, err := envpkg.Registry["copy"].FromState("copy", "copy-binary", saved, nil, op)
	require.NoError(t, err, "FromState must succeed")
	return step
}

// dummyOp returns a non-nil Operation for tests that don't exercise the op.
func dummyOp() api.API { return &api.Operation{} }

// noopProgressCopy is a no-op progress callback for copy step tests.
func noopProgressCopy(_ event.Progress) {}

// recordingWriterCopy returns a StateWriter that captures all writes.
func recordingWriterCopy() (workflow.StateWriter, *[]model.ResourceState) {
	var writes []model.ResourceState
	return func(_ context.Context, state model.ResourceState) error {
		writes = append(writes, state)
		return nil
	}, &writes
}

// failingWriterCopy returns a StateWriter that always returns the given error.
func failingWriterCopy(err error) workflow.StateWriter {
	return func(_ context.Context, _ model.ResourceState) error {
		return err
	}
}

// --- CopyStep.Apply ---
// Rationale: CopyStep.Apply requires a real SSH connection to execute (CPCopy),
// so we cannot test the happy path in unit tests. We test the nil-op guard to
// ensure callers get a clear error instead of a nil pointer dereference crash.

func TestCopyStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupOp func(_ *testing.T) api.API
		ctx     func() context.Context
		wantErr string
	}{
		// --- Error paths FIRST ---

		"nil_op_rejected_at_construction": {
			setupOp: func(_ *testing.T) api.API { return nil },
			ctx:     context.Background,
			wantErr: "operation not initialized",
		},
		"nil_op_with_cancelled_context_rejected_at_construction": {
			setupOp: func(_ *testing.T) api.API { return nil },
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "operation not initialized",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			_, err := envpkg.Registry["copy"].FromSpec("copy", "copy-binary", map[string]any{
				"name":   "copy-binary",
				"target": "rc-vm",
				"user":   "root",
				"src":    "./mvm",
				"dst":    "/root/",
			}, op)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// --- CopyStep.Destroy ---
// Rationale: CopyStep.Destroy is a no-op for teardown (file copies are
// ephemeral), but it must handle nil op, write state for persistence, and
// recover saved state from the parameter for workflow resumption after a crash.

func TestCopyStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(_ *testing.T) *api.Operation
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantSource     string
		wantWasCreated bool
	}{
		// --- Error paths FIRST ---

		"context_cancelled_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},

		// --- Happy paths AFTER ---

		"writes_state_and_returns_nil": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			ctx:   context.Background,
			saved: model.ResourceState{},
		},
		"recovers_saved_state_from_param": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Spec: model.ResourceMap{"source": "./mvm"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantSource:     "./mvm",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newCopyStep(t, op)

			writer, writes := recordingWriterCopy()
			// Use a context-aware writer for context cancellation tests.
			ctxWriter := func(ctx context.Context, state model.ResourceState) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				return writer(ctx, state)
			}
			err := step.Destroy(tc.ctx(), tc.saved, ctxWriter, noopProgressCopy)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even though it's a no-op for teardown).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			if tc.wantSource != "" {
				assert.Equal(t, tc.wantSource, written.Output["source"],
					"destroyed state must reference the recovered source")
				assert.Equal(t, tc.wantWasCreated, written.Meta.WasCreated)
			}
		})
	}
}

// --- CopyStep.Destroy write failure ---
// Rationale: If the StateWriter returns an error during Destroy, the error
// must be wrapped and propagated. Silently swallowing persistence failures
// causes state drift on the next workflow run.

func TestCopyStep_Destroy_WriteFailure(t *testing.T) {
	op := &api.Operation{}
	step := newCopyStep(t, op)

	writeErr := errors.New("disk full")
	err := step.Destroy(
		context.Background(),
		model.ResourceState{},
		failingWriterCopy(writeErr),
		noopProgressCopy,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist step state after destroy",
		"Destroy must wrap write errors with context")
}

// --- CopyStep.StateData ---
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestCopyStep_StateData(t *testing.T) {
	tests := map[string]struct {
		fromState bool
		savedSpec model.ResourceMap
		savedMeta model.ResourceMeta
		want      model.ResourceState
	}{
		"nil_saved_returns_empty": {
			fromState: false,
			want:      model.ResourceState{},
		},
		"with_saved_returns_correct_state": {
			fromState: true,
			savedSpec: model.ResourceMap{"source": "./mvm"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Output: model.ResourceMap{"source": "./mvm"},
				Meta:   model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &api.Operation{}

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				step = newCopyStepFromState(t, saved, op)
			} else {
				step = newCopyStep(t, op)
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- CopyStep.SpecHash ---
// Rationale: SpecHash must be set from the original YAML spec for drift
// detection. If the hash is empty or changes between runs, the workflow
// engine cannot detect configuration drift.

func TestCopyStep_SpecHash(t *testing.T) {
	step := newCopyStep(t, dummyOp())
	hash := step.SpecHash()
	assert.NotEmpty(t, hash, "SpecHash must be set from spec for drift detection")
}

// --- CopyStep.Name and Type ---
// Rationale: Name() must return "type:name" format and Type() must return
// the step type, so that dependency resolution and registry lookups work.

func TestCopyStep_NameAndType(t *testing.T) {
	step := newCopyStep(t, dummyOp())
	assert.Equal(t, "copy:copy-binary", step.Name())
	assert.Equal(t, "copy", step.Type())
}

// --- CopyStep.Dependencies ---
// Rationale: When depends_on is specified in the spec, Dependencies() must
// return those values so the DAG executor runs steps in the correct order.

func TestCopyStep_Dependencies(t *testing.T) {
	spec := map[string]any{
		"name":       "copy-binary",
		"target":     "rc-vm",
		"user":       "root",
		"src":        "./mvm",
		"dst":        "/root/",
		"depends_on": []any{"vm:my-vm"},
	}
	step, err := envpkg.Registry["copy"].FromSpec("copy", "copy-binary", spec, dummyOp())
	require.NoError(t, err)

	deps := step.Dependencies()
	require.Len(t, deps, 1)
	assert.Equal(t, "vm:my-vm", deps[0])
}

// --- CopyStep from spec with multi-source ---
// Rationale: The YAML spec allows `src` as a single string (convenience) or
// as a list. The factory must handle both cases correctly.

func TestFromSpec_CopyStep_MultiSource(t *testing.T) {
	spec := map[string]any{
		"name":   "copy-files",
		"target": "rc-vm",
		"user":   "root",
		"src":    []any{"./file1", "./file2"},
		"dst":    "/root/",
	}
	step, err := envpkg.Registry["copy"].FromSpec("copy", "copy-files", spec, dummyOp())
	require.NoError(t, err)
	assert.Equal(t, "copy:copy-files", step.Name())
	assert.Equal(t, "copy", step.Type())
}

// --- CopyStep from state preserves meta ---
// Rationale: When reconstructing a step from persisted state, the meta
// (WasCreated, SpecHash) must be preserved exactly so that subsequent
// Destroy operations can make correct decisions.

func TestFromState_CopyStep_PreservesMeta(t *testing.T) {
	saved := model.ResourceState{
		Spec: model.ResourceMap{"source": "./mvm"},
		Meta: model.ResourceMeta{WasCreated: true, SpecHash: "deadbeef"},
	}
	step, err := envpkg.Registry["copy"].FromState("copy", "copy-binary", saved, []string{"vm:my-vm"}, dummyOp())
	require.NoError(t, err)

	got := step.StateData()
	want := model.ResourceState{
		Output: model.ResourceMap{"source": "./mvm"},
		Meta:   model.ResourceMeta{WasCreated: true, SpecHash: "deadbeef"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
	}

	// Also verify dependencies were preserved.
	wantDeps := []string{"vm:my-vm"}
	if diff := cmp.Diff(wantDeps, step.Dependencies()); diff != "" {
		t.Errorf("Dependencies() mismatch (-want +got):\n%s", diff)
	}
}
