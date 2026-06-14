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

// ─── Pause state validation ─────────────────────────────────────────────────
// Rationale: Pause must reject invalid state transitions before any I/O.
// All error paths are pure — they check c.vm.Status and c.vm.APISocketPath
// without touching the Firecracker API or repository.

func TestController_Pause_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status     model.VMStatus
		socketPath string
		wantErr    string
	}{
		// Error paths — invalid states (FIRST)
		"starting":               {status: model.VMStatusStarting, wantErr: "still starting"},
		"stopped":                {status: model.VMStatusStopped, wantErr: "stopped"},
		"stopping":               {status: model.VMStatusStopping, wantErr: "shutting down"},
		"error":                  {status: model.VMStatusError, wantErr: "error"},
		"crashed":                {status: model.VMStatusCrashed, wantErr: "crashed"},
		"running_without_socket": {status: model.VMStatusRunning, wantErr: "no API socket"},

		// Happy path — idempotent no-op
		"paused_is_noop": {status: model.VMStatusPaused},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0, tc.socketPath)
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

// ─── Resume state validation ────────────────────────────────────────────────
// Rationale: Resume must reject invalid state transitions before any I/O.
// Already-running and already-starting VMs are no-ops.

func TestController_Resume_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status     model.VMStatus
		socketPath string
		wantErr    string
	}{
		// Error paths — invalid states (FIRST)
		"error":                 {status: model.VMStatusError, wantErr: "remove and recreate"},
		"crashed":               {status: model.VMStatusCrashed, wantErr: "remove and recreate"},
		"stopped":               {status: model.VMStatusStopped, wantErr: "use start()"},
		"stopping":              {status: model.VMStatusStopping, wantErr: "shutting down"},
		"paused_without_socket": {status: model.VMStatusPaused, wantErr: "no API socket"},

		// Happy paths — idempotent no-ops
		"running_is_noop":  {status: model.VMStatusRunning},
		"starting_is_noop": {status: model.VMStatusStarting},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0, tc.socketPath)
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

// ─── Start state validation ─────────────────────────────────────────────────
// Rationale: Start must reject invalid state transitions before any I/O.
// Already-running, already-starting, and stopping VMs are no-ops.

func TestController_Start_stateValidation(t *testing.T) {
	tests := map[string]struct {
		status     model.VMStatus
		socketPath string
		wantErr    string
	}{
		// Error paths — invalid states (FIRST)
		"error":                  {status: model.VMStatusError, wantErr: "remove and recreate"},
		"crashed":                {status: model.VMStatusCrashed, wantErr: "remove and recreate"},
		"paused":                 {status: model.VMStatusPaused, wantErr: "use resume()"},
		"stopped_without_socket": {status: model.VMStatusStopped, wantErr: "no API socket"},

		// Happy paths — idempotent no-ops
		"running_is_noop":  {status: model.VMStatusRunning},
		"starting_is_noop": {status: model.VMStatusStarting},
		"stopping_is_noop": {status: model.VMStatusStopping},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0, tc.socketPath)
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

// ─── Stop idempotent no-op ──────────────────────────────────────────────────
// Rationale: Stop on non-running VMs with pid=0 returns nil immediately without
// touching the repository or subprocess. Every non-Running/non-Starting status
// with pid <= 0 is idempotent regardless of force flag.

func TestController_Stop_nonRunningWithNoPID(t *testing.T) {
	statuses := []model.VMStatus{
		model.VMStatusStopped,
		model.VMStatusPaused,
		model.VMStatusStopping,
		model.VMStatusError,
		model.VMStatusCrashed,
	}

	// Same result for both force values — pid <= 0 is checked before force
	for _, force := range []bool{false, true} {
		for _, status := range statuses {
			name := string(status)
			if force {
				name += "_force"
			} else {
				name += "_graceful"
			}

			t.Run(name, func(t *testing.T) {
				ctrl := newController(status, 0, "")
				err := ctrl.Stop(context.Background(), force)
				assert.NoError(t, err)
			})
		}
	}
}

// ─── Snapshot state validation ──────────────────────────────────────────────
// Rationale: Snapshot must reject invalid states before any I/O. Only RUNNING
// and PAUSED are valid; all other statuses return DomainError. Missing API
// socket also returns DomainError.

func TestController_Snapshot_stateValidationTable(t *testing.T) {
	tests := map[string]struct {
		status     model.VMStatus
		socketPath string
		wantErr    string
	}{
		// Error paths — invalid states (FIRST)
		"starting":               {status: model.VMStatusStarting, wantErr: "still starting"},
		"stopped":                {status: model.VMStatusStopped, wantErr: "stopped"},
		"stopping":               {status: model.VMStatusStopping, wantErr: "shutting down"},
		"error":                  {status: model.VMStatusError, wantErr: "error"},
		"crashed":                {status: model.VMStatusCrashed, wantErr: "crashed"},
		"running_without_socket": {status: model.VMStatusRunning, wantErr: "Socket not found"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, 0, tc.socketPath)
			err := ctrl.Snapshot(context.Background(), "/tmp/mem", "/tmp/state")

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// ─── LoadSnapshot state validation ──────────────────────────────────────────
// Rationale: LoadSnapshot checks APISocketPath before any I/O. Unlike other
// lifecycle methods, it does not check c.vm.Status — only the socket path.

func TestController_LoadSnapshot_stateValidation(t *testing.T) {
	tests := map[string]struct {
		socketPath string
		wantErr    string
	}{
		// Error path
		"missing_socket": {socketPath: "", wantErr: "Socket not found"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Status does not matter — LoadSnapshot only checks socket
			ctrl := newController(model.VMStatusRunning, 0, tc.socketPath)
			err := ctrl.LoadSnapshot(context.Background(), "/tmp/mem", "/tmp/state", false)

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// ─── Reboot delegation ──────────────────────────────────────────────────────
// Rationale: Reboot calls Stop then Start. With a non-running VM (pid=0),
// Stop returns nil immediately. If socket is missing, Start returns DomainError
// — proving delegation is wired correctly.

func TestController_Reboot_delegation(t *testing.T) {
	tests := map[string]struct {
		status     model.VMStatus
		pid        int
		socketPath string
		wantErr    string
	}{
		// Error propagates from Start when socket is missing
		"stopped_no_socket_propagates": {
			status: model.VMStatusStopped, pid: 0, socketPath: "",
			wantErr: "no API socket",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := newController(tc.status, tc.pid, tc.socketPath)
			err := ctrl.Reboot(context.Background(), false)

			if tc.wantErr != "" {
				require.Error(t, err)
				assertDomainError(t, err, errs.CodeVMStateInvalid, tc.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// ─── Context cancellation ────────────────────────────────────────────────────
// Rationale: Every controller method takes context.Context. The pure state
// validation paths (no-op and socket-missing) must not block on a cancelled
// context. These tests prove that cancellation does NOT affect early-return
// branches — the state check happens before any I/O that would observe ctx.

func TestController_ContextCancellation(t *testing.T) {
	t.Run("Pause_paused_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusPaused, 0, "")
		err := ctrl.Pause(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Resume_running_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.Resume(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Start_running_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.Start(ctx)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Stop_nonRunning_noop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusStopped, 0, "")
		err := ctrl.Stop(ctx, false)
		assert.NoError(t, err, "no-op must not block on cancelled context")
	})

	t.Run("Snapshot_missingSocket", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.Snapshot(ctx, "/mem", "/state")
		require.Error(t, err, "socket check must happen before context check")
		assertDomainError(t, err, errs.CodeVMStateInvalid, "Socket not found")
	})

	t.Run("LoadSnapshot_missingSocket", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.LoadSnapshot(ctx, "/mem", "/state", false)
		require.Error(t, err, "socket check must happen before context check")
		assertDomainError(t, err, errs.CodeVMStateInvalid, "Socket not found")
	})

	t.Run("Reboot_propagates_from_start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusStopped, 0, "")
		err := ctrl.Reboot(ctx, false)
		require.Error(t, err, "Start must check socket before context")
		assertDomainError(t, err, errs.CodeVMStateInvalid, "no API socket")
	})

	t.Run("AttachVolume_empty_socket", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.AttachVolume(ctx, model.DriveConfig{DriveID: "test"})
		require.Error(t, err, "dial on empty socket must fail immediately, not hang")
	})

	t.Run("DetachVolume_empty_socket", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctrl := newController(model.VMStatusRunning, 0, "")
		err := ctrl.DetachVolume(ctx, "test")
		require.Error(t, err, "dial on empty socket must fail immediately, not hang")
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// newController creates a Controller with a VM in the given state.
func newController(status model.VMStatus, pid int, socketPath string) *vm.Controller {
	repo := testutil.NewVMRepo()
	m := &model.VM{
		ID:            "ctrl-test-vm",
		Name:          "ctrl-test",
		Status:        status,
		PID:           pid,
		APISocketPath: socketPath,
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
