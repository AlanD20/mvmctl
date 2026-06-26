package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// --- Test helpers ---

// ctxKeyRepo wraps testutil.KeyRepo to propagate context cancellation
// from GetByName. The plain mock ignores context, so this wrapper is needed
// to test the R8 (context cancellation) iron rule.
type ctxKeyRepo struct {
	*testutil.KeyRepo
}

var _ key.Repository = (*ctxKeyRepo)(nil)

func (r *ctxKeyRepo) GetByName(ctx context.Context, name string) (*model.SSHKeyItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.KeyRepo.GetByName(ctx, name)
}

// errorKeyRepo wraps testutil.KeyRepo to inject errors into GetByName.
type errorKeyRepo struct {
	*testutil.KeyRepo
	getErr error
}

var _ key.Repository = (*errorKeyRepo)(nil)

func (r *errorKeyRepo) GetByName(_ context.Context, _ string) (*model.SSHKeyItem, error) {
	return nil, r.getErr
}

// newKeyStep is a shorthand for creating a KeyStep via the registry.
// For nil-op tests, it constructs the step directly via NewKeyStep.
func newKeyStep(t *testing.T, op *api.Operation) workflow.Step {
	t.Helper()
	if op == nil {
		return envpkg.NewKeyStep(nil, "my-key", inputs.KeyCreateInput{Name: "my-key"})
	}
	spec := map[string]any{"name": "my-key"}
	step, err := envpkg.Registry["key"].FromSpec("key", "my-key", spec, op)
	require.NoError(t, err, "FromSpec must succeed")
	return step
}

// newKeyOp creates a minimal Operation with repos suitable for KeyRemove.
// AuditLog is included because KeyRemove calls op.AuditLog.LogOperation.
func newKeyOp(keyRepo key.Repository) *api.Operation {
	return &api.Operation{
		Repos: api.Repos{
			Key: keyRepo,
			VM:  testutil.NewVMRepo(),
		},
		AuditLog: logging.NewAuditLog(),
	}
}

// --- KeyStep.Apply ---
// Rationale: KeyStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestKeyStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantKeyID      string
		wantWasCreated bool
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return nil },
			ctx:     context.Background,
			wantErr: "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{
					Repos: api.Repos{Key: &ctxKeyRepo{KeyRepo: testutil.NewKeyRepo()}},
				}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},
		"getbyname_database_error_wraps_correctly": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{
					Repos: api.Repos{
						Key: &errorKeyRepo{
							KeyRepo: testutil.NewKeyRepo(),
							getErr:  errors.New("connection refused"),
						},
					},
				}
			},
			ctx:     context.Background,
			wantErr: "check key",
		},

		// --- Happy paths AFTER ---

		"key_exists_skips_and_writes_state": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewKeyRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
					ID:        "key-existing",
					Name:      "my-key",
					IsPresent: true,
				}))
				return newKeyOp(repo)
			},
			ctx:       context.Background,
			wantKeyID: "key-existing",
			// WasCreated defaults to false on fresh run
			wantWasCreated: false,
		},
		"key_exists_preserves_was_created_from_saved": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewKeyRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
					ID:        "key-preserved",
					Name:      "my-key",
					IsPresent: true,
				}))
				return newKeyOp(repo)
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantKeyID:      "key-preserved",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newKeyStep(t, op)

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

			// Verify written state matches expected ResourceState.
			wantState := model.ResourceState{
				Spec: model.ResourceMap{"key_id": tc.wantKeyID},
				Meta: model.ResourceMeta{
					WasCreated: tc.wantWasCreated,
				},
			}
			if diff := cmp.Diff(
				wantState,
				written,
				cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash"),
			); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("key:my-key")
			require.True(t, ok, "shared state must contain step output")
			keyState, ok := val.(*envpkg.KeyState)
			require.True(t, ok, "shared state value must be *KeyState")
			assert.Equal(t, tc.wantKeyID, keyState.KeyID)
		})
	}
}

// --- KeyStep.Apply write-failure propagation ---
// Rationale: If the StateWriter returns an error, Apply must propagate it
// rather than silently swallowing the persistence failure.

func TestKeyStep_Apply_WriteFailure(t *testing.T) {
	repo := testutil.NewKeyRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
		ID:        "key-1",
		Name:      "my-key",
		IsPresent: true,
	}))
	op := newKeyOp(repo)
	step := newKeyStep(t, op)

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

// --- KeyStep.Destroy ---
// Rationale: Destroy is the teardown path for keys. A nil-op crash, a
// WasCreated=false that still removes, or a silent KeyRemove failure would
// all leave orphaned resources or destroy resources that should be kept.

func TestKeyStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		saved          model.ResourceState
		wantErr        string
		wantKeyID      string
		wantWasCreated bool
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return nil },
			wantErr: "operation not initialized",
		},
		"key_remove_error_propagates": {
			setupOp: func(t *testing.T) *api.Operation {
				return &api.Operation{
					Repos: api.Repos{
						Key: &errorKeyRepo{
							KeyRepo: testutil.NewKeyRepo(),
							getErr:  errors.New("db connection lost"),
						},
						VM: testutil.NewVMRepo(),
					},
					AuditLog: logging.NewAuditLog(),
				}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"key_id": "key-err"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantErr: "db connection lost",
		},

		// --- Happy paths AFTER ---

		"saved_nil_and_spec_nil_writes_state_and_returns": {
			setupOp: func(_ *testing.T) *api.Operation {
				return newKeyOp(testutil.NewKeyRepo())
			},
			saved: model.ResourceState{},
			// No removal, no error — just writes state
		},
		"was_created_false_skips_destroy": {
			setupOp: func(_ *testing.T) *api.Operation {
				return newKeyOp(testutil.NewKeyRepo())
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"key_id": "key-keep"},
				Meta: model.ResourceMeta{WasCreated: false},
			},
			wantKeyID: "key-keep",
			// WasCreated=false → skip removal, still writes state
		},
		"was_created_true_removes_key": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewKeyRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
					ID:            "key-remove",
					Name:          "my-key",
					IsPresent:     true,
					PublicKeyPath: "/tmp/nonexistent.pub", // file won't exist; os.Remove is a no-op
				}))
				return newKeyOp(repo)
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"key_id": "key-remove"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantKeyID:      "key-remove",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newKeyStep(t, op)

			writer, writes := recordingWriter()
			err := step.Destroy(context.Background(), tc.saved, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even on skip).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			// Verify written state matches expected ResourceState.
			wantState := model.ResourceState{}
			if tc.wantKeyID != "" {
				wantState.Spec = model.ResourceMap{"key_id": tc.wantKeyID}
				wantState.Meta = model.ResourceMeta{WasCreated: tc.wantWasCreated}
			}
			if diff := cmp.Diff(wantState, written); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- KeyStep.Destroy context cancellation ---
// Rationale: Destroy must propagate context cancellation so that long-running
// teardown can be interrupted by signal handlers (R8).

func TestKeyStep_Destroy_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Must use WasCreated=true so Destroy actually calls KeyRemove,
	// which passes the context to repo methods. With WasCreated=false or
	// nil saved, Destroy returns early without checking context.
	op := &api.Operation{
		Repos: api.Repos{
			Key: &ctxKeyRepo{KeyRepo: testutil.NewKeyRepo()},
			VM:  testutil.NewVMRepo(),
		},
		AuditLog: logging.NewAuditLog(),
	}
	step := newKeyStep(t, op)

	writer, _ := recordingWriter()
	saved := model.ResourceState{
		Spec: model.ResourceMap{"key_id": "key-ctx"},
		Meta: model.ResourceMeta{WasCreated: true},
	}
	err := step.Destroy(ctx, saved, writer, noopProgress)

	require.Error(t, err)
	// KeyRemove wraps the context error in a BatchResult message,
	// so we check the message rather than errors.Is.
	assert.Contains(t, err.Error(), "context canceled")
}

// --- KeyStep.Destroy write-failure propagation ---
// Rationale: If the StateWriter returns an error, Destroy must propagate it
// rather than silently swallowing the persistence failure.

func TestKeyStep_Destroy_WriteFailure(t *testing.T) {
	op := newKeyOp(testutil.NewKeyRepo())
	step := newKeyStep(t, op)

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

// --- KeyStep.StateData ---
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestKeyStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{"key_id": ""},
			savedMeta: model.ResourceMeta{},
			want: model.ResourceState{
				Spec: model.ResourceMap{"key_id": ""},
				Meta: model.ResourceMeta{},
			},
		},
		"with_saved_returns_correct_state": {
			fromState: true,
			savedSpec: model.ResourceMap{"key_id": "key-123"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Spec: model.ResourceMap{"key_id": "key-123"},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := newKeyOp(testutil.NewKeyRepo())

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				var err error
				step, err = envpkg.Registry["key"].FromState("key", "my-key", saved, nil, op)
				require.NoError(t, err)
			} else {
				step = newKeyStep(t, op)
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- KeyStep.StateData after Apply with existing key ---
// Rationale: After Apply finds an existing key and skips creation, StateData
// must reflect the actual key ID and correct WasCreated flag — not a stale
// or zero-value state.

func TestKeyStep_StateData_AfterApply(t *testing.T) {
	repo := testutil.NewKeyRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
		ID:        "key-abc",
		Name:      "my-key",
		IsPresent: true,
	}))
	op := newKeyOp(repo)
	step := newKeyStep(t, op)

	state := workflow.NewSharedState()
	writer, _ := recordingWriter()

	err := step.Apply(
		context.Background(),
		state,
		model.ResourceState{Meta: model.ResourceMeta{WasCreated: false}},
		writer,
		noopProgress,
	)
	require.NoError(t, err)

	got := step.StateData()

	want := model.ResourceState{
		Spec: model.ResourceMap{"key_id": "key-abc"},
		Meta: model.ResourceMeta{WasCreated: false},
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash")); diff != "" {
		t.Errorf("StateData() after Apply mismatch (-want +got):\n%s", diff)
	}
	assert.NotEmpty(t, got.Meta.SpecHash,
		"SpecHash must be set for drift detection")
}

// --- KeyStep.StateData after Destroy with WasCreated ---
// Rationale: After Destroy removes a key that WasCreated, StateData must
// reflect the final state. The key ID must still be present (we don't clear
// saved on destroy) and WasCreated must remain true (destroy doesn't rewrite history).

func TestKeyStep_StateData_AfterDestroy(t *testing.T) {
	repo := testutil.NewKeyRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.SSHKeyItem{
		ID:            "key-del",
		Name:          "my-key",
		IsPresent:     true,
		PublicKeyPath: "/tmp/nonexistent.pub",
	}))
	op := newKeyOp(repo)

	saved := model.ResourceState{
		Spec: model.ResourceMap{"key_id": "key-del"},
		Meta: model.ResourceMeta{WasCreated: true},
	}
	step, err := envpkg.Registry["key"].FromState("key", "my-key", saved, nil, op)
	require.NoError(t, err)

	writer, _ := recordingWriter()
	err = step.Destroy(context.Background(), saved, writer, noopProgress)
	require.NoError(t, err)

	got := step.StateData()
	assert.Equal(t, "key-del", got.Spec["key_id"],
		"StateData must retain key ID after destroy")
	assert.True(t, got.Meta.WasCreated,
		"WasCreated must remain true after destroy")
}

// --- KeyStep.FromSpec name and type ---
// Rationale: FromSpec must produce a step with correct Name() and Type() so
// that dependency resolution and registry lookups work correctly.

func TestKeyStep_FromSpec_NameAndType(t *testing.T) {
	spec := map[string]any{"name": "ssh-key"}
	step, err := envpkg.Registry["key"].FromSpec("key", "ssh-key", spec, &api.Operation{})
	require.NoError(t, err)

	assert.Equal(t, "key:ssh-key", step.Name())
	assert.Equal(t, "key", step.Type())
	assert.IsType(t, &envpkg.KeyStep{}, step)
}

// --- KeyStep.FromState recovery ---
// Rationale: FromState must reconstruct a step from previously persisted
// state. The resulting step must have correct name, type, and be usable
// for Destroy without a preceding Apply.

func TestKeyStep_FromState_CorrectType(t *testing.T) {
	saved := model.ResourceState{
		Spec: model.ResourceMap{"key_id": "key-789"},
		Meta: model.ResourceMeta{WasCreated: true},
	}
	step, err := envpkg.Registry["key"].FromState("key", "restored-key", saved, nil, &api.Operation{})
	require.NoError(t, err)

	assert.Equal(t, "key:restored-key", step.Name())
	assert.Equal(t, "key", step.Type())
	assert.IsType(t, &envpkg.KeyStep{}, step)
}
