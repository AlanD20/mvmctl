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
// stopping, error, and crashed states. Active-VM API socket admission belongs
// to the root broker and requires runner-VM qualification.

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

// A local stopped projection requires root cleanup authority even when the
// caller's PID mirror is zero. Invalid selection fails before any root effect.
func TestService_StopRejectsInvalidSelectionWithoutLocalProjection(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewVMRepo()
	vmItem := &model.VMItem{ID: "invalid-vm-id", Name: "test", Status: model.VMStatusRunning, PID: 0}
	require.NoError(t, repo.Upsert(ctx, vmItem))

	err := vm.NewService(repo).Stop(ctx, vmItem, false)
	require.Error(t, err)
	assertCode(t, err, errs.CodeValidationFailed)
	assert.Equal(t, model.VMStatusRunning, vmItem.Status)
	got, getErr := repo.Get(ctx, vmItem.ID)
	require.NoError(t, getErr)
	require.NotNil(t, got)
	assert.Equal(t, model.VMStatusRunning, got.Status)
}

// --- Service: Bulk operations ---
// Rationale: Bulk stop preserves item identity and reports validation rejection
// without projecting local success or invoking root effects.

func TestService_StopMany(t *testing.T) {
	ctx := context.Background()

	t.Run("serial_stops_after_first_rejection", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		vms := []*model.VMItem{
			{ID: "invalid-vm-a", Name: "a", Status: model.VMStatusRunning, PID: 0},
			{ID: "invalid-vm-b", Name: "b", Status: model.VMStatusPaused, PID: 0},
		}
		for _, v := range vms {
			require.NoError(t, repo.Upsert(ctx, v))
		}

		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, vms, false, false, 0)

		require.Len(t, result.Items, len(vms))
		assert.Same(t, vms[0], result.Items[0].Item)
		require.Error(t, result.Items[0].Error)
		assertCode(t, result.Items[0].Error, errs.CodeValidationFailed)
		assert.Nil(t, result.Items[1].Item)
		assert.NoError(t, result.Items[1].Error)
		assert.Equal(t, model.VMStatusRunning, vms[0].Status)
		assert.Equal(t, model.VMStatusPaused, vms[1].Status)
		for index, wantStatus := range []model.VMStatus{model.VMStatusRunning, model.VMStatusPaused} {
			got, getErr := repo.Get(ctx, vms[index].ID)
			require.NoError(t, getErr)
			require.NotNil(t, got)
			assert.Equal(t, wantStatus, got.Status)
		}
	})

	t.Run("empty_list", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, []*model.VMItem{}, false, false, 0)
		assert.Empty(t, result.Items)
	})

	t.Run("parallel_collects_each_rejection", func(t *testing.T) {
		repo := testutil.NewVMRepo()
		vms := []*model.VMItem{
			{ID: "invalid-vm-a", Name: "a", Status: model.VMStatusRunning, PID: 0},
			{ID: "invalid-vm-b", Name: "b", Status: model.VMStatusPaused, PID: 0},
		}
		for _, v := range vms {
			require.NoError(t, repo.Upsert(ctx, v))
		}

		svc := vm.NewService(repo)
		result := svc.StopMany(ctx, vms, false, true, 4)
		require.Len(t, result.Items, len(vms))
		for index, item := range result.Items {
			assert.Same(t, vms[index], item.Item)
			require.Error(t, item.Error)
			assertCode(t, item.Error, errs.CodeValidationFailed)
			got, getErr := repo.Get(ctx, vms[index].ID)
			require.NoError(t, getErr)
			require.NotNil(t, got)
			wantStatus := []model.VMStatus{model.VMStatusRunning, model.VMStatusPaused}[index]
			assert.Equal(t, wantStatus, got.Status)
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
