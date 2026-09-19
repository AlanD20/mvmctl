package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// SnapshotAPI defines the public interface for snapshot operations.
type SnapshotAPI interface {
	SnapshotCreate(
		ctx context.Context,
		input inputs.SnapshotCreateInput,
		onProgress event.OnProgressCallback,
	) (*model.SnapshotItem, error)
	SnapshotList(ctx context.Context) []*model.SnapshotItem
	SnapshotInspect(ctx context.Context, input inputs.SnapshotInput) (*results.SnapshotInspect, error)
	SnapshotRestore(ctx context.Context, input inputs.SnapshotRestoreInput) ([]*model.VMItem, error)
	SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult
}

// --- SnapshotCreate ---

// SnapshotCreate creates a snapshot from a running VM.
func (op *Operation) SnapshotCreate(
	ctx context.Context,
	input inputs.SnapshotCreateInput,
	onProgress event.OnProgressCallback,
) (*model.SnapshotItem, error) {
	if err := input.Validate(); err != nil {
		return nil, errs.New(errs.CodeSnapshotCreateFailed, err.Error())
	}
	if err := system.CheckPrivileges("/usr/sbin/ip", "create snapshot"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}

	// 1. Resolve VM
	emitProgress(onProgress, "resolve", "running", "Resolving VM...")
	vmItem, err := input.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("vm not found: %s", input.Identifier), err)
	}

	// 2. Enrich VM with needed relations
	emitProgress(onProgress, "enrich", "running", "Enriching VM info...")
	if err := op.Enr.EnrichVM(
		ctx,
		[]*model.VMItem{vmItem},
		"kernel",
		"image",
		"binary",
		"network",
		"vsock",
	); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			"failed to enrich VM with relations", err)
	}

	// 3. Generate snapshot ID
	createdAt := infra.Now()
	snapID := crypto.SnapshotID(vmItem.ID, createdAt)

	// 4. Create snapshot directory
	snapDir := infra.GetSnapshotDir(snapID)
	emitProgress(onProgress, "dir", "running", fmt.Sprintf("Creating snapshot directory: %s", snapDir))
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to create snapshot directory %s", snapDir), err)
	}

	// Cleanup on any failure
	var cleanup bool
	defer func() {
		if cleanup {
			os.RemoveAll(snapDir)
		}
	}()

	// 5. Copy rootfs
	rootfsFile := filepath.Join(snapDir, infra.SnapshotRootfsFilename)
	emitProgress(onProgress, "rootfs", "running", "Copying root filesystem...")
	if err := infra.CopyFile(vmItem.RootfsPath, rootfsFile); err != nil {
		cleanup = true
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to copy rootfs from %s: %v", vmItem.RootfsPath, err), err)
	}

	// Create phantom symlink so the vmstate captures a snapshot-local path
	// instead of the source VM's path. The symlink initially points to the
	// snapshot's own rootfs copy.
	phantomPath := filepath.Join(snapDir, "phantom-rootfs.img")
	if err := os.Symlink(rootfsFile, phantomPath); err != nil {
		cleanup = true
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to create phantom symlink: %v", err), err)
	}

	// 6. Firecracker API operations (pause + snapshot + optionally resume)
	memFile := filepath.Join(snapDir, infra.SnapshotMemoryFilename)
	stateFile := filepath.Join(snapDir, infra.SnapshotStateFilename)

	if vmItem.Status == model.VMStatusRunning || vmItem.Status == model.VMStatusPaused {
		emitProgress(onProgress, "snapshot", "running", "Creating Firecracker snapshot...")
		ctrl := vm.NewController(vmItem, op.Repos.VM)
		if err := ctrl.SnapshotCreate(ctx, model.SnapshotCreateConfig{
			MemFile:           memFile,
			StateFile:         stateFile,
			PauseOnly:         input.Pause,
			PhantomRootfsPath: phantomPath,
			RootfsPath:        vmItem.RootfsPath,
		}); err != nil {
			cleanup = true
			return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
				fmt.Sprintf("failed to create snapshot for VM '%s': %v", vmItem.Name, err), err)
		}
	} else {
		// No API socket — snapshot from stopped VM (just copy rootfs)
		emitProgress(onProgress, "snapshot", "running", "No API socket — snapshot from stopped VM (rootfs only)...")
		emitProgress(onProgress, "info", "running",
			"Snapshot from stopped VM: no memory/state files captured")
	}

	// 7. Construct snapshot name (default: <vm>-<timestamp>)
	snapName := vmItem.Name + "-" + createdAt
	if input.Name != nil && *input.Name != "" {
		snapName = *input.Name
	}

	// 8. Build extra config from VM boot settings
	extraConfig := &model.SnapshotExtraConfig{
		BootArgs:      vmItem.BootArgs,
		LSMFlags:      vmItem.LSMFlags,
		PCIEnabled:    vmItem.PCIEnabled,
		NestedVirt:    vmItem.NestedVirt,
		Console:       vmItem.EnableConsole,
		EnableLogging: vmItem.EnableLogging,
		EnableMetrics: vmItem.EnableMetrics,
		LogLevel:      vmItem.LogLevel,
		CPUConfig:     vmItem.CPUConfig,
		CgroupLimits:  vmItem.CgroupLimits,
	}
	if vmItem.Vsock != nil {
		extraConfig.VsockPort = vmItem.Vsock.Port
		extraConfig.VsockCID = vmItem.Vsock.GuestCID
		extraConfig.VsockToken = vmItem.Vsock.Token
	}

	// 9. Build metadata from enriched VM
	now := infra.Now()
	snapshotItem := &model.SnapshotItem{
		ID:           snapID,
		Name:         snapName,
		SourceVMID:   vmItem.ID,
		SourceVMName: vmItem.Name,
		SnapshotDir:  snapDir,
		MemoryFile:   memFile,
		StateFile:    stateFile,
		RootfsFile:   rootfsFile,
		ImageID:      vmItem.ImageID,
		KernelID:     vmItem.KernelID,
		NetworkID:    vmItem.NetworkID,
		BinaryID:     vmItem.BinaryID,
		VCPUCount:    vmItem.VCPUCount,
		MemSizeMiB:   vmItem.MemSizeMiB,
		DiskSizeMiB:  vmItem.DiskSizeMiB,
		SSHKeys:      vmItem.SSHKeys,
		SSHUser:      vmItem.SSHUser,
		ExtraConfig:  extraConfig,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// 9. Persist to DB
	emitProgress(onProgress, "store", "running", "Persisting snapshot metadata...")
	if err := op.Repos.Snapshot.Upsert(ctx, snapshotItem); err != nil {
		cleanup = true
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to persist snapshot record: %v", err), err)
	}

	// Cleanup is no longer needed on success
	cleanup = false

	op.AuditLog.LogOperation("snapshot.create", map[string]any{
		"snapshot_id": snapID,
		"source_vm":   vmItem.Name,
		"name":        snapName,
	}, "")

	emitProgress(onProgress, "complete", "complete", fmt.Sprintf("Snapshot created: %s", snapID))
	return snapshotItem, nil
}

// --- SnapshotRestore ---

// SnapshotRestore restores one or more VMs from a snapshot.
func (op *Operation) SnapshotRestore(
	ctx context.Context,
	input inputs.SnapshotRestoreInput,
) ([]*model.VMItem, error) {
	if err := input.Validate(); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed, "invalid snapshot restore request", err)
	}
	// Restore must reserve the captured endpoint and admit its backing files through
	// root-owned snapshot authority before a lifecycle launch can be authorized.
	return nil, errs.New(errs.CodeSnapshotRestoreFailed,
		"snapshot restore is unavailable until root-owned snapshot capture and backing authority are implemented")
}

type managedSnapshotArtifacts struct {
	dir    string
	rootfs string
	memory string
	state  string
}

// resolveManagedSnapshotArtifacts derives the closed snapshot layout from the
// trusted cache root and snapshot identity. Stored file paths are descriptive
// metadata and cannot select restore inputs.
func resolveManagedSnapshotArtifacts(cacheDir, snapshotID string) (*managedSnapshotArtifacts, error) {
	if len(snapshotID) != 64 || snapshotID != strings.ToLower(snapshotID) {
		return nil, fmt.Errorf("invalid snapshot identity")
	}
	if _, err := hex.DecodeString(snapshotID); err != nil {
		return nil, fmt.Errorf("invalid snapshot identity: %w", err)
	}
	dir := filepath.Join(cacheDir, "snapshots", snapshotID)
	return &managedSnapshotArtifacts{
		dir:    dir,
		rootfs: filepath.Join(dir, infra.SnapshotRootfsFilename),
		memory: filepath.Join(dir, infra.SnapshotMemoryFilename),
		state:  filepath.Join(dir, infra.SnapshotStateFilename),
	}, nil
}

func jailedRuntimePathForAPI(hostPath string) string {
	if hostPath == "" {
		return ""
	}
	return filepath.Join("/run/mvm", filepath.Base(hostPath))
}

// --- SnapshotList ---

// SnapshotList returns all snapshots.
// Never returns an error (matches VMList pattern) — empty slice on failure.
func (op *Operation) SnapshotList(ctx context.Context) []*model.SnapshotItem {
	items, err := op.Repos.Snapshot.ListAll(ctx)
	if err != nil {
		slog.Error("failed to list snapshots", "error", err)
		return []*model.SnapshotItem{}
	}
	if len(items) > 0 {
		// best-effort enrichment for display purposes
		_ = op.Enr.EnrichSnapshot(ctx, items, "image", "kernel", "network", "binary")
	}
	return items
}

// --- SnapshotInspect ---

// SnapshotInspect returns detailed information about a single snapshot.
func (op *Operation) SnapshotInspect(
	ctx context.Context,
	input inputs.SnapshotInput,
) (*results.SnapshotInspect, error) {
	snaps, err := input.Resolve(ctx, op.Repos.Snapshot)
	if err != nil {
		return nil, err
	}
	if len(snaps) != 1 {
		return nil, fmt.Errorf("expected exactly one snapshot identifier")
	}
	snap := snaps[0]
	// best-effort enrichment for display purposes
	_ = op.Enr.EnrichSnapshot(ctx, []*model.SnapshotItem{snap}, "image", "kernel", "network", "binary")

	return &results.SnapshotInspect{
		Snapshot: results.SnapshotItemInfo{
			ID:           snap.ID,
			Name:         snap.Name,
			SourceVMID:   snap.SourceVMID,
			SourceVMName: snap.SourceVMName,
			BaseDir:      snap.SnapshotDir,
			CreatedAt:    snap.CreatedAt,
		},
		Assets: results.SnapshotAssetsInfo{
			Image:   snap.Image,
			Kernel:  snap.Kernel,
			Network: snap.Network,
			Binary:  snap.Binary,
		},
		Resources: results.SnapshotResourcesInfo{
			VCPU: snap.VCPUCount,
			Mem:  snap.MemSizeMiB,
			Disk: snap.DiskSizeMiB,
		},
	}, nil
}

// --- SnapshotRemove ---

// SnapshotRemove removes one or more snapshots.
// Uses input.Resolve to resolve identifiers (name or ID prefix), then processes
// each resolved snapshot. Returns a BatchResult (matches VMRemove pattern).
func (op *Operation) SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult {
	snapshots, resolveErr := input.Resolve(ctx, op.Repos.Snapshot)

	results := make([]errs.OperationResult, 0, len(input.Identifiers))

	// Report unresolvable identifiers as batch errors
	if resolveErr != nil && len(snapshots) == 0 {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeSnapshotRemoveFailed), Message: resolveErr.Error()},
			},
		}
	}

	for _, snap := range snapshots {
		paths, err := resolveManagedSnapshotArtifacts(op.CacheDir, snap.ID)
		if err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeSnapshotRemoveFailed),
				Item: snap, Message: fmt.Sprintf("invalid managed snapshot identity: %v", err),
			})
			continue
		}

		// Remove only the ID-derived managed snapshot directory. Stored paths
		// are display metadata and cannot select a deletion target.
		if err := os.RemoveAll(paths.dir); err != nil {
			slog.Warn("failed to remove snapshot directory", "dir", paths.dir, "error", err)
		}

		// Remove from DB
		if err := op.Repos.Snapshot.Delete(ctx, snap.ID); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeSnapshotRemoveFailed),
				Item:    snap,
				Message: fmt.Sprintf("failed to delete snapshot record: %v", err),
			})
			continue
		}

		op.AuditLog.LogOperation("snapshot.remove",
			map[string]any{"snapshot_id": snap.ID, "name": snap.Name}, "")
		results = append(results, errs.OperationResult{
			Status: "success", Code: "snapshot.removed",
			Item: snap, Message: fmt.Sprintf("removed snapshot: %s", snap.Name),
		})
	}
	return &errs.BatchResult{Items: results}
}
