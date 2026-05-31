package vm

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"mvmctl/internal/infra"
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
		if c.vm.ProcessStartTime != nil {
			handler = system.NewProcessSignalHandler(
				c.vm.PID,
				system.WithIsChild(true),
				system.WithExpectedStartTime(*c.vm.ProcessStartTime),
			)
		} else {
			handler = system.NewProcessSignalHandler(
				c.vm.PID,
				system.WithIsChild(true),
			)
		}
	}

	// ── Non-running VMs: idempotent + orphan cleanup ──
	// The DB status might be STOPPED/PAUSED/ERROR but the actual
	// firecracker process could still be running (e.g., after a
	// failed cleanup or orphaned process from a previous run).
	//
	// Python calls update_status() OUTSIDE the try/except block in this
	// code path, so exceptions must propagate to caller — NOT absorbed.
	if c.vm.Status != model.StatusRunning && c.vm.Status != model.StatusStarting {
		if c.vm.PID != 0 && handler.IsAlive() {
			handler.Kill()
			if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusStopped); err != nil {
				return err
			}
			c.vm.Status = model.StatusStopped
		}
		return nil
	}

	// ── RUNNING/STARTING but process is already gone ──
	// Python calls update_status() OUTSIDE try/except — exception propagates.
	// Python does NOT update in-memory status here (only DB).
	if c.vm.PID == 0 || !handler.IsAlive() {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusStopped); err != nil {
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
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusStopping); err != nil {
		return err
	}

	stopErr := c.normalStop(ctx, force, handler)
	if stopErr != nil {
		// Python catches ANY exception and sets status to ERROR, then returns
		// None (error absorbed). The error is logged but NOT returned to caller.
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusError); err != nil {
			slog.Warn("Failed to update VM status to ERROR", "error", err)
		}
		slog.Warn("Failed to stop VM", "name", c.vm.Name, "error", stopErr)
		// Return nil — absorb the error like Python does
		return nil
	}
	return nil
}

// normalStop contains the normal-stop logic that Python wraps in try/except.
// Any error returned here causes Stop() to set status to ERROR per Python behavior.
func (c *Controller) normalStop(ctx context.Context, force bool, handler *system.ProcessSignalHandler) error {
	var exitCode *int

	if !force && c.vm.APISocketPath != "" {
		// Resolve full path: Python joins vm_dir / api_socket_path
		apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)

		// Try graceful shutdown via Firecracker API first
		client := NewFirecrackerClient(apiSocket)
		wasCtrlAltDel, ctrlErr := client.SendCtrlAltDel(ctx)
		client.Close()

		if ctrlErr != nil {
			// Non-MVMError propagated through — fall through to signal-based
			// shutdown (matches Python's: except Exception: exit_code = None)
		} else if wasCtrlAltDel {
			// Wait for guest OS shutdown via pre_signal_hook
			// pre_signal_hook=lambda: False means "hook handled it, just wait"
			exitCode = handler.GracefulShutdown(func() bool { return false })
		}
		// If wasCtrlAltDel was false (API failed), exitCode stays nil,
		// matching Python's: except Exception: exit_code = None

		if exitCode == nil {
			exitCode = handler.GracefulShutdown(nil)
		}
	} else {
		if force {
			handler.Kill()
		}
		exitCode = handler.GracefulShutdown(nil)
	}

	// Capture exit code if not already captured
	if exitCode == nil {
		exitCode = handler.WaitAndCaptureExit()
	}

	// Persist exit code to database
	if exitCode != nil {
		if err := c.repo.UpdateExitCode(ctx, c.vm.ID, *exitCode); err != nil {
			return err
		}
	}

	// Update status to STOPPED
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusStopped); err != nil {
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
	if c.vm.Status == model.StatusPaused {
		return nil
	}

	// Cannot pause from these states
	if c.vm.Status == model.StatusStarting {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is still starting — cannot pause (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusStopped {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is stopped — cannot pause (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusStopping {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is shutting down — cannot pause (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusError || c.vm.Status == model.StatusCrashed {
		return &ControllerStateError{
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — cannot pause (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		}
	}

	// Valid transition — must be RUNNING
	if c.vm.APISocketPath == "" {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
		}
	}

	// Resolve full path: Python joins vm_dir / api_socket_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if err := client.PauseVM(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusPaused); err != nil {
		return err
	}
	return nil
}

// ── Resume ──
// Matches Python's VMController.resume() exactly.
func (c *Controller) Resume(ctx context.Context) error {
	name := c.vm.Name

	// No-op — already in or moving toward target state (RUNNING)
	if c.vm.Status == model.StatusRunning || c.vm.Status == model.StatusStarting {
		return nil
	}

	// Error/crashed state
	if c.vm.Status == model.StatusError || c.vm.Status == model.StatusCrashed {
		return &ControllerStateError{
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		}
	}

	// Wrong direction — stopped
	if c.vm.Status == model.StatusStopped {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is stopped — use start() instead (current state: %s)", name, c.vm.Status),
		}
	}

	// Wrong direction — shutting down
	if c.vm.Status == model.StatusStopping {
		return &ControllerStateError{
			Message: fmt.Sprintf(
				"VM '%s' is shutting down — use start() after it stops (current state: %s)",
				name,
				c.vm.Status,
			),
		}
	}

	// Valid transition — must be PAUSED
	if c.vm.APISocketPath == "" {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
		}
	}

	// Resolve full path: Python joins vm_dir / api_socket_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if err := client.ResumeVM(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusRunning); err != nil {
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
	if c.vm.Status == model.StatusRunning || c.vm.Status == model.StatusStarting ||
		c.vm.Status == model.StatusStopping {
		return nil
	}

	// Error/crashed state
	if c.vm.Status == model.StatusError || c.vm.Status == model.StatusCrashed {
		return &ControllerStateError{
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		}
	}

	// Wrong direction — paused
	if c.vm.Status == model.StatusPaused {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is paused — use resume() instead (current state: %s)", name, c.vm.Status),
		}
	}

	// Valid transition — must be STOPPED
	if c.vm.APISocketPath == "" {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' has no API socket enabled", name),
		}
	}

	// Resolve full path: Python joins vm_dir / api_socket_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if _, err := client.StartInstance(ctx); err != nil {
		return err
	}

	// Python does NOT update in-memory status here — only DB.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusRunning); err != nil {
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
// Uses named return (err) so the defer can propagate non-MVMError from the
// resume path, matching Python's finally/except MVMError behavior.
func (c *Controller) Snapshot(ctx context.Context, memOut, stateOut string) (err error) {
	name := c.vm.Name

	// Validate state — snapshot requires RUNNING or PAUSED
	if c.vm.Status == model.StatusStarting {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is still starting — cannot snapshot (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusStopped {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is stopped — cannot snapshot (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusStopping {
		return &ControllerStateError{
			Message: fmt.Sprintf("VM '%s' is shutting down — cannot snapshot (current state: %s)", name, c.vm.Status),
		}
	}
	if c.vm.Status == model.StatusError || c.vm.Status == model.StatusCrashed {
		return &ControllerStateError{
			Message: fmt.Sprintf(
				"VM '%s' is in %s state — cannot snapshot (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		}
	}

	if c.vm.APISocketPath == "" {
		return &ControllerStateError{
			Message: fmt.Sprintf("Socket not found for VM '%s'. Must be running with --enable-api-socket", name),
		}
	}

	// Resolve full path: Python joins vm_dir / api_socket_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	client := NewFirecrackerClient(apiSocket)
	wasRunning := c.vm.Status == model.StatusRunning

	defer func() {
		// Resume if we paused it
		// Python's try/except catches MVMError from BOTH resume_vm AND update_status
		// in the same except block — if either fails (with MVMError), we log a
		// warning and leave the VM in paused state.
		// Only MVMError-equivalent errors (Firecracker client / domain errors) are
		// absorbed; unexpected errors propagate to the caller via named return err.
		if wasRunning {
			if resumeErr := client.ResumeVM(ctx); resumeErr != nil {
				if isMVMError(resumeErr) {
					slog.Warn("Failed to resume VM after snapshot — leaving in paused state", "name", name)
				} else {
					// Non-MVMError propagates (matches Python's except MVMError:
					// non-MVMError from resume_vm or update_status replaces
					// the original error in the finally block).
					err = resumeErr
				}
			} else if updateErr := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusRunning); updateErr != nil {
				if isMVMError(updateErr) {
					slog.Warn("Failed to resume VM after snapshot — leaving in paused state", "name", name)
				} else {
					err = updateErr
				}
			} else {
				c.vm.Status = model.StatusRunning
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
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusPaused); err != nil {
			return err
		}
		c.vm.Status = model.StatusPaused
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
		return &ControllerStateError{
			Message: fmt.Sprintf("Socket not found for VM '%s'. Must be running with --enable-api-socket", c.vm.Name),
		}
	}

	// Resolve full path: Python joins vm_dir / api_socket_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	client := NewFirecrackerClient(apiSocket)
	// Python: try: ... finally: client.close()
	defer client.Close()

	if _, err := client.LoadSnapshot(ctx, memIn, stateIn, resumeAfter); err != nil {
		return err
	}

	// Update status based on whether VM was resumed
	// Python's update_status() is NOT in a try block — exception propagates.
	if resumeAfter {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusRunning); err != nil {
			return err
		}
		c.vm.Status = model.StatusRunning
	} else {
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.StatusPaused); err != nil {
			return err
		}
		c.vm.Status = model.StatusPaused
	}
	return nil
}

// ── AttachVolume ──
// Matches Python's VMController.attach_volume(vol).
// Takes `any` because core vm cannot import volume package (architectural constraint).
// Validates that the value has the expected fields at runtime.
//
// NOTE(verdict#2): This is NOT a pure state-transition operation (start/stop/pause/resume).
// It is placed here because the VM controller manages the Firecracker process lifecycle
// and needs to modify drive configs at runtime. A future refactor should move this to the
// service layer when the architectural constraint on core imports is resolved.
func (c *Controller) AttachVolume(ctx context.Context, vol any) error {
	var driveID, path string
	var isReadOnly bool

	switch v := vol.(type) {
	case map[string]any:
		driveID, _ = v["id"].(string)
		path, _ = v["path"].(string)
		isReadOnly, _ = v["is_read_only"].(bool)
		if driveID == "" {
			return fmt.Errorf("AttachVolume: volume must have a non-empty 'id' field")
		}
		if path == "" {
			return fmt.Errorf("AttachVolume: volume must have a non-empty 'path' field")
		}
	default:
		return fmt.Errorf("AttachVolume: invalid volume type %T (expected map with 'id' and 'path')", vol)
	}

	driveConfig := model.DriveConfig{
		DriveID:      driveID,
		PathOnHost:   path,
		IsRootDevice: false,
		IsReadOnly:   isReadOnly,
		CacheType:    "Unsafe",
		IOEngine:     "Sync",
	}

	// Resolve full paths: Python joins vm_dir / api_socket_path and vm_dir / config_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	configPath := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.ConfigPath)

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

	slog.Info("Attached volume", "drive_id", driveID, "vm", c.vm.Name)
	return nil
}

// ── DetachVolume ──
// Matches Python's VMController.detach_volume(vol).
// Takes `any` because core vm cannot import volume package (architectural constraint).
// Validates that the value has the expected fields at runtime.
//
// NOTE(verdict#2): This is NOT a pure state-transition operation (start/stop/pause/resume).
// It is placed here because the VM controller manages the Firecracker process lifecycle
// and needs to modify drive configs at runtime. A future refactor should move this to the
// service layer when the architectural constraint on core imports is resolved.
func (c *Controller) DetachVolume(ctx context.Context, vol any) error {
	var driveID string

	switch v := vol.(type) {
	case map[string]any:
		driveID, _ = v["id"].(string)
		if driveID == "" {
			return fmt.Errorf("DetachVolume: volume must have a non-empty 'id' field")
		}
	default:
		return fmt.Errorf("DetachVolume: invalid volume type %T (expected map with 'id')", vol)
	}

	// Resolve full paths: Python joins vm_dir / api_socket_path and vm_dir / config_path
	apiSocket := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.APISocketPath)
	configPath := filepath.Join(infra.GetVmDir(c.vm.ID), c.vm.ConfigPath)

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

// ── Error type matching Python's VMStateError ──

// ControllerStateError matches Python's VMStateError with exact error messages.
type ControllerStateError struct {
	Message string
}

func (e *ControllerStateError) Error() string {
	return e.Message
}

// IsMVMError marks ControllerStateError as an MVMError subclass for the enricher's
// soft-fail interface check. Matches Python's VMStateError inheriting from VMError
// which inherits from MVMError.
func (e *ControllerStateError) IsMVMError() bool { return true }

// isMVMError checks if an error matches Python's MVMError hierarchy.
// Used to match Python's "except MVMError" catch blocks.
// In Python, FirecrackerClientError, SocketNotFoundError, FirecrackerSpawnError,
// FirecrackerConfigError, ControllerStateError, and DomainError are all subclasses
// of MVMError, so they must also be absorbed, not propagated.
func isMVMError(err error) bool {
	if err == nil {
		return false
	}
	_, isDomain := err.(*errs.DomainError)
	_, isFC := err.(*FirecrackerClientError)
	_, isSocket := err.(*SocketNotFoundError)
	_, isSpawn := err.(*FirecrackerSpawnError)
	_, isConfig := err.(*FirecrackerConfigError)
	_, isState := err.(*ControllerStateError)
	return isDomain || isFC || isSocket || isSpawn || isConfig || isState
}
