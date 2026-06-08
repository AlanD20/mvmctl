package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// ── Controller ──
// Matches Python's VMController class exactly.

// Controller manages per-VM lifecycle operations.
// Matches Python's VMController(entity: str | VMInstanceItem, repo: Repository).
type Controller struct {
	vm   *model.VM
	repo Repository
}

// NewController creates a new VM controller.
// Matches Python's VMController.__init__(self, entity: str | VMInstanceItem, repo: Repository).
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	c := &Controller{repo: repo}
	switch e := entity.(type) {
	case *model.VM:
		c.vm = e
	case string:
		resolver := NewResolver(repo)
		resolved, err := resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		c.vm = resolved
	default:
		return nil, fmt.Errorf("invalid entity type: %T (expected *model.VM or string)", entity)
	}
	return c, nil
}

// ── Stop ──
// Matches Python's VMController.stop(force=False) exactly.
//
// Stop is idempotent — never raises.
// If the VM is already stopped or the underlying process is gone,
// returns immediately. If the process exists but cannot be stopped,
// the status is set to ERROR and the method returns cleanly so
// that removal cleanup can still proceed.
//
// Python wraps ALL normal-stop logic in try/except Exception which
// catches *any* exception and sets status to ERROR.  The first two
// code paths (non-running, process-gone) are OUTSIDE the try/except
// in Python — exceptions from those paths propagate to the caller.
func (c *Controller) Stop(ctx context.Context, force bool) error {
	var handler *system.ProcessSignalHandler
	if c.vm.PID > 0 {
		cfg := system.ProcessSignalHandlerConfig{
			PID:     c.vm.PID,
			IsChild: true,
		}
		if c.vm.ProcessStartTime != nil {
			cfg.ExpectedStartTime = c.vm.ProcessStartTime
		}
		handler = system.NewProcessSignalHandler(cfg)
	}

	// ── Non-running VMs: idempotent + orphan cleanup ──
	// The DB status might be STOPPED/PAUSED/ERROR but the actual
	// firecracker process could still be running (e.g., after a
	// failed cleanup or orphaned process from a previous run).
	//
	// Python calls update_status() OUTSIDE the try/except block in this
	// code path, so exceptions must propagate to caller — NOT absorbed.
	if c.vm.Status != model.VMStatusRunning && c.vm.Status != model.VMStatusStarting {
		if c.vm.PID != 0 && handler.IsAlive() {
			handler.Kill()
			if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusStopped); err != nil {
				return err
			}
			c.vm.Status = model.VMStatusStopped
		}
		return nil
	}

	// ── RUNNING/STARTING but process is already gone ──
	// Python calls update_status() OUTSIDE try/except — exception propagates.
	// Python does NOT update in-memory status here (only DB).
	if c.vm.PID == 0 || !handler.IsAlive() {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusStopped); err != nil {
			return err
		}
		return nil
	}

	// ── Normal stop: process is alive, VM is running ──
	// Python wraps ALL of the following in try/except Exception:
	//   try:
	//       update_status(STOPPING)
	//       ... (SendCtrlAltDel, graceful_shutdown, update_exit_code, update_status(STOPPED))
	//   except Exception as exc:
	//       update_status(ERROR)
	//       logger.warning(...)
	//
	// In Python, update_status(STOPPING) is BEFORE the try block — exception propagates.
	// Python does NOT update in-memory status in any normal-stop path (only in DB).
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusStopping); err != nil {
		return err
	}

	stopErr := c.shutdownProcess(ctx, force, handler)
	if stopErr != nil {
		// Python catches ANY exception and sets status to ERROR, then returns
		// None (error absorbed). The error is logged but NOT returned to caller.
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusError); err != nil {
			slog.Warn("Failed to update VM status to ERROR", "error", err)
		}
		slog.Warn("Failed to stop VM", "name", c.vm.Name, "error", stopErr)
		// Return nil — absorb the error like Python does
		return nil
	}
	return nil
}

// shutdownProcess handles the actual Firecracker process shutdown.
// This is the "normal stop" logic from Python's try/except block in Stop().
// Any error returned causes Stop() to set DB status to ERROR.
func (c *Controller) shutdownProcess(ctx context.Context, force bool, handler *system.ProcessSignalHandler) error {
	var exitCode *int

	if !force && c.vm.APISocketPath != "" {
		apiSocket := c.vm.APISocketPath

		// Try graceful shutdown via Firecracker API first
		client := NewFirecrackerClient(apiSocket)
		wasCtrlAltDel, ctrlErr := client.SendCtrlAltDel(ctx)
		client.Close()

		// If Ctrl+Alt+Del was sent, use hook-based wait: the guest OS
		// initiates shutdown, and the hook (returns false) tells the
		// signal handler to wait rather than sending SIGTERM.
		if ctrlErr == nil && wasCtrlAltDel {
			exitCode = handler.GracefulShutdown(func() bool { return false })
		}

		// Fallback: signal-based shutdown (SIGTERM → wait → SIGKILL)
		if exitCode == nil {
			exitCode = handler.GracefulShutdown(nil)
		}
	} else {
		if force {
			handler.Kill()
		}
		exitCode = handler.GracefulShutdown(nil)
	}

	// Capture exit code if not already captured (non-blocking)
	if exitCode == nil {
		exitCode = handler.TryCaptureExit()
	}

	// Persist exit code to database
	if exitCode != nil {
		if err := c.repo.UpdateExitCode(ctx, c.vm.ID, *exitCode); err != nil {
			return err
		}
	}

	// Update status to STOPPED
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusStopped); err != nil {
		return err
	}
	return nil
}

// ── Pause ──
// Matches Python's VMController.pause() exactly.
//
// Pause is idempotent — no-op if already paused.
func (c *Controller) Pause(ctx context.Context) error {
	name := c.vm.Name

	// No-op — already paused
	if c.vm.Status == model.VMStatusPaused {
		return nil
	}

	// Cannot pause from these states
	if c.vm.Status == model.VMStatusStarting {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is still starting — cannot pause (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusStopped {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is stopped — cannot pause (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusStopping {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is shutting down — cannot pause (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return &errs.DomainError{
			Code: errs.CodeVMStateInvalid,
			Op:   "vm.controller",
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — cannot pause (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
			Class: errs.ClassValidation,
		}
	}

	// Valid transition — must be RUNNING
	if c.vm.APISocketPath == "" {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
			Class:   errs.ClassValidation,
		}
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if err := client.PauseVM(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusPaused); err != nil {
		return err
	}
	return nil
}

// ── Resume ──
// Matches Python's VMController.resume() exactly.
func (c *Controller) Resume(ctx context.Context) error {
	name := c.vm.Name

	// No-op — already in or moving toward target state (RUNNING)
	if c.vm.Status == model.VMStatusRunning || c.vm.Status == model.VMStatusStarting {
		return nil
	}

	// Error/crashed state
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return &errs.DomainError{
			Code: errs.CodeVMStateInvalid,
			Op:   "vm.controller",
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
			Class: errs.ClassValidation,
		}
	}

	// Wrong direction — stopped
	if c.vm.Status == model.VMStatusStopped {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is stopped — use start() instead (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}

	// Wrong direction — shutting down
	if c.vm.Status == model.VMStatusStopping {
		return &errs.DomainError{
			Code: errs.CodeVMStateInvalid,
			Op:   "vm.controller",
			Message: fmt.Sprintf(
				"VM '%s' is shutting down — use start() after it stops (current state: %s)",
				name,
				c.vm.Status,
			),
			Class: errs.ClassValidation,
		}
	}

	// Valid transition — must be PAUSED
	if c.vm.APISocketPath == "" {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
			Class:   errs.ClassValidation,
		}
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if err := client.ResumeVM(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); err != nil {
		return err
	}
	return nil
}

// ── Start ──
// Matches Python's VMController.start() exactly.
func (c *Controller) Start(ctx context.Context) error {
	name := c.vm.Name

	// Log nested virt status for observability
	if c.vm.NestedVirt {
		slog.Info("VM has nested virtualization enabled", "name", name)
	}

	// No-op — already in or moving toward target state (RUNNING),
	// or will be stopped soon (retry start after)
	if c.vm.Status == model.VMStatusRunning || c.vm.Status == model.VMStatusStarting ||
		c.vm.Status == model.VMStatusStopping {
		return nil
	}

	// Error/crashed state
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return &errs.DomainError{
			Code: errs.CodeVMStateInvalid,
			Op:   "vm.controller",
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
			Class: errs.ClassValidation,
		}
	}

	// Wrong direction — paused
	if c.vm.Status == model.VMStatusPaused {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is paused — use resume() instead (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}

	// Valid transition — must be STOPPED
	if c.vm.APISocketPath == "" {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
			Class:   errs.ClassValidation,
		}
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if _, err := client.StartInstance(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); err != nil {
		return err
	}
	return nil
}

// ── Reboot ──
// Matches Python's VMController.reboot(force=False).
func (c *Controller) Reboot(ctx context.Context, force bool) error {
	if err := c.Stop(ctx, force); err != nil {
		return err
	}
	return c.Start(ctx)
}

// ── Snapshot ──
// Matches Python's VMController.snapshot(mem_out, state_out) exactly.
// Uses named return (err) so the defer can propagate non-DomainError from the
// resume path, matching Python's finally/except MVMError behavior.
func (c *Controller) Snapshot(ctx context.Context, memOut, stateOut string) (err error) {
	name := c.vm.Name

	// Validate state — snapshot requires RUNNING or PAUSED
	if c.vm.Status == model.VMStatusStarting {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is still starting — cannot snapshot (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusStopped {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is stopped — cannot snapshot (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusStopping {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("VM '%s' is shutting down — cannot snapshot (current state: %s)", name, c.vm.Status),
			Class:   errs.ClassValidation,
		}
	}
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return &errs.DomainError{
			Code: errs.CodeVMStateInvalid,
			Op:   "vm.controller",
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — cannot snapshot (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
			Class: errs.ClassValidation,
		}
	}

	if c.vm.APISocketPath == "" {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("Socket not found for VM '%s'. Must be running with --enable-api-socket", name),
			Class:   errs.ClassValidation,
		}
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := NewFirecrackerClient(apiSocket)
	wasRunning := c.vm.Status == model.VMStatusRunning

	defer func() {
		// Resume if we paused it
		// Python's try/except catches MVMError from BOTH resume_vm AND update_status
		// in the same except block — if either fails (with MVMError), we log a
		// warning and leave the VM in paused state.
		// Only DomainError-equivalent errors are absorbed; unexpected errors
		// propagate to the caller via named return err.
		if wasRunning {
			if resumeErr := client.ResumeVM(ctx); resumeErr != nil {
				var de *errs.DomainError
				if errors.As(resumeErr, &de) {
					slog.Warn("Failed to resume VM after snapshot — leaving in paused state", "name", name)
				} else {
					// Non-DomainError propagates (matches Python's except MVMError:
					// non-MVMError from resume_vm or update_status replaces
					// the original error in the finally block).
					err = resumeErr
				}
			} else if updateErr := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); updateErr != nil {
				var de *errs.DomainError
				if errors.As(updateErr, &de) {
					slog.Warn("Failed to resume VM after snapshot — leaving in paused state", "name", name)
				} else {
					err = updateErr
				}
			} else {
				c.vm.Status = model.VMStatusRunning
			}
		}
		client.Close()
	}()

	// Pause before snapshotting (Firecracker requires VM to be paused)
	if wasRunning {
		if err := client.PauseVM(ctx); err != nil {
			return err
		}
		// Python's update_status() is inside the try block — if it fails,
		// the exception propagates through finally (resume + close).
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusPaused); err != nil {
			return err
		}
		c.vm.Status = model.VMStatusPaused
	}

	_, snapshotErr := client.CreateSnapshot(ctx, memOut, stateOut)
	if snapshotErr != nil {
		return snapshotErr
	}
	return nil
}

// ── LoadSnapshot ──
// Matches Python's VMController.load_snapshot(mem_in, state_in, resume_after=False).
func (c *Controller) LoadSnapshot(ctx context.Context, memIn, stateIn string, resumeAfter bool) error {
	if c.vm.APISocketPath == "" {
		return &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Op:      "vm.controller",
			Message: fmt.Sprintf("Socket not found for VM '%s'. Must be running with --enable-api-socket", c.vm.Name),
			Class:   errs.ClassValidation,
		}
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if _, err := client.LoadSnapshot(ctx, memIn, stateIn, resumeAfter); err != nil {
		return err
	}

	// Update status based on whether VM was resumed
	// Python's update_status() is NOT in a try block — exception propagates.
	if resumeAfter {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); err != nil {
			return err
		}
		c.vm.Status = model.VMStatusRunning
	} else {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusPaused); err != nil {
			return err
		}
		c.vm.Status = model.VMStatusPaused
	}
	return nil
}

// ── AttachVolume ──
// AttachVolume hotplugs a drive into the running Firecracker process and
// persists the config so it survives reboot.
func (c *Controller) AttachVolume(ctx context.Context, driveConfig model.DriveConfig) error {
	apiSocket := c.vm.APISocketPath
	configPath := c.vm.ConfigPath

	// Hotplug into the running Firecracker process
	client := NewFirecrackerClient(apiSocket)
	err := client.PutDrive(ctx, driveConfig)
	client.Close()
	if err != nil {
		return err
	}

	// Persist to the Firecracker config JSON so it survives reboot
	configMgr := NewFirecrackerConfigManager(configPath)
	if err := configMgr.AddDrive(driveConfig); err != nil {
		return err
	}

	slog.Info("Attached volume", "drive_id", driveConfig.DriveID, "vm", c.vm.Name)
	return nil
}

// ── DetachVolume ──
// DetachVolume hot-unplugs a drive from the running Firecracker process and
// removes it from the persisted config.
func (c *Controller) DetachVolume(ctx context.Context, driveID string) error {
	apiSocket := c.vm.APISocketPath
	configPath := c.vm.ConfigPath

	// Call Firecracker API to delete the drive
	client := NewFirecrackerClient(apiSocket)
	err := client.DeleteDrive(ctx, driveID)
	client.Close()
	if err != nil {
		return err
	}

	// Update Firecracker config JSON on disk
	configMgr := NewFirecrackerConfigManager(configPath)
	if _, err := configMgr.RemoveDrive(driveID); err != nil {
		return err
	}

	slog.Info("Detached volume", "drive_id", driveID, "vm", c.vm.Name)
	return nil
}
