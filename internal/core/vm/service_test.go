package vm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Controller: Pause state machine ---
// Rationale: Pause must reject invalid transitions with specific error messages
// and be idempotent when already paused. Error paths cover starting, stopped,
// stopping, error, and crashed states.

func TestController_Pause_stateTransitions(t *testing.T) {
	repo := testutil.NewVMRepo()

	t.Run("already_paused_is_noop", func(t *testing.T) {
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusPaused}
		ctrl := vm.NewController(m, repo)
		assert.NoError(t, ctrl.Pause(context.Background()))
	})

	t.Run("starting_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStarting)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assertCode(t, err, errs.CodeVMStateInvalid)
		assert.Contains(t, err.Error(), "still starting")
	})

	t.Run("stopped_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopped)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stopped")
	})

	t.Run("stopping_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopping)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})

	t.Run("error_state_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusError)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error")
	})

	t.Run("crashed_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusCrashed)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "crashed")
	})

	t.Run("running_no_socket_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusRunning)
		err := ctrl.Pause(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no API socket")
	})
}

// --- Controller: Resume state machine ---
// Rationale: Resume must reject invalid transitions and be idempotent when
// already running.

func TestController_Resume_stateTransitions(t *testing.T) {
	t.Run("already_running_is_noop", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusRunning)
		assert.NoError(t, ctrl.Resume(context.Background()))
	})

	t.Run("starting_is_noop", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStarting)
		assert.NoError(t, ctrl.Resume(context.Background()))
	})

	t.Run("error_state_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusError)
		err := ctrl.Resume(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove and recreate")
	})

	t.Run("crashed_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusCrashed)
		err := ctrl.Resume(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove and recreate")
	})

	t.Run("stopped_rejected_with_start_hint", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopped)
		err := ctrl.Resume(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "use start()")
	})

	t.Run("stopping_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopping)
		err := ctrl.Resume(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})
}

// --- Controller: Start state machine ---
// Rationale: Start must reject invalid transitions and be idempotent when
// already running.

func TestController_Start_stateTransitions(t *testing.T) {
	t.Run("already_running_is_noop", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusRunning)
		assert.NoError(t, ctrl.Start(context.Background()))
	})

	t.Run("starting_is_noop", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStarting)
		assert.NoError(t, ctrl.Start(context.Background()))
	})

	t.Run("stopping_is_noop", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopping)
		assert.NoError(t, ctrl.Start(context.Background()))
	})

	t.Run("error_state_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusError)
		err := ctrl.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove and recreate")
	})

	t.Run("crashed_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusCrashed)
		err := ctrl.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove and recreate")
	})

	t.Run("paused_rejected_with_resume_hint", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusPaused)
		err := ctrl.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "use resume()")
	})
}

// --- Controller: Stop idempotency ---
// Rationale: Stop on non-running VMs (pid=0) must return nil immediately
// without touching the repo. This is the most common error path.

func TestController_Stop_idempotent(t *testing.T) {
	ctx := context.Background()

	for _, status := range []model.VMStatus{
		model.VMStatusStopped,
		model.VMStatusPaused,
		model.VMStatusError,
		model.VMStatusCrashed,
	} {
		t.Run(string(status)+"_noop", func(t *testing.T) {
			repo := testutil.NewVMRepo()
			m := &model.VMItem{ID: "vm-1", Name: "test", Status: status, PID: 0}
			require.NoError(t, repo.Upsert(ctx, m))

			ctrl := vm.NewController(m, repo)
			assert.NoError(t, ctrl.Stop(ctx, false))

			// Verify DB state unchanged
			got, err := repo.Get(ctx, m.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, status, got.Status)
		})
	}
}

// --- Controller: Snapshot state validation ---
// Rationale: Snapshot must reject invalid states with specific messages.

func TestController_Snapshot_stateValidation(t *testing.T) {
	t.Run("starting_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStarting)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "still starting")
	})

	t.Run("stopped_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopped)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stopped")
	})

	t.Run("stopping_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusStopping)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})

	t.Run("error_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusError)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error")
	})

	t.Run("crashed_rejected", func(t *testing.T) {
		ctrl := ctrlFor(model.VMStatusCrashed)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "crashed")
	})

	t.Run("running_no_socket_rejected", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusRunning, APISocketPath: ""}
		ctrl := vm.NewController(m, repo)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Socket not found")
	})

	t.Run("paused_no_socket_rejected", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusPaused, APISocketPath: ""}
		ctrl := vm.NewController(m, repo)
		err := ctrl.Snapshot(context.Background(), "/mem", "/state")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Socket not found")
	})
}

// --- Service: Single-VM delegation ---
// Rationale: Service.Stop/Start/Pause/Resume delegate to Controller.
// Verified by checking state machine rules through Service.

func TestService_Stop(t *testing.T) {
	ctx := context.Background()

	t.Run("stopped_vm_is_noop", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusStopped, PID: 0}
		require.NoError(t, repo.Upsert(ctx, m))

		svc := vm.NewService(repo)
		assert.NoError(t, svc.Stop(ctx, m, false))

		got, _ := repo.Get(ctx, m.ID)
		assert.Equal(t, model.VMStatusStopped, got.Status)
	})

	t.Run("running_pid_zero_is_gone_path", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusRunning, PID: 0, APISocketPath: ""}
		require.NoError(t, repo.Upsert(ctx, m))

		svc := vm.NewService(repo)
		assert.NoError(t, svc.Stop(ctx, m, false))

		got, err := repo.Get(ctx, m.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, model.VMStatusStopped, got.Status)
	})

	t.Run("context_cancelled_before_call", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		m := &model.VMItem{ID: "vm-1", Name: "test", Status: model.VMStatusStopped, PID: 0}
		require.NoError(t, repo.Upsert(ctx, m))

		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		svc := vm.NewService(repo)
		err := svc.Stop(cancelCtx, m, false)
		// Stop on non-running VM returns nil even with cancelled context
		// (the early return path checks status, not context)
		assert.NoError(t, err)
	})
}

// --- Service: Bulk operations ---
// Rationale: StopMany must process all VMs and collect errors.

func TestService_StopMany(t *testing.T) {
	ctx := context.Background()

	t.Run("all_already_stopped", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		vms := []*model.VMItem{
			{ID: "vm-1", Name: "a", Status: model.VMStatusStopped, PID: 0},
			{ID: "vm-2", Name: "b", Status: model.VMStatusPaused, PID: 0},
			{ID: "vm-3", Name: "c", Status: model.VMStatusError, PID: 0},
		}
		for _, v := range vms {
			require.NoError(t, repo.Upsert(ctx, v))
		}

		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, vms, false, false, 0)

		assert.Equal(t, 3, len(result.Items))
		for _, item := range result.Items {
			assert.NoError(t, item.Error)
		}
	})

	t.Run("empty_list", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, []*model.VMItem{}, false, false, 0)
		assert.Empty(t, result.Items)
	})

	t.Run("with_parallelism", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		vms := []*model.VMItem{
			{ID: "vm-1", Name: "a", Status: model.VMStatusStopped, PID: 0},
			{ID: "vm-2", Name: "b", Status: model.VMStatusStopped, PID: 0},
		}
		for _, v := range vms {
			require.NoError(t, repo.Upsert(ctx, v))
		}

		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, vms, false, true, 4)
		assert.Equal(t, 2, len(result.Items))
		for _, item := range result.Items {
			assert.NoError(t, item.Error)
		}
	})
}

// --- Helpers ---

// ctrlFor creates a Controller with a VM in the given status.
// PID=0, no socket — ensures only state-machine paths are hit.
func ctrlFor(status model.VMStatus) *vm.Controller {
	repo := testutil.NewVMRepo()
	m := &model.VMItem{
		ID:            "vm-1",
		Name:          "test",
		Status:        status,
		PID:           0,
		APISocketPath: "",
	}
	return vm.NewController(m, repo)
}

// assertCode checks that err is a DomainError with the given code.
func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		if diff := cmp.Diff(code, de.Code); diff != "" {
			t.Errorf("DomainError.Code mismatch (-want +got):\n%s", diff)
		}
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}
