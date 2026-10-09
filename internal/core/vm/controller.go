package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
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
	if _, err := jailersvc.NewVMLifecycleClient().
		Stop(ctx, jailersvc.VMStopRequest{VMID: c.vm.ID, Force: force}); err != nil {
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

	if _, err := jailersvc.NewVMAPIBrokerClient().Pause(ctx, jailersvc.VMAPIRequest{VMID: c.vm.ID}); err != nil {
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
	return c.resume(ctx, jailersvc.NewVMAPIBrokerClient())
}

type vmResumeBroker interface {
	Resume(context.Context, jailersvc.VMAPIRequest) (jailersvc.VMAPIResult, error)
}

func (c *Controller) resume(ctx context.Context, broker vmResumeBroker) error {
	name := c.vm.Name

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

	// The cached status cannot authorize a no-op while root export recovery is pending.
	if _, err := broker.Resume(ctx, jailersvc.VMAPIRequest{VMID: c.vm.ID}); err != nil {
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

	if _, err := jailersvc.NewVMAPIBrokerClient().
		StartInstance(ctx, jailersvc.VMAPIRequest{VMID: c.vm.ID}); err != nil {
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
func (c *Controller) SnapshotCreate(
	ctx context.Context,
	cfg model.SnapshotCreateConfig,
) (model.SnapshotCaptureResult, error) {
	result, err := jailersvc.NewVMSnapshotClient().Capture(ctx, jailersvc.VMSnapshotCaptureRequest{
		CacheRoot: cfg.CacheRoot, SnapshotID: cfg.SnapshotID, VMID: c.vm.ID, PauseOnly: cfg.PauseOnly,
	})
	if err != nil {
		slog.Error("failed to capture trusted VM snapshot", "vm", c.vm.ID, "error", err)
		var domain *errs.DomainError
		if errors.As(err, &domain) && domain.Details["vm_state"] == "paused" {
			if updateErr := c.repo.UpdateStatus(
				context.WithoutCancel(ctx),
				c.vm.ID,
				model.VMStatusPaused,
			); updateErr != nil {
				return result, errors.Join(err, updateErr)
			}
			c.vm.Status = model.VMStatusPaused
		}
		return result, err
	}
	status := model.VMStatusStopped
	if result.Mode == "full" {
		status = model.VMStatusRunning
		if result.Paused {
			status = model.VMStatusPaused
		}
	}
	if err := c.repo.UpdateStatus(ctx, c.vm.ID, status); err != nil {
		return result, err
	}
	c.vm.Status = status
	return result, nil
}

// --- SnapshotRestore ---
// Loads a snapshot into Firecracker (must already be spawned in snapshot mode
// with a ready API socket). Handles: LoadSnapshot with network override,
// vsock reconfiguration, and status update.
func (c *Controller) SnapshotRestore(
	ctx context.Context,
	cfg model.SnapshotRestoreConfig,
) (model.SnapshotRestoreResult, error) {
	result, err := jailersvc.NewVMSnapshotClient().Restore(ctx, jailersvc.VMSnapshotRestoreRequest{
		CacheRoot: cfg.CacheRoot, SnapshotID: cfg.SnapshotID, VMID: c.vm.ID, Resume: cfg.Resume,
	})
	if err != nil {
		slog.Error("failed to restore trusted VM snapshot", "vm", c.vm.ID, "error", err)
		return result, err
	}
	c.vm.PID = result.PID
	started := int64(result.StartTimeTicks)
	c.vm.ProcessStartTime = &started
	c.vm.Status = model.VMStatusPaused
	if !result.Configuration.Paused {
		c.vm.Status = model.VMStatusRunning
	}
	c.vm.IPv4, c.vm.MAC, c.vm.NetworkID = result.Configuration.GuestIPv4, result.Configuration.GuestMAC,
		model.VMNetworkID(result.Configuration.NetworkID)
	c.vm.NetworkPlacement = result.Configuration.NetworkPlacement
	c.vm.VCPUCount, c.vm.MemSizeMiB = result.Configuration.VCPUCount, result.Configuration.MemSizeMiB
	c.vm.CgroupLimits = result.Configuration.CgroupLimits
	c.vm.KernelID = result.Configuration.KernelID
	c.vm.PCIEnabled, c.vm.NestedVirt = result.Configuration.PCIEnabled, result.Configuration.NestedVirt
	c.vm.CPUConfig = result.Configuration.CPUConfig
	c.vm.VolumeIDs = nil
	for _, volume := range result.Volumes {
		c.vm.VolumeIDs = append(c.vm.VolumeIDs, volume.ID)
	}
	if err := c.repo.Upsert(ctx, c.vm); err != nil {
		return result, err
	}
	return result, nil
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
	cacheRoot := filepath.Dir(filepath.Dir(driveConfig.PathOnHost))
	if driveConfig.PathOnHost != filepath.Join(cacheRoot, "volumes", driveConfig.DriveID+".raw") ||
		driveConfig.IsRootDevice || driveConfig.Partuuid != nil || driveConfig.RateLimiter != nil || driveConfig.Socket != nil ||
		driveConfig.IOEngine != "Sync" {
		return errs.New(errs.CodeValidationFailed, "volume attach requires one canonical managed raw volume")
	}
	stat, err := os.Stat(driveConfig.PathOnHost)
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "inspect managed volume size", err)
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 {
		return errs.New(errs.CodeValidationFailed, "volume attach requires a positive regular raw volume")
	}
	_, err = c.AttachManagedVolume(ctx, cacheRoot, &model.VolumeItem{
		ID: driveConfig.DriveID, Format: model.VolumeFormatRaw, SizeBytes: stat.Size(),
		IsReadOnly: driveConfig.IsReadOnly, CacheType: driveConfig.CacheType,
	})
	return err
}

// AttachManagedVolume selects the durable logical volume without requiring its
// original caller-owned import file to remain present after first protection.
func (c *Controller) AttachManagedVolume(
	ctx context.Context, cacheRoot string, volume *model.VolumeItem,
) (jailersvc.VMVolumeResult, error) {
	if volume == nil || volume.SizeBytes <= 0 {
		return jailersvc.VMVolumeResult{}, errs.New(errs.CodeValidationFailed, "managed volume selection is incomplete")
	}
	result, err := jailersvc.NewVMLifecycleClient().AttachVolume(ctx, jailersvc.VMVolumeAttachRequest{
		VMID: c.vm.ID, CacheRoot: cacheRoot, Volume: jailersvc.VMLaunchVolume{
			ID: volume.ID, Format: volume.Format, ExpectedBytes: uint64(volume.SizeBytes),
			ReadOnly: volume.IsReadOnly, CacheType: volume.CacheType,
		},
	})
	if err != nil {
		slog.Error("failed to attach root-authorized volume", "vm", c.vm.Name, "volume", volume.ID, "error", err)
	}
	return result, err
}

// --- DetachVolume ---
// DetachVolume hot-unplugs a drive from the running Firecracker process and
// removes it from the persisted config.
func (c *Controller) DetachVolume(ctx context.Context, observed jailersvc.VMVolumeObservation) error {
	_, err := jailersvc.NewVMLifecycleClient().
		DetachVolume(ctx, jailersvc.VMVolumeDetachRequest{VMID: c.vm.ID, VolumeID: observed.VolumeID, Observed: observed})
	if err != nil {
		slog.Error(
			"failed to detach root-authorized volume",
			"vm",
			c.vm.Name,
			"volume",
			observed.VolumeID,
			"error",
			err,
		)
	}
	return err
}

// ObserveVolume reads the selected root-owned attachment without mutation.
func (c *Controller) ObserveVolume(ctx context.Context, volumeID string) (jailersvc.VMVolumeObservation, error) {
	return jailersvc.NewVMLifecycleClient().
		ObserveVolume(ctx, jailersvc.VMVolumeObserveRequest{VMID: c.vm.ID, VolumeID: volumeID})
}
