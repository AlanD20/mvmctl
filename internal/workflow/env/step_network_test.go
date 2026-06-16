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
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// ctxMockNetworkAPI wraps testutil.MockNetworkAPI to propagate context
// cancellation from NetworkGet. The plain mock ignores context, so this
// wrapper is needed to test the R8 (context cancellation) iron rule.
type ctxMockNetworkAPI struct {
	testutil.MockNetworkAPI
}

func (m *ctxMockNetworkAPI) NetworkGet(ctx context.Context, input inputs.NetworkInput) (*model.Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.MockNetworkAPI.NetworkGet(ctx, input)
}

// errorMockNetworkAPI wraps testutil.MockNetworkAPI to inject errors
// into NetworkGet.
type errorMockNetworkAPI struct {
	testutil.MockNetworkAPI
	getErr error
}

func (m *errorMockNetworkAPI) NetworkGet(_ context.Context, _ inputs.NetworkInput) (*model.Network, error) {
	return nil, m.getErr
}

// newNetworkStep is a shorthand for creating a NetworkStep.
// For nil-op tests, it constructs the step directly via NewNetworkStep.
func newNetworkStep(t *testing.T, op api.NetworkAPI) workflow.Step {
	t.Helper()
	if op == nil {
		return envpkg.NewNetworkStep(nil, "test-net", inputs.NetworkCreateInput{
			Name: "test-net", Subnet: "10.0.0.0/24",
		})
	}
	return envpkg.NewNetworkStep(op, "test-net", inputs.NetworkCreateInput{
		Name: "test-net", Subnet: "10.0.0.0/24",
	})
}

// ─── NetworkStep.Apply ───────────────────────────────────────────────────────
// Rationale: NetworkStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestNetworkStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupAPI       func(t *testing.T) api.NetworkAPI
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantNetworkID  string
		wantSubnet     string
		wantWasCreated bool
	}{
		// ── Error paths FIRST ──────────────────────────────────────────

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.NetworkAPI { return nil },
			ctx:      context.Background,
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &ctxMockNetworkAPI{
					MockNetworkAPI: testutil.MockNetworkAPI{
						NetworkGetFunc: func(_ context.Context, _ inputs.NetworkInput) (*model.Network, error) {
							return &model.Network{}, nil
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
		"getbyname_database_error_wraps_correctly": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &errorMockNetworkAPI{
					getErr: errors.New("connection refused"),
				}
			},
			ctx:     context.Background,
			wantErr: "check network",
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"already_exists_skips_and_writes_state": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{
					NetworkGetFunc: func(_ context.Context, _ inputs.NetworkInput) (*model.Network, error) {
						return &model.Network{
							ID:        "net-existing",
							Name:      "test-net",
							Subnet:    "10.0.0.0/24",
							IsPresent: true,
						}, nil
					},
				}
			},
			ctx:            context.Background,
			wantNetworkID:  "net-existing",
			wantSubnet:     "10.0.0.0/24",
			wantWasCreated: false, // WasCreated defaults to false on fresh run
		},
		"already_exists_preserves_was_created_from_saved": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{
					NetworkGetFunc: func(_ context.Context, _ inputs.NetworkInput) (*model.Network, error) {
						return &model.Network{
							ID:        "net-preserved",
							Name:      "test-net",
							Subnet:    "10.0.1.0/24",
							IsPresent: true,
						}, nil
					},
				}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantNetworkID:  "net-preserved",
			wantSubnet:     "10.0.1.0/24",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newNetworkStep(t, op)

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

			// Verify the written state contains the correct network data.
			assert.Equal(t, tc.wantNetworkID, written.Spec["network_id"],
				"written state must reference the correct network ID")
			assert.Equal(t, tc.wantSubnet, written.Spec["subnet"],
				"written state must reference the correct subnet")
			assert.Equal(t, tc.wantWasCreated, written.Meta.WasCreated,
				"WasCreated must be preserved from saved meta")
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("network:test-net")
			require.True(t, ok, "shared state must contain step output")
			netState, ok := val.(*envpkg.NetworkState)
			require.True(t, ok, "shared state value must be *NetworkState")
			assert.Equal(t, tc.wantNetworkID, netState.NetworkID)
			assert.Equal(t, tc.wantSubnet, netState.Subnet)
		})
	}
}

// ─── NetworkStep.Apply write-failure propagation ─────────────────────────────
// Rationale: If the StateWriter returns an error, Apply must propagate it
// rather than silently swallowing the persistence failure.

func TestNetworkStep_Apply_WriteFailure(t *testing.T) {
	step := newNetworkStep(t, &testutil.MockNetworkAPI{
		NetworkGetFunc: func(_ context.Context, _ inputs.NetworkInput) (*model.Network, error) {
			return &model.Network{
				ID:        "net-1",
				Name:      "test-net",
				Subnet:    "10.0.0.0/24",
				IsPresent: true,
			}, nil
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
	return
}

// ─── NetworkStep.Destroy ─────────────────────────────────────────────────────
// Rationale: Destroy must handle nil op, skip when WasCreated=false (resource
// was pre-existing), skip when saved state is nil, and recover saved state
// from the parameter for workflow resumption after a crash.

func TestNetworkStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupAPI       func(t *testing.T) api.NetworkAPI
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantPanic      bool
		useCtxWriter   bool
		wantNetworkID  string
		wantWasCreated bool
	}{
		// ── Error paths FIRST ──────────────────────────────────────────

		"nil_op_returns_error": {
			setupAPI: func(_ *testing.T) api.NetworkAPI { return nil },
			wantErr:  "operation not initialized",
		},
		"context_cancelled_returns_error": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"network_id": "net-ctx", "subnet": "10.0.0.0/24"},
				Meta: model.ResourceMeta{WasCreated: false},
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			useCtxWriter: true,
			wantErr:      "context canceled",
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"nil_saved_and_empty_state_skips_destroy": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{}
			},
			saved: model.ResourceState{},
		},
		"was_created_false_skips_destroy": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"network_id": "net-123"},
				Meta: model.ResourceMeta{WasCreated: false},
			},
			wantNetworkID:  "net-123",
			wantWasCreated: false,
		},
		"recovers_saved_state_from_param_and_attempts_destroy": {
			setupAPI: func(_ *testing.T) api.NetworkAPI {
				return &testutil.MockNetworkAPI{
					NetworkRemoveFunc: func(_ context.Context, _ inputs.NetworkInput, _ bool) error {
						// NetworkRemove succeeded (no panic)
						return nil
					},
				}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{
					"network_id": "net-456",
					"subnet":     "10.0.0.0/24",
				},
				Meta: model.ResourceMeta{WasCreated: true},
			},
			// WasCreated=true triggers NetworkRemove, which now uses the mock
			wantNetworkID:  "net-456",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupAPI(t)
			step := newNetworkStep(t, op)

			writer, writes := recordingWriter()

			if tc.wantPanic {
				assert.Panics(t, func() {
					_ = step.Destroy(context.Background(), tc.saved, writer, noopProgress)
				}, "Destroy with WasCreated=true must attempt NetworkRemove")
				return
			}

			ctx := context.Background()
			usedWriter := writer
			if tc.ctx != nil {
				ctx = tc.ctx()
			}
			if tc.useCtxWriter {
				usedWriter = ctxWriter(writer)
			}

			err := step.Destroy(ctx, tc.saved, usedWriter, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even on skip paths).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			if tc.wantNetworkID != "" {
				assert.Equal(t, tc.wantNetworkID, written.Spec["network_id"],
					"destroyed state must reference the recovered network ID")
				assert.Equal(t, tc.wantWasCreated, written.Meta.WasCreated)
			}
		})
	}
}

// ─── NetworkStep.Destroy write-failure propagation ───────────────────────────
// Rationale: If the StateWriter returns an error during Destroy's skip path,
// it must propagate the error rather than silently swallowing it.

func TestNetworkStep_Destroy_WriteFailure(t *testing.T) {
	step := newNetworkStep(t, &testutil.MockNetworkAPI{})

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
	return
}

// ─── NetworkStep.StateData ───────────────────────────────────────────────────
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision networks.

func TestNetworkStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{"network_id": "net-123", "subnet": "10.0.0.0/24"},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Spec: model.ResourceMap{"network_id": "net-123", "subnet": "10.0.0.0/24"},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
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
				step, err = envpkg.Registry["network"].FromState("network", "test-net", saved, nil, dummyOp)
				require.NoError(t, err)
			} else {
				step = newNetworkStep(t, &testutil.MockNetworkAPI{})
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── NetworkStep.FromSpec NAT default ────────────────────────────────────────
// Rationale: The network step defaults NATEnabled to true when the "nat" key
// is absent from the spec. This matches the production default and prevents
// silent network misconfiguration if the default changes.

func TestFromSpec_NetworkStep_NATDefault(t *testing.T) {
	dummyOp := &api.Operation{}

	t.Run("nat_key_absent_produces_nonempty_deterministic_hash", func(t *testing.T) {
		spec := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24"}
		step1, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)

		step2, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)

		assert.NotEmpty(t, step1.SpecHash(), "SpecHash must be non-empty")
		assert.Equal(t, step1.SpecHash(), step2.SpecHash(),
			"identical specs must produce identical hashes")
	})

	t.Run("nat_key_false_produces_different_hash", func(t *testing.T) {
		specTrue := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24", "nat": true}
		stepTrue, err := envpkg.Registry["network"].FromSpec("network", "test-net", specTrue, dummyOp)
		require.NoError(t, err)

		specFalse := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24", "nat": false}
		stepFalse, err := envpkg.Registry["network"].FromSpec("network", "test-net", specFalse, dummyOp)
		require.NoError(t, err)

		assert.NotEqual(t, stepTrue.SpecHash(), stepFalse.SpecHash(),
			"nat=false must produce a different SpecHash than nat=true")
	})

	t.Run("spec_hash_is_deterministic", func(t *testing.T) {
		spec := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24", "nat": true}
		step1, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)

		step2, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)

		assert.NotEmpty(t, step1.SpecHash(), "SpecHash must be non-empty")
		assert.Equal(t, step1.SpecHash(), step2.SpecHash(),
			"identical specs must produce identical hashes")
	})
}

// ─── NetworkStep.FromSpec dependencies ───────────────────────────────────────
// Rationale: FromSpec must parse depends_on from the spec so that downstream
// steps (e.g. VM) correctly declare their network dependency.

func TestFromSpec_NetworkStep_Dependencies(t *testing.T) {
	t.Run("no_depends_on_returns_nil", func(t *testing.T) {
		spec := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24"}
		dummyOp := &api.Operation{}
		step, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)
		assert.Nil(t, step.Dependencies(), "no depends_on should return nil")
	})

	t.Run("explicit_depends_on_parsed_correctly", func(t *testing.T) {
		spec := map[string]any{
			"name":       "test-net",
			"subnet":     "10.0.0.0/24",
			"depends_on": []any{"kernel:fc-kernel"},
		}
		dummyOp := &api.Operation{}
		step, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
		require.NoError(t, err)
		require.Len(t, step.Dependencies(), 1)
		assert.Equal(t, "kernel:fc-kernel", step.Dependencies()[0])
	})
}

// ─── NetworkStep.FromSpec SpecHash determinism ───────────────────────────────
// Rationale: Two steps with identical specs must produce identical SpecHash
// values. If the hash is non-deterministic, drift detection will produce
// false positives on every apply.

func TestFromSpec_NetworkStep_SpecHashDeterminism(t *testing.T) {
	spec := map[string]any{"name": "test-net", "subnet": "10.0.0.0/24"}
	dummyOp := &api.Operation{}

	step1, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
	require.NoError(t, err)

	step2, err := envpkg.Registry["network"].FromSpec("network", "test-net", spec, dummyOp)
	require.NoError(t, err)

	assert.Equal(t, step1.SpecHash(), step2.SpecHash(),
		"identical specs must produce identical hashes")
}

// ─── NetworkStep.FromState state recovery ────────────────────────────────────
// Rationale: FromState must correctly recover NetworkState and ResourceMeta
// from a saved ResourceState so that Destroy can determine whether to skip
// (WasCreated=false) or attempt removal (WasCreated=true).

func TestFromState_NetworkStep_StateRecovery(t *testing.T) {
	tests := map[string]struct {
		savedSpec      model.ResourceMap
		savedMeta      model.ResourceMeta
		wantNetworkID  string
		wantSubnet     string
		wantWasCreated bool
	}{
		"was_created_false_recovers_correctly": {
			savedSpec:      model.ResourceMap{"network_id": "net-100", "subnet": "10.0.0.0/24"},
			savedMeta:      model.ResourceMeta{WasCreated: false},
			wantNetworkID:  "net-100",
			wantSubnet:     "10.0.0.0/24",
			wantWasCreated: false,
		},
		"was_created_true_recovers_correctly": {
			savedSpec:      model.ResourceMap{"network_id": "net-200", "subnet": "10.0.1.0/24"},
			savedMeta:      model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			wantNetworkID:  "net-200",
			wantSubnet:     "10.0.1.0/24",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fsDummyOp := &api.Operation{}
			saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
			step, err := envpkg.Registry["network"].FromState("network", "test-net", saved, nil, fsDummyOp)
			require.NoError(t, err)

			got := step.StateData()
			assert.Equal(t, tc.wantNetworkID, got.Spec["network_id"],
				"recovered state must contain correct network_id")
			assert.Equal(t, tc.wantSubnet, got.Spec["subnet"],
				"recovered state must contain correct subnet")
			assert.Equal(t, tc.wantWasCreated, got.Meta.WasCreated,
				"recovered meta must preserve WasCreated")
		})
	}
}
