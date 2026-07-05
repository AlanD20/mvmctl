package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// --- Test helpers ---

// noopProgress is a no-op progress callback for tests that don't assert on events.
func noopProgress(_ event.Progress) {}

// recordingWriter returns a StateWriter that captures all writes and a pointer
// to the slice of captured states for post-call assertions.
func recordingWriter() (workflow.StateWriter, *[]model.ResourceState) {
	var writes []model.ResourceState
	return func(_ context.Context, state model.ResourceState) error {
		writes = append(writes, state)
		return nil
	}, &writes
}

// failingWriter returns a StateWriter that always returns the given error.
func failingWriter(err error) workflow.StateWriter {
	return func(_ context.Context, _ model.ResourceState) error {
		return err
	}
}

// ctxAwareWriter returns a StateWriter that propagates context cancellation
// before delegating to the wrapped writer. This is the Destroy-path analogue
// of ctxImageRepo: the plain recordingWriter ignores context, so this wrapper
// is needed to test the R8 (context cancellation) iron rule.
func ctxAwareWriter(inner workflow.StateWriter) workflow.StateWriter {
	return func(ctx context.Context, state model.ResourceState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return inner(ctx, state)
	}
}

// ctxMockImageAPI wraps testutil.MockImageAPI to propagate context cancellation
// from ImageGet. The plain mock ignores context, so this wrapper is needed
// to test the R8 (context cancellation) iron rule.
type ctxMockImageAPI struct {
	testutil.MockImageAPI
}

func (m *ctxMockImageAPI) ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.MockImageAPI.ImageGet(ctx, input)
}

// errorMockImageAPI wraps testutil.MockImageAPI to inject errors into ImageGet.
type errorMockImageAPI struct {
	testutil.MockImageAPI
	getErr error
}

func (m *errorMockImageAPI) ImageGet(_ context.Context, _ inputs.ImageInput) (*model.ImageItem, error) {
	return nil, m.getErr
}

// newImageStep is a shorthand for creating an ImageStep.
// For nil-op tests, it constructs the step directly via NewImageStep.
// For other tests, it uses the registry with the given Operation.
func newImageStep(t *testing.T, op api.ImageAPI) workflow.Step {
	t.Helper()
	if op == nil {
		return envpkg.NewImageStep(nil, "alpine", inputs.ImagePullInput{Type: "alpine", Version: "3.21"})
	}
	return envpkg.NewImageStep(op, "alpine", inputs.ImagePullInput{Type: "alpine", Version: "3.21"})
}

// --- ImageStep.Apply ---
// Rationale: ImageStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestImageStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupAPI       func(t *testing.T) api.ImageAPI
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantImageID    string
		wantWasCreated bool
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.ImageAPI { return nil },
			ctx:      context.Background,
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &ctxMockImageAPI{
					MockImageAPI: testutil.MockImageAPI{
						ImageGetFunc: func(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error) {
							return &model.ImageItem{}, nil
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
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &errorMockImageAPI{
					getErr: errors.New("connection refused"),
				}
			},
			ctx:     context.Background,
			wantErr: "check image type",
		},

		// --- Happy paths AFTER ---

		"image_exists_skips_pull_and_writes_state": {
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &testutil.MockImageAPI{
					ImageGetFunc: func(_ context.Context, _ inputs.ImageInput) (*model.ImageItem, error) {
						return &model.ImageItem{ID: "img-existing"}, nil
					},
				}
			},
			ctx:            context.Background,
			wantImageID:    "img-existing",
			wantWasCreated: false, // WasCreated defaults to false on fresh run
		},
		"image_exists_preserves_was_created_from_saved": {
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &testutil.MockImageAPI{
					ImageGetFunc: func(_ context.Context, _ inputs.ImageInput) (*model.ImageItem, error) {
						return &model.ImageItem{ID: "img-preserved"}, nil
					},
				}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantImageID:    "img-preserved",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newImageStep(t, op)

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
				Output: model.ResourceMap{"image_id": tc.wantImageID},
				Meta:   model.ResourceMeta{WasCreated: tc.wantWasCreated},
			}
			if diff := cmp.Diff(want, written, cmpopts.IgnoreFields(model.ResourceMeta{}, "SpecHash")); diff != "" {
				t.Errorf("written state mismatch (-want +got):\n%s", diff)
			}
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("image:alpine")
			require.True(t, ok, "shared state must contain step output")
			imgState, ok := val.(*envpkg.ImageState)
			require.True(t, ok, "shared state value must be *ImageState")
			assert.Equal(t, tc.wantImageID, imgState.ImageID)
		})
	}
}

// --- ImageStep.Destroy ---
// Rationale: Destroy is a no-op for images (they persist in the DB), but it
// must still handle nil op, write state, and recover saved state from the
// parameter for workflow resumption after a crash.

func TestImageStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupAPI       func(t *testing.T) api.ImageAPI
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantImageID    string
		wantWasCreated bool
	}{
		// --- Error paths FIRST ---

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.ImageAPI { return nil },
			ctx:      context.Background,
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &testutil.MockImageAPI{}
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
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &testutil.MockImageAPI{}
			},
			ctx:   context.Background,
			saved: model.ResourceState{},
		},
		"recovers_saved_state_from_param": {
			setupAPI: func(_ *testing.T) api.ImageAPI {
				return &testutil.MockImageAPI{}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Spec: model.ResourceMap{"image_id": "img-456"},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantImageID:    "img-456",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newImageStep(t, op)

			writer, writes := recordingWriter()
			// Wrap with ctxAwareWriter so cancelled contexts propagate from
			// the write callback, mirroring how ctxImageRepo works for Apply.
			err := step.Destroy(tc.ctx(), tc.saved, ctxAwareWriter(writer), noopProgress)

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
			if tc.wantImageID != "" {
				want.Output = model.ResourceMap{"image_id": tc.wantImageID}
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

// --- ImageStep.StateData ---
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestImageStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{"image_id": "img-123"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Output: model.ResourceMap{"image_id": "img-123"},
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
				step, err = envpkg.Registry["image"].FromState("image", "alpine", saved, nil, dummyOp)
				require.NoError(t, err)
			} else {
				step = newImageStep(t, &testutil.MockImageAPI{})
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- ImageStep.StateData write-failure propagation ---
// Rationale: If the StateWriter returns an error, Apply and Destroy must
// propagate it rather than silently swallowing the persistence failure.

func TestImageStep_Apply_WriteFailure(t *testing.T) {
	step := envpkg.NewImageStep(
		&testutil.MockImageAPI{
			ImageGetFunc: func(_ context.Context, _ inputs.ImageInput) (*model.ImageItem, error) {
				return &model.ImageItem{ID: "img-1", Type: "alpine", IsPresent: true}, nil
			},
		},
		"alpine",
		inputs.ImagePullInput{Type: "alpine", Version: "3.21"},
	)

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

func TestImageStep_Destroy_WriteFailure(t *testing.T) {
	step := newImageStep(t, &testutil.MockImageAPI{})

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
