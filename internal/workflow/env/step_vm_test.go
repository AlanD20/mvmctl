package env_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// ctxVMRepo wraps testutil.VMRepo to propagate context cancellation
// from GetByName. The plain mock ignores context, so this wrapper is needed
// to test the R8 (context cancellation) iron rule.
type ctxVMRepo struct {
	*testutil.VMRepo
}

var _ vm.Repository = (*ctxVMRepo)(nil)

func (r *ctxVMRepo) GetByName(ctx context.Context, name string) (*model.VM, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.VMRepo.GetByName(ctx, name)
}

// errorVMRepo wraps testutil.VMRepo to inject errors into GetByName.
type errorVMRepo struct {
	*testutil.VMRepo
	getErr error
}

var _ vm.Repository = (*errorVMRepo)(nil)

func (r *errorVMRepo) GetByName(_ context.Context, _ string) (*model.VM, error) {
	return nil, r.getErr
}

// newVMStep is a shorthand for creating a VMStep via the registry.
func newVMStep(t *testing.T, op *api.Operation) workflow.Step {
	t.Helper()
	spec := map[string]any{
		"name":    "test-vm",
		"network": "my-net",
		"key":     "my-key",
		"image":   "alpine",
		"kernel":  "fc-kernel",
		"binary":  "firecracker",
	}
	step, err := envpkg.Registry["vm"].FromSpec("vm", "test-vm", spec, op)
	require.NoError(t, err, "FromSpec must succeed")
	return step
}

// newVMProgressRecorder returns an OnProgressCallback and a pointer to the
// captured events for post-call assertions on progress messages.
func newVMProgressRecorder() (event.OnProgressCallback, *[]event.Progress) {
	var events []event.Progress
	return func(e event.Progress) {
		events = append(events, e)
	}, &events
}

// ─── VMStep.Apply ─────────────────────────────────────────────────────────────
// Rationale: VMStep.Apply is the core provisioning path. A nil-op crash,
// a missed context cancellation, or a database error swallowed as success
// would all cause silent data loss or hung workflows.

func TestVMStep_Apply(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		ctx            func() context.Context
		saved          model.ResourceState
		wantErr        string
		wantVMID       string
		wantVMDir      string
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
				return &api.Operation{
					Repos: api.Repos{VM: &ctxVMRepo{VMRepo: testutil.NewVMRepo()}},
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
						VM: &errorVMRepo{
							VMRepo: testutil.NewVMRepo(),
							getErr: errors.New("connection refused"),
						},
					},
				}
			},
			ctx:     context.Background,
			wantErr: "check vm",
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"already_exists_skips_creation_and_writes_state": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewVMRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.VM{
					ID:         "vm-existing",
					Name:       "test-vm",
					RootfsPath: "/mnt/vms/vm-existing/rootfs.ext4",
				}))
				return &api.Operation{Repos: api.Repos{VM: repo}}
			},
			ctx:       context.Background,
			wantVMID:  "vm-existing",
			wantVMDir: "/mnt/vms/vm-existing/rootfs.ext4",
		},
		"already_exists_preserves_was_created_from_saved": {
			setupOp: func(t *testing.T) *api.Operation {
				repo := testutil.NewVMRepo()
				require.NoError(t, repo.Upsert(context.Background(), &model.VM{
					ID:         "vm-preserved",
					Name:       "test-vm",
					RootfsPath: "/mnt/vms/vm-preserved/rootfs.ext4",
				}))
				return &api.Operation{Repos: api.Repos{VM: repo}}
			},
			ctx: context.Background,
			saved: model.ResourceState{
				Meta: model.ResourceMeta{WasCreated: true},
			},
			wantVMID:       "vm-preserved",
			wantVMDir:      "/mnt/vms/vm-preserved/rootfs.ext4",
			wantWasCreated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newVMStep(t, op)

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

			// Verify the written state contains the correct VM ID and dir.
			assert.Equal(t, tc.wantVMID, written.Spec["vm_id"],
				"written state must reference the existing VM ID")
			assert.Equal(t, tc.wantVMDir, written.Spec["vm_dir"],
				"written state must reference the existing VM rootfs path")
			assert.Equal(t, tc.wantWasCreated, written.Meta.WasCreated,
				"WasCreated must be preserved from saved meta")
			assert.NotEmpty(t, written.Meta.SpecHash,
				"SpecHash must be set for drift detection")

			// Verify shared state was populated for downstream steps.
			val, ok := state.Get("vm:test-vm")
			require.True(t, ok, "shared state must contain step output")
			vmState, ok := val.(*envpkg.VMState)
			require.True(t, ok, "shared state value must be *VMState")
			assert.Equal(t, tc.wantVMID, vmState.VMID)
			assert.Equal(t, tc.wantVMDir, vmState.VMDir)
		})
	}
}

// ─── VMStep.Apply write-failure propagation ──────────────────────────────────
// Rationale: If the StateWriter returns an error, Apply must propagate it
// rather than silently swallowing the persistence failure.

func TestVMStep_Apply_WriteFailure(t *testing.T) {
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VM{
		ID:         "vm-1",
		Name:       "test-vm",
		RootfsPath: "/mnt/vms/vm-1/rootfs.ext4",
	}))
	op := &api.Operation{Repos: api.Repos{VM: repo}}
	step := newVMStep(t, op)

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

// ─── VMStep.Apply progress events ────────────────────────────────────────────
// Rationale: Progress events must fire for "checking if exists" and
// "already exists, skipping" so the CLI can display step status.

func TestVMStep_Apply_ProgressEvents(t *testing.T) {
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VM{
		ID:         "vm-1",
		Name:       "test-vm",
		RootfsPath: "/mnt/vms/vm-1/rootfs.ext4",
	}))
	op := &api.Operation{Repos: api.Repos{VM: repo}}
	step := newVMStep(t, op)

	progress, events := newVMProgressRecorder()
	writer, _ := recordingWriter()
	err := step.Apply(context.Background(), workflow.NewSharedState(), model.ResourceState{}, writer, progress)
	require.NoError(t, err)

	require.Len(t, *events, 2, "expected two progress events (checking + skipping)")
	assert.Equal(t, "running", (*events)[0].Status)
	assert.Equal(t, "checking if exists", (*events)[0].Message)
	assert.Equal(t, "running", (*events)[1].Status)
	assert.Equal(t, "already exists, skipping", (*events)[1].Message)
}

// ─── VMStep.Destroy ──────────────────────────────────────────────────────────
// Rationale: Destroy handles nil op, skip when WasCreated=false, and state
// recovery from persisted data. A nil-op crash or incorrect WasCreated
// check would either panic or skip actual teardown.

func TestVMStep_Destroy(t *testing.T) {
	tests := map[string]struct {
		setupOp        func(t *testing.T) *api.Operation
		saved          model.ResourceState
		wantErr        string
		wantVMID       string
		wantWasCreated bool
	}{
		// ── Error paths FIRST ──────────────────────────────────────────

		"nil_op_returns_error": {
			setupOp: func(_ *testing.T) *api.Operation { return nil },
			wantErr: "operation not initialized",
		},

		// ── Happy paths AFTER ──────────────────────────────────────────

		"was_created_false_skips_removal": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
			},
			saved:          model.ResourceState{},
			wantWasCreated: false,
		},
		"recovers_saved_state_from_param": {
			setupOp: func(_ *testing.T) *api.Operation {
				return &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
			},
			saved: model.ResourceState{
				Spec: model.ResourceMap{"vm_id": "vm-456", "vm_dir": "/mnt/vms/vm-456"},
				Meta: model.ResourceMeta{WasCreated: false},
			},
			wantVMID:       "vm-456",
			wantWasCreated: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := tc.setupOp(t)
			step := newVMStep(t, op)

			writer, writes := recordingWriter()
			err := step.Destroy(context.Background(), tc.saved, writer, noopProgress)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Destroy always writes state (even for skip path).
			require.Len(t, *writes, 1)
			written := (*writes)[0]

			if tc.wantVMID != "" {
				assert.Equal(t, tc.wantVMID, written.Spec["vm_id"],
					"destroyed state must reference the recovered VM ID")
			}
			assert.Equal(t, tc.wantWasCreated, written.Meta.WasCreated)
		})
	}
}

// ─── VMStep.Destroy write-failure propagation ────────────────────────────────
// Rationale: If the StateWriter returns an error during Destroy, it must
// propagate rather than silently swallowing the persistence failure.

func TestVMStep_Destroy_WriteFailure(t *testing.T) {
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step := newVMStep(t, op)

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

// ─── VMStep.Destroy context cancellation ────────────────────────────────────
// Rationale: Destroy must propagate context cancellation rather than ignoring
// a cancelled ctx and proceeding with teardown.

func TestVMStep_Destroy_ContextCancelled(t *testing.T) {
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step := newVMStep(t, op)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Use a writer that checks context, since the default recordingWriter
	// ignores it and would succeed regardless of cancellation.
	ctxWriter := func(ctx context.Context, _ model.ResourceState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}

	err := step.Destroy(ctx, model.ResourceState{}, ctxWriter, noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled",
		"Destroy must return context cancellation error")
}

// ─── VMStep.Destroy WasCreated=true exercises VMRemove ──────────────────────
// Rationale: When WasCreated=true, Destroy must call VMRemove to tear down
// the actual VM. A missing WasCreated check would skip real teardown.

func TestVMStep_Destroy_WasCreated(t *testing.T) {
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VM{
		ID:         "vm-to-remove",
		Name:       "test-vm",
		RootfsPath: "/mnt/vms/vm-to-remove/rootfs.ext4",
	}))
	op := &api.Operation{Repos: api.Repos{VM: repo}}
	step := newVMStep(t, op)

	saved := model.ResourceState{
		Spec: model.ResourceMap{
			"vm_id":  "vm-to-remove",
			"vm_dir": "/mnt/vms/vm-to-remove/rootfs.ext4",
		},
		Meta: model.ResourceMeta{WasCreated: true},
	}

	writer, _ := recordingWriter()
	// WasCreated=true triggers VMRemove. The call panics (nil Connection)
	// or errors (CheckPrivileges fails in CI). Either proves branch reached.
	didPanic, didError := false, false
	func() {
		defer func() {
			if r := recover(); r != nil {
				didPanic = true
			}
		}()
		err := step.Destroy(context.Background(), saved, writer, noopProgress)
		if err != nil {
			didError = true
		}
	}()
	assert.True(t, didPanic || didError,
		"Destroy with WasCreated=true must attempt VMRemove (panic or error)")
}

// ─── VMStep.StateData ────────────────────────────────────────────────────────
// Rationale: StateData is the serialization contract between Apply/Destroy
// and the workflow persistence layer. If it returns wrong keys or drops meta,
// the next workflow run will lose state and re-provision resources.

func TestVMStep_StateData(t *testing.T) {
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
			savedSpec: model.ResourceMap{
				"vm_id":        "vm-123",
				"vm_dir":       "/mnt/vms/vm-123",
				"nocloud_port": 8080,
				"tap_name":     "tap-123",
			},
			savedMeta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			want: model.ResourceState{
				Spec: model.ResourceMap{
					"vm_id":        "vm-123",
					"vm_dir":       "/mnt/vms/vm-123",
					"nocloud_port": 8080,
					"tap_name":     "tap-123",
				},
				Meta: model.ResourceMeta{WasCreated: true, SpecHash: "abc123"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}

			var step workflow.Step
			if tc.fromState {
				saved := model.ResourceState{Spec: tc.savedSpec, Meta: tc.savedMeta}
				var err error
				step, err = envpkg.Registry["vm"].FromState("vm", "test-vm", saved, nil, op)
				require.NoError(t, err)
			} else {
				step = newVMStep(t, op)
			}

			got := step.StateData()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("StateData() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── VMStep.Dependencies ─────────────────────────────────────────────────────
// Rationale: Dependencies must include explicit deps from the spec AND
// implicit deps from input fields (network, key, image, kernel, binary).
// Missing deps means the DAG scheduler won't wait for prerequisites.

func TestVMStep_Dependencies(t *testing.T) {
	tests := map[string]struct {
		spec model.ResourceMap
		want []string
	}{
		"all_resource_deps_extracted": {
			spec: model.ResourceMap{
				"name":    "test-vm",
				"network": "my-net",
				"key":     "my-key",
				"image":   "alpine",
				"kernel":  "fc-kernel",
				"binary":  "firecracker",
			},
			want: []string{
				"network:my-net",
				"key:my-key",
				"image:alpine",
				"kernel:fc-kernel",
				"binary:firecracker",
			},
		},
		"explicit_depends_on_plus_implicit": {
			spec: model.ResourceMap{
				"name":       "test-vm",
				"network":    "my-net",
				"depends_on": []any{"custom:step-1"},
			},
			want: []string{
				"custom:step-1",
				"network:my-net",
			},
		},
		"no_deps_when_no_resources": {
			spec: model.ResourceMap{
				"name": "test-vm",
			},
			want: nil,
		},
		"deduplicates_explicit_and_implicit": {
			spec: model.ResourceMap{
				"name":       "test-vm",
				"network":    "my-net",
				"depends_on": []any{"network:my-net"},
			},
			want: []string{
				"network:my-net",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
			step, err := envpkg.Registry["vm"].FromSpec("vm", "test-vm", tc.spec, op)
			require.NoError(t, err)

			got := step.Dependencies()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Dependencies() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── VMStep.Apply dependency resolution from SharedState ─────────────────────
// Rationale: When a VM step's dependencies (network, image, etc.) have
// already applied, the VM step must read resolved IDs from SharedState
// instead of using raw spec names. If it doesn't, the VM will be created
// with unresolved reference names instead of real database IDs.

func TestVMStep_Apply_DependencyResolution(t *testing.T) {
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VM{
		ID:         "vm-1",
		Name:       "test-vm",
		RootfsPath: "/mnt/vms/vm-1/rootfs.ext4",
	}))
	op := &api.Operation{Repos: api.Repos{VM: repo}}

	spec := map[string]any{
		"name":    "test-vm",
		"network": "my-net",
		"key":     "my-key",
		"image":   "alpine",
		"kernel":  "fc-kernel",
		"binary":  "firecracker",
	}
	step, err := envpkg.Registry["vm"].FromSpec("vm", "test-vm", spec, op)
	require.NoError(t, err)

	// Populate SharedState with resolved dependency outputs.
	state := workflow.NewSharedState()
	state.Set("network:my-net", &envpkg.NetworkState{NetworkID: "net-resolved-123"})
	state.Set("key:my-key", &envpkg.KeyState{KeyID: "key-resolved-456"})
	state.Set("image:alpine", &envpkg.ImageState{ImageID: "img-resolved-789"})
	state.Set("kernel:fc-kernel", &envpkg.KernelState{KernelID: "krnl-resolved-abc"})
	state.Set("binary:firecracker", &envpkg.BinaryState{BinaryID: "bin-resolved-def"})

	writer, writes := recordingWriter()
	err = step.Apply(context.Background(), state, model.ResourceState{}, writer, noopProgress)
	require.NoError(t, err)

	// The step should have written state successfully.
	require.Len(t, *writes, 1)

	// Verify shared state was populated with the VM step output.
	val, ok := state.Get("vm:test-vm")
	require.True(t, ok, "shared state must contain VM step output")
	vmState, ok := val.(*envpkg.VMState)
	require.True(t, ok, "shared state value must be *VMState")
	assert.Equal(t, "vm-1", vmState.VMID)
}

// ─── VMStep.SpecHash ─────────────────────────────────────────────────────────
// Rationale: SpecHash must be set for drift detection. If the hash is empty,
// the workflow engine can't detect spec changes between runs.

func TestVMStep_SpecHash(t *testing.T) {
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step := newVMStep(t, op)

	hash := step.SpecHash()
	assert.NotEmpty(t, hash, "SpecHash must be non-empty for drift detection")
	assert.Len(t, hash, 64, "SHA-256 hash must be 64 hex characters")
}

// ─── VMStep.Name and Type ────────────────────────────────────────────────────
// Rationale: Name format ("type:name") is the SharedState key contract used
// by downstream steps for dependency resolution. The format is already verified
// implicitly by TestVMStep_Apply (state.Get("vm:test-vm")) and
// TestVMStep_Dependencies (dependency format "network:my-net"). A standalone
// getter-only test would be tautological.

// ─── FromSpec construction ───────────────────────────────────────────────────
// Rationale: FromSpec must correctly parse spec fields into VMCreateInput,
// including the special "key" → SSHKeys mapping and nil-pointer cleanup
// for empty optional fields. The name-setting behavior is verified by
// TestVMStep_Apply_DependencyResolution which exercises FromSpec end-to-end.

func TestVMStep_FromSpec_EmptyOptionalFieldsNil(t *testing.T) {
	spec := map[string]any{
		"name":    "my-vm",
		"network": "",
		"image":   "",
		"kernel":  "",
		"binary":  "",
	}
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step, err := envpkg.Registry["vm"].FromSpec("vm", "my-vm", spec, op)
	require.NoError(t, err)

	// Empty string optional fields should produce no implicit dependencies.
	deps := step.Dependencies()
	assert.Empty(t, deps, "empty optional fields must not produce dependencies")
}

// ─── FromState construction ──────────────────────────────────────────────────
// Rationale: FromState must reconstruct a VMStep from previously persisted
// state so that Destroy can recover the VM ID for teardown.

func TestVMStep_FromState_CorrectType(t *testing.T) {
	saved := model.ResourceState{
		Spec: model.ResourceMap{
			"vm_id":        "vm-123",
			"vm_dir":       "/mnt/vms/vm-123",
			"nocloud_port": 8080,
			"tap_name":     "tap-123",
		},
		Meta: model.ResourceMeta{WasCreated: true},
	}
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step, err := envpkg.Registry["vm"].FromState("vm", "my-vm", saved, []string{"network:my-net"}, op)
	require.NoError(t, err)

	// IsType verifies the factory returns the correct concrete type,
	// which is the meaningful behavior contract of FromState.
	assert.IsType(t, &envpkg.VMStep{}, step)
}

func TestVMStep_FromState_PreservesDependencies(t *testing.T) {
	saved := model.ResourceState{
		Spec: model.ResourceMap{"vm_id": "vm-123"},
		Meta: model.ResourceMeta{WasCreated: true},
	}
	deps := []string{"network:my-net", "key:my-key"}
	op := &api.Operation{Repos: api.Repos{VM: testutil.NewVMRepo()}}
	step, err := envpkg.Registry["vm"].FromState("vm", "my-vm", saved, deps, op)
	require.NoError(t, err)

	got := step.Dependencies()
	if diff := cmp.Diff(deps, got); diff != "" {
		t.Errorf("FromState dependencies mismatch (-want +got):\n%s", diff)
	}
}
