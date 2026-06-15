package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// ctxBinaryRepo wraps testutil.BinaryRepo to propagate context cancellation
// from GetByTypeAndVersion. The plain mock ignores context, so this wrapper
// is needed to test the R8 (context cancellation) iron rule.
type ctxBinaryRepo struct {
	*testutil.BinaryRepo
}

var _ binary.Repository = (*ctxBinaryRepo)(nil)

func (r *ctxBinaryRepo) GetByTypeAndVersion(ctx context.Context, typ, version string) (*model.BinaryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.BinaryRepo.GetByTypeAndVersion(ctx, typ, version)
}

// ctxWriter wraps a StateWriter to propagate context cancellation.
// The plain recordingWriter ignores context, so this wrapper is needed
// to test context cancellation in steps that only use ctx via write().
func ctxWriter(base workflow.StateWriter) workflow.StateWriter {
	return func(ctx context.Context, state model.ResourceState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return base(ctx, state)
	}
}

// errorBinaryRepo wraps testutil.BinaryRepo to inject errors into GetByTypeAndVersion.
type errorBinaryRepo struct {
	*testutil.BinaryRepo
	getErr error
}

var _ binary.Repository = (*errorBinaryRepo)(nil)

func (r *errorBinaryRepo) GetByTypeAndVersion(_ context.Context, _, _ string) (*model.BinaryItem, error) {
	return nil, r.getErr
}

// newBinaryStep is a shorthand for creating a BinaryStep via the registry.
// For nil-op tests, it constructs the step directly via NewBinaryStep.
func newBinaryStep(t *testing.T, op *api.Operation) workflow.Step {
	t.Helper()
	if op == nil {
		return envpkg.NewBinaryStep(nil, "firecracker", inputs.BinaryPullInput{Type: "firecracker", Version: "1.15.1"})
	}
	spec := map[string]any{
		"type":    "firecracker",
		"version": "1.15.1",
	}
	step, err := envpkg.Registry["binary"].FromSpec("binary", "firecracker", spec, op)
	require.NoError(t, err, "FromSpec must succeed")
	return step
}

// ─── BinaryStep.Apply ────────────────────────────────────────────────────────
// Rationale: BinaryStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestBinaryStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		setupStep      func(t *testing.T) workflow.Step // overrides setupOp
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantBinaryID   string
		wantWasCreated bool
	}{
		// ── Error paths FIRST ──────────────────────────────────────────

		"nil_op_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return nil },
			ctx:     context.Background,
			wantErr: "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupStep: func(t *testing.T) workflow.Step {
				t.Helper()
				mockAPI := &testutil.MockBinaryAPI{
					BinaryGetFunc: func(ctx context.Context, _ inputs.BinaryInput) ([]*model.BinaryItem, error) {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						return nil, errs.NotFound(errs.CodeBinaryNotFound, "not found")
					},
				}
				return envpkg.NewBinaryStep(mockAPI, "firecracker",
					inputs.BinaryPullInput{Type: "firecracker", Version: "1.15.1"},
				)
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},
		"getbytypeandversion_database_error_wraps_correctly": {
			setupStep: func(t *testing.T) workflow.Step {
				t.Helper()
				mockAPI := &testutil.MockBinaryAPI{
					BinaryGetFunc: func(_ context.Context, _ inputs.BinaryInput) ([]*model.BinaryItem, error) {
						return nil, errors.New("connection refused")
					},
				}
				return envpkg.NewBinaryStep(mockAPI, "firecracker",
					inputs.BinaryPullInput{Type: "firecracker", Version: "1.15.1"},
				)
			},
			ctx:     context.Background,
			wantErr: "check binary",
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"binary_exists_skips_pull_and_writes_state": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewBinaryRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.BinaryItem{
					ID:        "bin-existing",
					Type:      "firecracker",
					Version:   "1.15.1",
					IsPresent: true,
				}))
				return &api.Operation{Repos: api.Repos{Binary: repo}}
			},
			ctx:            context.Background,
			wantBinaryID:   "bin-existing",
			wantWasCreated: false, // WasCreated defaults to false on fresh run
		},
		"binary_exists_preserves_was_created_from_saved": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewBinaryRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.BinaryItem{
					ID:        "bin-preserved",
					Type:      "firecracker",
					Version:   "1.15.1",
					IsPresent: true,
				}))
				return &api.Operation{Repos: api.Repos{Binary: repo}}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantBinaryID:   "bin-preserved",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var step workflow.Step
			if tc.setupStep != nil {
				step = tc.setupStep(t)
			} else {
				op := tc.setupOp(t)
				step = newBinaryStep(t, op)
			}

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
			want := model.ResourceState{
				Spec: model.ResourceMap{"binary_id": tc.wantBinaryID},
				Meta: model.ResourceMeta{WasCreated: tc.wantWasCreated},
			}
			if diff := cmp.Diff(want, written, cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash")); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("binary:firecracker")
			require.True(t, ok, "shared state must contain step output")
			binState, ok := val.(*envpkg.BinaryState)
			require.True(t, ok, "shared state value must be *BinaryState")
			assert.Equal(t, tc.wantBinaryID, binState.BinaryID)
		})
	}
}

// ─── BinaryStep.Destroy ──────────────────────────────────────────────────────
// Rationale: Destroy is a no-op for binaries (they persist in the DB), but it
// must still handle nil op, write state, and recover saved state from the
// parameter for workflow resumption after a crash.

func TestBinaryStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		ctx            func() context.Context
		writer         func() (workflow.StateWriter, *[]model.ResourceState) // nil → recordingWriter
		saved          model.ResourceState
		wantErr        string
		wantErrIs      error // if set, assert errors.Is(err, wantErrIs)
		wantBinaryID   string
		wantWasCreated bool
	}{
		// ── Error paths FIRST ──────────────────────────────────────────

		"nil_op_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return nil },
			ctx:     context.Background,
			wantErr: "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{Repos: api.Repos{Binary: testutil.NewBinaryRepo()}}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			writer: func() (workflow.StateWriter, *[]model.ResourceState) {
				base, writes := recordingWriter()
				return ctxWriter(base), writes
			},
			wantErrIs: context.Canceled,
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"writes_state_and_returns_nil": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{Repos: api.Repos{Binary: testutil.NewBinaryRepo()}}
			},
			ctx:   context.Background,
			saved: model.ResourceState{},
		},
		"recovers_saved_state_from_param": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{Repos: api.Repos{Binary: testutil.NewBinaryRepo()}}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Spec: model.ResourceMap{"binary_id": "bin-456"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantBinaryID:   "bin-456",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newBinaryStep(t, op)

			var writer workflow.StateWriter
			var writes *[]model.ResourceState
			if tc.writer != nil {
				writer, writes = tc.writer()
			} else {
				writer, writes = recordingWriter()
			}
			err := step.Destroy(tc.ctx(), tc.saved, writer, noopProgress)

			if tc.wantErrIs != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErrIs)
				return
			}
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
			want := model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: tc.wantWasCreated},
			}
			if tc.wantBinaryID != "" {
				want.Spec = model.ResourceMap{"binary_id": tc.wantBinaryID}
			}
			if diff := cmp.Diff(
				want,
				written,
				cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BinaryStep.StateData ────────────────────────────────────────────────────
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestBinaryStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{"binary_id": "bin-123"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Spec: model.ResourceMap{"binary_id": "bin-123"},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &api.Operation{Repos: api.Repos{Binary: testutil.NewBinaryRepo()}}

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				var err error
				step, err = envpkg.Registry["binary"].FromState("binary", "firecracker", saved, nil, op)
				require.NoError(t, err)
			} else {
				step = newBinaryStep(t, op)
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BinaryStep.StateData write-failure propagation ──────────────────────────
// Rationale: If the StateWriter returns an error, Apply and Destroy must
// propagate it rather than silently swallowing the persistence failure.

func TestBinaryStep_Apply_WriteFailure(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.BinaryItem{
		ID:        "bin-1",
		Type:      "firecracker",
		Version:   "1.15.1",
		IsPresent: true,
	}))
	op := &api.Operation{Repos: api.Repos{Binary: repo}}
	step := newBinaryStep(t, op)

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

func TestBinaryStep_Destroy_WriteFailure(t *testing.T) {
	op := &api.Operation{Repos: api.Repos{Binary: testutil.NewBinaryRepo()}}
	step := newBinaryStep(t, op)

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
