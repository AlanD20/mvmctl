package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// --- ExecStep.Apply ---
// Rationale: ExecStep.Apply requires a real vsock connection via Exec,
// so we cannot test the happy path without a live VM. We test the nil-op
// guard (R1: every error-returning function needs at least one error case).

func TestExecStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupOp func(t *testing.T) api.API
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
			_, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
				"name":   "run-cmd",
				"target": "my-vm",
				"user":   "root",
				"cmd":    "uptime",
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

// --- ExecStep.Apply Exec exit code ---
// Rationale: ExecStep.Apply must fail when the remote command exits with a
// non-zero code. A zero exit code succeeds and persists state. If Exec
// itself returns an error (e.g., vsock failure), Apply must propagate it.

func TestExecStep_Apply_Exec_ExitCode(t *testing.T) {
	tests := map[string]struct {
		execFunc func(ctx context.Context, input inputs.ExecInput) (*results.ExecResult, error)
		wantErr    string
	}{
		"exit_code_0_succeeds": {
			execFunc: func(_ context.Context, _ inputs.ExecInput) (*results.ExecResult, error) {
				return &results.ExecResult{ExitCode: 0}, nil
			},
		},
		"exit_code_1_fails": {
			execFunc: func(_ context.Context, _ inputs.ExecInput) (*results.ExecResult, error) {
				return &results.ExecResult{ExitCode: 1}, nil
			},
			wantErr: "exited with code 1",
		},
		"exit_code_127_fails": {
			execFunc: func(_ context.Context, _ inputs.ExecInput) (*results.ExecResult, error) {
				return &results.ExecResult{ExitCode: 127}, nil
			},
			wantErr: "exited with code 127",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &testutil.MockOperation{
				MockExecAPI: testutil.MockExecAPI{
					ExecFunc: tc.execFunc,
				},
			}
			step, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
				"name":   "run-cmd",
				"target": "my-vm",
				"user":   "root",
				"cmd":    "some-command",
			}, op)
			require.NoError(t, err, "FromSpec must succeed")

			state := workflow.NewSharedState()
			writer, writes := recordingWriter()
			err = step.Apply(context.Background(), state, model.ResourceState{}, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, *writes, 1, "state must be persisted on success")
		})
	}
}

func TestExecStep_Apply_Exec_Error(t *testing.T) {
	op := &testutil.MockOperation{
		MockExecAPI: testutil.MockExecAPI{
			ExecFunc: func(_ context.Context, _ inputs.ExecInput) (*results.ExecResult, error) {
				return nil, errors.New("vsock connection refused")
			},
		},
	}
	step, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
		"name":   "run-cmd",
		"target": "my-vm",
		"user":   "root",
		"cmd":    "some-command",
	}, op)
	require.NoError(t, err, "FromSpec must succeed")

	state := workflow.NewSharedState()
	writer, writes := recordingWriter()
	err = step.Apply(context.Background(), state, model.ResourceState{}, writer, noopProgress)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "vsock connection refused")
	assert.Len(t, *writes, 0, "state must not be persisted on transport error")
}

// --- ExecStep.Destroy ---
// Rationale: Destroy is a no-op for Exec (commands are ephemeral), but it
// must still handle nil op, write state, and recover saved state from the
// parameter for workflow resumption after a crash.

func TestExecStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupOp     func(t *testing.T) *api.Operation
		setupWriter func() (workflow.StateWriter, *[]model.ResourceState)
		ctx         func() context.Context
		saved       model.ResourceState
		wantErr     string
		wantCommand string
		wantCreated bool
	}{
		// --- Error paths FIRST ---

		"context_cancelled_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return &api.Operation{} },
			setupWriter: func() (workflow.StateWriter, *[]model.ResourceState) {
				return func(ctx context.Context, _ model.ResourceState) error {
					return ctx.Err()
				}, nil
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},

		// --- Happy paths AFTER ---

		"noop_with_nil_saved_writes_empty_state": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			setupWriter: recordingWriter,
			ctx:         context.Background,
			saved:       model.ResourceState{},
		},
		"recovers_saved_state_from_param": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			setupWriter: recordingWriter,
			ctx:         context.Background,
			saved: model.ResourceState{
				Spec: model.ResourceMap{"command": "apt update"},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
			wantCommand: "apt update",
			wantCreated: true,
		},
		"recovers_empty_command_from_saved_spec": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{}
			},
			setupWriter: recordingWriter,
			ctx:         context.Background,
			saved: model.ResourceState{
				Spec: model.ResourceMap{"command": ""},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantCommand: "",
			wantCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
				"name":   "run-cmd",
				"target": "my-vm",
				"user":   "root",
				"cmd":    "uptime",
			}, op)
			require.NoError(t, err, "FromSpec must succeed")

			writer, writes := tc.setupWriter()
			err = step.Destroy(tc.ctx(), tc.saved, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even though it's a no-op for teardown).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			if tc.wantCommand != "" || tc.wantCreated {
				assert.Equal(t, tc.wantCommand, written.Spec["command"],
					"destroyed state must reference the recovered command")
				assert.Equal(t, tc.wantCreated, written.Meta.WasCreated)
			}
		})
	}
}

// --- ExecStep.Destroy write-failure propagation ---
// Rationale: If the StateWriter returns an error, Destroy must propagate it
// rather than silently swallowing the persistence failure.

func TestExecStep_Destroy_WriteFailure(t *testing.T) {
	op := &api.Operation{}
	step, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
		"name":   "run-cmd",
		"target": "my-vm",
		"user":   "root",
		"cmd":    "uptime",
	}, op)
	require.NoError(t, err, "FromSpec must succeed")

	writeErr := errors.New("disk full")
	err = step.Destroy(
		context.Background(),
		model.ResourceState{},
		failingWriter(writeErr),
		noopProgress,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist step state after destroy",
		"Destroy must wrap write errors with context")
}

// --- ExecStep.StateData ---
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestExecStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{"command": "apt update"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Spec: model.ResourceMap{"command": "apt update"},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
		"empty_command_preserved": {
			fromState: true,
			savedSpec: model.ResourceMap{"command": ""},
			savedMeta: model.ResourceMeta{WasCreated: true},
			want: model.ResourceState{
				Spec: model.ResourceMap{"command": ""},
				Meta: model.ResourceMeta{WasCreated: true},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &api.Operation{}

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				var err error
				step, err = envpkg.Registry["exec"].FromState("exec", "run-cmd", saved, nil, op)
				require.NoError(t, err)
			} else {
				var err error
				step, err = envpkg.Registry["exec"].FromSpec("exec", "run-cmd", map[string]any{
					"name":   "run-cmd",
					"target": "my-vm",
					"user":   "root",
					"cmd":    "uptime",
				}, op)
				require.NoError(t, err, "FromSpec must succeed")
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- ExecStep.Dependencies ---
// Rationale: Dependencies must round-trip through FromSpec so that the DAG
// engine can correctly order Exec steps after their VM dependencies.

func TestExecStep_Dependencies(t *testing.T) {
	tests := map[string]struct {
		spec     map[string]any
		wantDeps []string
	}{
		"no_depends_on_returns_nil": {
			spec: map[string]any{
				"name":   "run-cmd",
				"target": "my-vm",
				"user":   "root",
				"cmd":    "uptime",
			},
			wantDeps: nil,
		},
		"explicit_depends_on": {
			spec: map[string]any{
				"name":       "run-cmd",
				"target":     "my-vm",
				"user":       "root",
				"cmd":        "uptime",
				"depends_on": []any{"vm:my-vm"},
			},
			wantDeps: []string{"vm:my-vm"},
		},
		"multiple_dependencies": {
			spec: map[string]any{
				"name":       "run-cmd",
				"target":     "my-vm",
				"user":       "root",
				"cmd":        "uptime",
				"depends_on": []any{"vm:my-vm", "key:my-key"},
			},
			wantDeps: []string{"vm:my-vm", "key:my-key"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			step, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", tc.spec, &api.Operation{})
			require.NoError(t, err, "FromSpec must succeed")

			got := step.Dependencies()
			if diff := cmp.Diff(tc.wantDeps, got); diff != "" {
				t.Errorf("Dependencies() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- ExecStep.SpecHash ---
// Rationale: SpecHash must be deterministic and non-empty for steps created
// from specs, enabling drift detection. Different specs must produce different
// hashes.

func TestExecStep_SpecHash(t *testing.T) {
	spec1 := map[string]any{
		"name":   "run-cmd",
		"target": "my-vm",
		"user":   "root",
		"cmd":    "uptime",
	}
	spec2 := map[string]any{
		"name":   "run-cmd",
		"target": "my-vm",
		"user":   "root",
		"cmd":    "hostname",
	}

	step1, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", spec1, &api.Operation{})
	require.NoError(t, err)

	step2, err := envpkg.Registry["exec"].FromSpec("exec", "run-cmd", spec2, &api.Operation{})
	require.NoError(t, err)

	assert.NotEmpty(t, step1.SpecHash(), "SpecHash must be non-empty for spec-created steps")
	assert.NotEmpty(t, step2.SpecHash(), "SpecHash must be non-empty for spec-created steps")
	assert.NotEqual(t, step1.SpecHash(), step2.SpecHash(),
		"different specs must produce different hashes")
}
