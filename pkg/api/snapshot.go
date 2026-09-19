package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
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
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed, "invalid snapshot create request", err)
	}
	vmItem, err := input.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	createdAt := infra.Now()
	snapshotID := crypto.SnapshotID(vmItem.ID, createdAt)
	emitProgress(onProgress, "snapshot", "running", "Capturing trusted VM state and protected disks...")
	capture, err := vm.NewController(vmItem, op.Repos.VM).SnapshotCreate(ctx, model.SnapshotCreateConfig{
		CacheRoot: op.CacheDir, SnapshotID: snapshotID, PauseOnly: input.Pause,
	})
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed, "trusted snapshot capture failed", err)
	}
	name := vmItem.Name + "-" + createdAt
	if input.Name != nil && *input.Name != "" {
		name = *input.Name
	}
	directory := filepath.Join(op.CacheDir, ".mvm-protected", "snapshots", snapshotID)
	item := &model.SnapshotItem{
		ID: snapshotID, Name: name, SourceVMID: capture.SourceVMID, SourceVMName: vmItem.Name,
		SnapshotDir: directory, RootfsFile: filepath.Join(directory, "rootfs.img"),
		KernelID: vmItem.KernelID, ImageID: vmItem.ImageID, BinaryID: vmItem.BinaryID,
		NetworkID: capture.NetworkID, VCPUCount: capture.VCPUCount, MemSizeMiB: capture.MemSizeMiB,
		DiskSizeMiB: int(capture.RootfsBytes >> 20), SSHKeys: vmItem.SSHKeys, SSHUser: vmItem.SSHUser,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if capture.Mode == "full" {
		item.MemoryFile = filepath.Join(directory, "memory")
		item.StateFile = filepath.Join(directory, "vmstate")
	}
	// Root publication is already committed. A local database failure must not destroy
	// the only durable capture; the exact snapshot ID remains available for recovery.
	if err := op.Repos.Snapshot.Upsert(ctx, item); err != nil {
		return item, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			"snapshot "+snapshotID+" was captured but its local projection could not be stored", err)
	}
	op.AuditLog.LogOperation("snapshot.create", map[string]any{
		"snapshot_id": snapshotID, "source_vm": vmItem.Name, "name": name,
	}, "")
	emitProgress(onProgress, "complete", "complete", "Snapshot created: "+snapshotID)
	return item, nil
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
		if len(input.Identifiers) != 1 {
			return nil, err
		}
		if _, identityErr := resolveManagedSnapshotArtifacts(op.CacheDir, input.Identifiers[0]); identityErr != nil {
			return nil, err
		}
		projection, rootErr := jailersvc.NewVMSnapshotClient().Inspect(ctx, jailersvc.VMSnapshotRequest{
			CacheRoot: op.CacheDir, SnapshotID: input.Identifiers[0],
		})
		if rootErr != nil {
			return nil, rootErr
		}
		snaps = []*model.SnapshotItem{{ID: projection.SnapshotID, Name: projection.SnapshotID,
			SourceVMID: projection.SourceVMID, VCPUCount: projection.VCPUCount, MemSizeMiB: projection.MemSizeMiB,
			DiskSizeMiB: int(projection.RootfsBytes >> 20), NetworkID: projection.NetworkID}}
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
	// Canonical IDs remain removable after root publication or staging succeeded but
	// local bookkeeping failed. Root still authenticates the exact protected entry.
	for _, identifier := range input.Identifiers {
		if _, err := resolveManagedSnapshotArtifacts(op.CacheDir, identifier); err != nil {
			continue
		}
		found := false
		for _, snapshot := range snapshots {
			if snapshot.ID == identifier {
				found = true
				break
			}
		}
		if !found {
			snapshots = append(snapshots, &model.SnapshotItem{ID: identifier, Name: identifier})
		}
	}

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
		if _, err := jailersvc.NewVMSnapshotClient().Remove(ctx, jailersvc.VMSnapshotRequest{
			CacheRoot: op.CacheDir, SnapshotID: snap.ID,
		}); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeSnapshotRemoveFailed), Item: snap,
				Message: "protected snapshot removal failed: " + err.Error(),
			})
			continue
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
