package vm

import (
	"context"
	"fmt"
	"log/slog"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// --- Controller ---

// Controller manages per-VM lifecycle operations.
type Controller struct {
	vm   *model.VMItem
	repo Repository
}

// NewController creates a new VM controller.
func NewController(vm *model.VMItem, repo Repository) *Controller {
	return &Controller{vm: vm, repo: repo}
}

// --- Stop ---
//
// Stop completes root-authoritative cleanup before projecting STOPPED locally.
// Unavailable or ambiguous authority is an error, never permission to signal a PID mirror.
func (c *Controller) Stop(ctx context.Context, force bool) error {
	if err := CleanupJail(ctx, c.vm.ID); err != nil {
		return err
	}
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusStopped); err != nil {
		return err
	}
	c.vm.Status = model.VMStatusStopped
	return nil
}

// --- Pause ---
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
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is still starting — cannot pause (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusStopped {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is stopped — cannot pause (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusStopping {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is shutting down — cannot pause (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is in %s state — cannot pause (current state: %s)", name, c.vm.Status, c.vm.Status))
	}

	// Valid transition — must be RUNNING
	if c.vm.APISocketPath == "" {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' has no API socket enabled", name))
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := firecracker.NewClient(apiSocket)
	defer client.Close()

	if err := client.PauseVM(ctx); err != nil {
		return err
	}

	// Only update DB, not in-memory status.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusPaused); err != nil {
		return err
	}
	return nil
}

// --- Resume ---
func (c *Controller) Resume(ctx context.Context) error {
	name := c.vm.Name

	// No-op — already in or moving toward target state (RUNNING)
	if c.vm.Status == model.VMStatusRunning || c.vm.Status == model.VMStatusStarting {
		return nil
	}

	// Error/crashed state
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return errs.New(
			errs.CodeVMStateInvalid,
			fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		)
	}

	// Wrong direction — stopped
	if c.vm.Status == model.VMStatusStopped {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is stopped — use start() instead (current state: %s)", name, c.vm.Status))
	}

	// Wrong direction — shutting down
	if c.vm.Status == model.VMStatusStopping {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is shutting down — use start() after it stops (current state: %s)", name, c.vm.Status))
	}

	// Valid transition — must be PAUSED
	if c.vm.APISocketPath == "" {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' has no API socket enabled", name))
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := firecracker.NewClient(apiSocket)
	defer client.Close()

	if err := client.ResumeVM(ctx); err != nil {
		return err
	}

	// Only update DB, not in-memory status.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); err != nil {
		return err
	}
	return nil
}

// --- Start ---
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
		return errs.New(
			errs.CodeVMStateInvalid,
			fmt.Sprintf(
				"VM '%s' is in %s state — remove and recreate (current state: %s)",
				name,
				c.vm.Status,
				c.vm.Status,
			),
		)
	}

	// Wrong direction — paused
	if c.vm.Status == model.VMStatusPaused {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is paused — use resume() instead (current state: %s)", name, c.vm.Status))
	}

	// Valid transition — must be STOPPED
	if c.vm.APISocketPath == "" {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' has no API socket enabled", name))
	}

	// APISocketPath is already a full path from DB
	apiSocket := c.vm.APISocketPath
	client := firecracker.NewClient(apiSocket)
	defer client.Close()

	if _, err := client.StartInstance(ctx); err != nil {
		return err
	}

	// Only update DB, not in-memory status.
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); err != nil {
		return err
	}
	return nil
}

// --- Reboot ---
func (c *Controller) Reboot(ctx context.Context, force bool) error {
	if err := c.Stop(ctx, force); err != nil {
		return err
	}
	return c.Start(ctx)
}

// --- SnapshotCreate ---
// Creates a Firecracker snapshot. If pauseOnly is true and the VM was running,
// it stays paused after the snapshot. If pauseOnly is false, the VM is resumed.
func (c *Controller) SnapshotCreate(ctx context.Context, cfg model.SnapshotCreateConfig) (err error) {
	name := c.vm.Name
	// Validate state — snapshot requires RUNNING or PAUSED
	if c.vm.Status == model.VMStatusStarting {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is still starting — cannot snapshot (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusStopped {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is stopped — cannot snapshot (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusStopping {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is shutting down — cannot snapshot (current state: %s)", name, c.vm.Status))
	}
	if c.vm.Status == model.VMStatusError || c.vm.Status == model.VMStatusCrashed {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("VM '%s' is in %s state — cannot snapshot (current state: %s)", name, c.vm.Status, c.vm.Status))
	}

	if c.vm.APISocketPath == "" {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("Socket not found for VM '%s'. Must be running with --enable-api-socket", name))
	}
	if cfg.SnapshotDir != "" {
		if err := ExposeSnapshot(ctx, c.vm.ID, cfg.SnapshotDir); err != nil {
			slog.Error("failed to expose snapshot directory inside VM jail", "vm", name, "error", err)
			return errs.WrapMsg(errs.CodeSnapshotCreateFailed,
				"failed to expose snapshot directory inside VM jail", err)
		}
	}

	apiSocket := c.vm.APISocketPath
	client := firecracker.NewClient(apiSocket)
	wasRunning := c.vm.Status == model.VMStatusRunning

	defer func() {
		// Resume if we paused it and caller didn't want pause-only.
		if wasRunning && !cfg.PauseOnly {
			if resumeErr := client.ResumeVM(ctx); resumeErr != nil {
				if _, ok := errs.AsType[*errs.DomainError](resumeErr); ok {
					slog.Warn("Failed to resume VM after snapshot — leaving in paused state", "name", name)
				} else {
					err = resumeErr
				}
			} else if updateErr := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusRunning); updateErr != nil {
				if _, ok := errs.AsType[*errs.DomainError](updateErr); ok {
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
		if err := c.repo.UpdateStatus(ctx, c.vm.ID, model.VMStatusPaused); err != nil {
			return err
		}
		c.vm.Status = model.VMStatusPaused
	}

	// PATCH drive to phantom path so the vmstate captures a snapshot-local
	// path instead of the source VM's path.
	if cfg.PhantomRootfsPath != "" {
		if patchErr := client.UpdateDrivePath(ctx, "rootfs", cfg.PhantomRootfsPath); patchErr != nil {
			return errs.WrapMsg(errs.CodeSnapshotCreateFailed,
				fmt.Sprintf("failed to patch drive to phantom path: %v", patchErr), patchErr)
		}
	}

	// Defer: restore original drive path after snapshot (runs before resume defer).
	if cfg.PhantomRootfsPath != "" {
		defer func() {
			if restoreErr := client.UpdateDrivePath(ctx, "rootfs", cfg.RootfsPath); restoreErr != nil {
				slog.Warn("Failed to restore drive path after snapshot",
					"vm", name, "error", restoreErr)
			}
		}()
	}

	_, snapshotErr := client.CreateSnapshot(ctx, cfg.MemFile, cfg.StateFile)
	if snapshotErr != nil {
		return snapshotErr
	}
	return nil
}

// --- SnapshotRestore ---
// Loads a snapshot into Firecracker (must already be spawned in snapshot mode
// with a ready API socket). Handles: LoadSnapshot with network override,
// vsock reconfiguration, and status update.
func (c *Controller) SnapshotRestore(ctx context.Context, cfg model.SnapshotRestoreConfig) error {
	if c.vm.APISocketPath == "" {
		return errs.New(errs.CodeVMStateInvalid,
			fmt.Sprintf("Socket not found for VM '%s'. Firecracker must be spawned before loading snapshot", c.vm.Name))
	}

	client := firecracker.NewClient(c.vm.APISocketPath)
	defer client.Close()
	vsockOverride := snapshotVsockOverride(cfg.VsockEnabled)

	// Map the VM's TAP device to eth0 via network_overrides so Firecracker
	// doesn't look for the original TAP name from the snapshot vmstate.
	if _, err := client.LoadSnapshot(
		ctx,
		cfg.MemFile,
		cfg.StateFile,
		cfg.Resume,
		cfg.NetworkOverrides,
		vsockOverride,
	); err != nil {
		return err
	}

	// NOTE: No PutVsock call needed here. vsock_override in LoadSnapshot
	// already sets the correct host-side UDS path. The guest CID is preserved
	// from the vmstate and matches what the guest agent uses. PutVsock is
	// pre-boot only — calling it on a resumed VM would fail or reset the
	// vsock device, breaking the guest agent's existing connection.

	// Update status
	if cfg.Resume {
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

func snapshotVsockOverride(enabled bool) string {
	if !enabled {
		return ""
	}
	return jailedRuntimePath(infra.VMVsockSocketFilename)
}

// --- AttachVolume ---
// AttachVolume hotplugs a drive into the running Firecracker process and
// persists the config so it survives reboot.
func (c *Controller) AttachVolume(ctx context.Context, driveConfig model.DriveConfig) error {
	apiSocket := c.vm.APISocketPath
	configPath := c.vm.ConfigPath

	if c.vm.JailerBinaryID != "" {
		if err := exposeVolume(ctx, c.vm.ID, driveConfig); err != nil {
			slog.Error("failed to expose volume inside VM jail", "vm", c.vm.Name, "error", err)
			return err
		}
		driveConfig.PathOnHost = jailedVolumePath(driveConfig.DriveID)
	}
	// Hotplug into the running Firecracker process
	client := firecracker.NewClient(apiSocket)
	err := client.PutDrive(ctx, driveConfig)
	client.Close()
	if err != nil {
		if c.vm.JailerBinaryID != "" {
			if cleanupErr := removeVolume(context.Background(), c.vm.ID, driveConfig.DriveID); cleanupErr != nil {
				slog.Warn("failed to remove volume mount after hotplug failure", "vm", c.vm.Name, "error", cleanupErr)
			}
		}
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

// --- DetachVolume ---
// DetachVolume hot-unplugs a drive from the running Firecracker process and
// removes it from the persisted config.
func (c *Controller) DetachVolume(ctx context.Context, driveID string) error {
	apiSocket := c.vm.APISocketPath
	configPath := c.vm.ConfigPath

	// Call Firecracker API to delete the drive
	client := firecracker.NewClient(apiSocket)
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
	if c.vm.JailerBinaryID != "" {
		if err := removeVolume(ctx, c.vm.ID, driveID); err != nil {
			slog.Error("failed to remove volume from VM jail", "vm", c.vm.Name, "drive_id", driveID, "error", err)
			return err
		}
	}

	slog.Info("Detached volume", "drive_id", driveID, "vm", c.vm.Name)
	return nil
}
