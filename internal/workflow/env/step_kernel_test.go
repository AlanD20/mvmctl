package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// --- Test helpers ---

// ctxKernelAPI wraps testutil.MockKernelAPI to propagate context cancellation
// from KernelGet. The plain mock ignores context, so this wrapper is needed
// to test the R8 (context cancellation) iron rule.
type ctxKernelAPI struct {
	testutil.MockKernelAPI
}

func (m *ctxKernelAPI) KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.MockKernelAPI.KernelGet(ctx, identifier)
}

// ctxRecordingWriter returns a StateWriter that propagates context cancellation
// instead of ignoring it (unlike recordingWriter). Used to test R8 in Destroy,
// which passes ctx to the writer but makes no repo calls.
func ctxRecordingWriter() (workflow.StateWriter, *[]model.ResourceState) {
	var writes []model.ResourceState
	return func(ctx context.Context, state model.ResourceState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		writes = append(writes, state)
		return nil
	}, &writes
}

// newKernelStep is a shorthand for creating a KernelStep.
// For nil-op tests, it constructs the step directly via NewKernelStep.
func newKernelStep(t *testing.T, op api.KernelAPI) workflow.Step {
	t.Helper()
	if op == nil {
		return envpkg.NewKernelStep(nil, "fc-kernel", inputs.KernelPullInput{
			KernelType: "firecracker", Version: "1.15.1",
		})
	}
	return envpkg.NewKernelStep(op, "fc-kernel", inputs.KernelPullInput{
		KernelType: "firecracker", Version: "1.15.1",
	})
}

// --- KernelStep.Apply ---
// Rationale: KernelStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestKernelStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupAPI     func(t *testing.T) api.KernelAPI
		ctx          func() context.Context
		saved        model.ResourceState
		wantErr      string
		wantKernelID string
		wantState    model.ResourceState
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.KernelAPI { return nil },
			ctx:      context.Background,
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &ctxKernelAPI{
					MockKernelAPI: testutil.MockKernelAPI{
						KernelGetFunc: func(_ context.Context, _ string) (*model.KernelItem, error) {
							return &model.KernelItem{}, nil
						},
					},
				}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},
		"getbytype_database_error_wraps_correctly": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{
					KernelGetFunc: func(_ context.Context, _ string) (*model.KernelItem, error) {
						return nil, errors.New("connection refused")
					},
				}
			},
			ctx:     context.Background,
			wantErr: "check kernel type",
		},

		// --- Happy paths AFTER ---

		"kernel_exists_skips_pull_and_writes_state": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{
					KernelGetFunc: func(_ context.Context, _ string) (*model.KernelItem, error) {
						return &model.KernelItem{
							ID:        "krnl-existing",
							Type:      "firecracker",
							IsPresent: true,
						}, nil
					},
				}
			},
			ctx:          context.Background,
			wantKernelID: "krnl-existing",
			wantState: model.ResourceState{
				Output: model.ResourceMap{"kernel_id": "krnl-existing"},
				Meta:   model.ResourceMeta{WasCreated: false},
			},
		},
		"kernel_exists_preserves_was_created_from_saved": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{
					KernelGetFunc: func(_ context.Context, _ string) (*model.KernelItem, error) {
						return &model.KernelItem{
							ID:        "krnl-preserved",
							Type:      "firecracker",
							IsPresent: true,
						}, nil
					},
				}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantKernelID: "krnl-preserved",
			wantState: model.ResourceState{
				Output: model.ResourceMap{"kernel_id": "krnl-preserved"},
				Meta:   model.ResourceMeta{WasCreated: true},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newKernelStep(t, op)

			state := workflow.NewSharedState()
			writer, writes := recordingWriter()

			err := step.Apply(tc.ctx(), state, tc.saved, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Verify state was written exactly once.
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			// Verify the full written state matches expectations.
			// SpecHash is verified separately (must be non-empty for drift detection).
			if diff := cmp.Diff(
				tc.wantState,
				written,
				cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash"),
			); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("kernel:fc-kernel")
			require.True(t, ok, "shared state must contain step output")
			kState, ok := val.(*envpkg.KernelState)
			require.True(t, ok, "shared state value must be *KernelState")
			assert.Equal(t, tc.wantKernelID, kState.KernelID)
		})
	}
}

// --- KernelStep.Destroy ---
// Rationale: Destroy is a no-op for kernels (they persist in the DB), but it
// must still handle nil op, write state, and recover saved state from the
// parameter for workflow resumption after a crash.

func TestKernelStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupAPI  func(t *testing.T) api.KernelAPI
		ctx       func() context.Context
		saved     model.ResourceState
		wantErr   string
		wantState model.ResourceState
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.KernelAPI { return nil },
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{}
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
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{}
			},
			saved:     model.ResourceState{},
			wantState: model.ResourceState{},
		},
		"recovers_saved_state_from_param": {
			setupAPI: func(_ *testing.T) api.KernelAPI {
				return &testutil.MockKernelAPI{}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"kernel_id": "krnl-456"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantState: model.ResourceState{
				Output: model.ResourceMap{"kernel_id": "krnl-456"},
				Meta:   model.ResourceMeta{WasCreated: true},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newKernelStep(t, op)

			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx()
			}

			writer, writes := ctxRecordingWriter()
			err := step.Destroy(ctx, tc.saved, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even though it's a no-op for teardown).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			// Verify the full written state matches expectations.
			if diff := cmp.Diff(tc.wantState, written); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- KernelStep.StateData ---
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestKernelStep_StateData(t *testing.T) {
	tests := map[string]struct {
		fromState bool
		savedSpec model.ResourceMap
		savedMeta model.ResourceMeta
		want      model.ResourceState
	}{
		"from_spec_no_apply_returns_zero_state": {
			fromState: false,
			want:      model.ResourceState{},
		},
		"from_state_empty_spec_returns_round_tripped_state": {
			fromState: true,
			savedSpec: model.ResourceMap{"kernel_id": ""},
			savedMeta: model.ResourceMeta{},
			want: model.ResourceState{
				Output: model.ResourceMap{"kernel_id": ""},
				Meta:   model.ResourceMeta{},
			},
		},
		"with_saved_returns_correct_state": {
			fromState: true,
			savedSpec: model.ResourceMap{"kernel_id": "krnl-123"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Output: model.ResourceMap{"kernel_id": "krnl-123"},
				Meta:   model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dummyOp := &api.Operation{}

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				var err error
				step, err = envpkg.Registry["kernel"].FromState("kernel", "fc-kernel", saved, nil, dummyOp)
				require.NoError(t, err)
			} else {
				step = newKernelStep(t, &testutil.MockKernelAPI{})
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- KernelStep.StateData write-failure propagation ---
// Rationale: If the StateWriter returns an error, Apply and Destroy must
// propagate it rather than silently swallowing the persistence failure.

func TestKernelStep_Apply_WriteFailure(t *testing.T) {
	step := newKernelStep(t, &testutil.MockKernelAPI{
		KernelGetFunc: func(_ context.Context, _ string) (*model.KernelItem, error) {
			return &model.KernelItem{ID: "krnl-1", Type: "firecracker", IsPresent: true}, nil
		},
	})

	writeErr := errors.New("disk full")
	err := step.Apply(
		context.Background(),
		workflow.NewSharedState(),
		model.ResourceState{},
		failingWriter(writeErr),
		noopProgress,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist step state after skip",
		"Apply must wrap write errors with context")
}

func TestKernelStep_Destroy_WriteFailure(t *testing.T) {
	step := newKernelStep(t, &testutil.MockKernelAPI{})

	writeErr := errors.New("disk full")
	err := step.Destroy(
		context.Background(),
		model.ResourceState{},
		failingWriter(writeErr),
		noopProgress,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist step state after destroy",
		"Destroy must wrap write errors with context")
}
