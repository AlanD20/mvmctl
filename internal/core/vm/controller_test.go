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

// --- Pause state validation ---
// Rationale: Pause must reject invalid state transitions before any I/O.
// These error paths check the VM status before contacting the root API broker.

func TestController_Pause_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status  model.VMStatus
		wantErr string
	}{
		// Error paths — invalid states (FIRST)
		"starting": {status: model.VMStatusStarting, wantErr: "still starting"},
		"stopped":  {status: model.VMStatusStopped, wantErr: "stopped"},
		"stopping": {status: model.VMStatusStopping, wantErr: "shutting down"},
		"error":    {status: model.VMStatusError, wantErr: "error"},
		"crashed":  {status: model.VMStatusCrashed, wantErr: "crashed"},

		// Happy path — idempotent no-op
		"paused_is_noop": {status: model.VMStatusPaused},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0)
			err := ctrl.Pause(context.Background())

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// --- Resume state validation ---
// Rationale: Resume must reject invalid state transitions before any I/O.
// Already-running and already-starting VMs are no-ops.

func TestController_Resume_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status  model.VMStatus
		wantErr string
	}{
		// Error paths — invalid states (FIRST)
		"error":    {status: model.VMStatusError, wantErr: "remove and recreate"},
		"crashed":  {status: model.VMStatusCrashed, wantErr: "remove and recreate"},
		"stopped":  {status: model.VMStatusStopped, wantErr: "use start()"},
		"stopping": {status: model.VMStatusStopping, wantErr: "shutting down"},

		// Happy paths — idempotent no-ops
		"running_is_noop":  {status: model.VMStatusRunning},
		"starting_is_noop": {status: model.VMStatusStarting},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0)
			err := ctrl.Resume(context.Background())

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// --- Start state validation ---
// Rationale: Start must reject invalid state transitions before any I/O.
// Already-running, already-starting, and stopping VMs are no-ops.

func TestController_Start_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status  model.VMStatus
		wantErr string
	}{
		// Error paths — invalid states (FIRST)
		"error":   {status: model.VMStatusError, wantErr: "remove and recreate"},
		"crashed": {status: model.VMStatusCrashed, wantErr: "remove and recreate"},
		"paused":  {status: model.VMStatusPaused, wantErr: "use resume()"},

		// Happy paths — idempotent no-ops
		"running_is_noop":  {status: model.VMStatusRunning},
		"starting_is_noop": {status: model.VMStatusStarting},
		"stopping_is_noop": {status: model.VMStatusStopping},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0)
			err := ctrl.Start(context.Background())

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// --- Context cancellation ---
// Rationale: Idempotent Pause, Resume, and Start must return without contacting
// the root API broker, even when the caller's context is already cancelled.

func TestController_ContextCancellation(t *testing.T) {
	t.Run("Pause_paused_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusPaused, 0)
		err := ctrl.Pause(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Resume_running_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0)
		err := ctrl.Resume(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Start_running_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0)
		err := ctrl.Start(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

}

// --- Helpers ---

// newController creates a Controller with a VM in the given state.
func newController(status model.VMStatus, pid int) *vm.Controller {
	repo := testutil.NewVMRepo()
	m := &model.VMItem{
		ID:     "ctrl-test-vm",
		Name:   "ctrl-test",
		Status: status,
		PID:    pid,
	}
	return vm.NewController(m, repo)
}

// assertDomainError asserts err is a *DomainError with the given Code
// and its Error() contains wantMsg.
func assertDomainError(t *testing.T, err error, wantCode errs.Code, wantMsg string) {
	t.Helper()
	var de *errs.DomainError
	if !errors.As(err, &de) {
		t.Errorf("expected *errs.DomainError, got %T", err)
		return
	}
	if diff := cmp.Diff(wantCode, de.Code); diff != "" {
		t.Errorf("DomainError.Code mismatch (-want +got):\n%s", diff)
	}
	assert.Contains(t, err.Error(), wantMsg)
}
