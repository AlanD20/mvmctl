package api

import (
	"context"
	"encoding/hex"
	"errors"
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
	sourceVsock, err := op.Repos.Vsock.GetByVMID(ctx, vmItem.ID)
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
		ID:           snapshotID,
		Name:         name,
		SourceVMID:   capture.SourceVMID,
		SourceVMName: vmItem.Name,
		SnapshotDir:  directory,
		RootfsFile:   filepath.Join(directory, "rootfs.img"),
		KernelID:     capture.KernelID,
		ImageID:      vmItem.ImageID,
		BinaryID: crypto.BinaryReleaseID(
			"firecracker",
			capture.ReleaseArchitecture,
			capture.ReleaseVersion,
			capture.FirecrackerSHA256,
		),
		NetworkID:   capture.NetworkID,
		VCPUCount:   capture.VCPUCount,
		MemSizeMiB:  capture.MemSizeMiB,
		DiskSizeMiB: int(capture.RootfsBytes >> 20),
		SSHKeys:     vmItem.SSHKeys,
		SSHUser:     vmItem.SSHUser,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if capture.Mode == "full" {
		item.MemoryFile = filepath.Join(directory, "memory")
		item.StateFile = filepath.Join(directory, "vmstate")
	}
	item.ExtraConfig = &model.SnapshotExtraConfig{PCIEnabled: capture.PCIEnabled, NestedVirt: capture.NestedVirt,
		BootArgs: capture.BootArgs, CPUConfig: capture.CPUConfig, CgroupLimits: capture.CgroupLimits,
		EnableLogging: capture.Logging, EnableMetrics: capture.Metrics, LogLevel: capture.LogLevel}
	if sourceVsock != nil && capture.VsockCID != nil && sourceVsock.GuestCID == int(*capture.VsockCID) {
		item.ExtraConfig.VsockCID = sourceVsock.GuestCID
		item.ExtraConfig.VsockPort = sourceVsock.Port
		item.ExtraConfig.VsockToken = sourceVsock.Token
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
) (items []*model.VMItem, restoreErr error) {
	if err := input.Validate(); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed, "invalid snapshot restore request", err)
	}
	if input.Count != 1 {
		return nil, errs.New(
			errs.CodeSnapshotRestoreFailed,
			"a snapshot preserves one exact guest IP/MAC endpoint; concurrent duplicate restores would conflict",
		)
	}
	snapshot, err := input.ResolveSnapshot(ctx, op.Repos.Snapshot)
	if err != nil {
		return nil, err
	}
	projection, err := jailersvc.NewVMSnapshotClient().
		Inspect(ctx, jailersvc.VMSnapshotRequest{CacheRoot: op.CacheDir, SnapshotID: snapshot.ID})
	if err != nil {
		return nil, err
	}
	if input.Network != nil && *input.Network != "" {
		selected, err := input.ResolveNetwork(ctx, op.Repos.Network)
		if err != nil {
			return nil, err
		}
		if selected == nil || selected.ID != projection.NetworkID {
			return nil, errs.New(
				errs.CodeSnapshotRestoreFailed,
				"snapshot restore cannot replace the captured network, IP, or MAC",
			)
		}
	}
	existing, err := op.Repos.VM.NamesExist(ctx, []string{input.Name})
	if err != nil {
		return nil, err
	}
	if len(existing) != 0 {
		return nil, errs.AlreadyExists(errs.CodeVMAlreadyExists, input.Name)
	}
	created := infra.Now()
	// A fresh attempt must never share an ID with a restore that may already
	// have committed at root, even when both start within the same second.
	vmID := crypto.VMID(input.Name, created+":"+crypto.UUIDV4())
	directory := filepath.Join(op.CacheDir, "vms", vmID)
	// CRITICAL: A restored guest keeps its captured endpoint. Reserve that exact
	// address before publishing a VM record or asking root to restore it.
	lease, err := op.Repos.Lease.Acquire(ctx, projection.NetworkID, projection.GuestIPv4, &vmID)
	if err != nil {
		// Acquire may have inserted the row before its readback failed. Root has
		// not been called, so releasing only this new VM's lease is safe.
		cleanupErr := op.Repos.Lease.ReleaseByVM(context.WithoutCancel(ctx), vmID)
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			"reserve captured IP for restored VM "+vmID, errors.Join(err, cleanupErr))
	}
	if lease == nil || lease.VMID == nil || *lease.VMID != vmID {
		return nil, errs.New(errs.CodeSnapshotRestoreFailed,
			"captured IP "+projection.GuestIPv4+" is already leased on network "+projection.NetworkID)
	}
	var item *model.VMItem
	var directoryCreated, vmRecordAttempted, vsockRecordAttempted, rootAttempted bool
	defer func() {
		if restoreErr == nil || rootAttempted {
			return
		}
		// CRITICAL: Once root restore is attempted its outcome can be unknown.
		// This cleanup runs only while root cannot own the captured endpoint.
		cleanupCtx := context.WithoutCancel(ctx)
		var cleanupErr error
		if vsockRecordAttempted {
			cleanupErr = op.Repos.Vsock.DeleteByVMID(cleanupCtx, vmID)
		}
		if cleanupErr == nil && vmRecordAttempted {
			cleanupErr = op.Repos.VM.Delete(cleanupCtx, vmID)
		}
		if cleanupErr == nil {
			cleanupErr = op.Repos.Lease.ReleaseByVM(cleanupCtx, vmID)
		}
		if cleanupErr == nil && directoryCreated {
			cleanupErr = os.Remove(directory)
		}
		if cleanupErr != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("clean up restored VM %s: %w", vmID, cleanupErr))
			if vmRecordAttempted {
				items = []*model.VMItem{item}
			}
			return
		}
		items = nil
	}()
	if err := os.Mkdir(directory, 0755); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed, "create restored VM bookkeeping directory", err)
	}
	directoryCreated = true
	item = &model.VMItem{
		ID:        vmID,
		Name:      input.Name,
		Status:    model.VMStatusStarting,
		IPv4:      projection.GuestIPv4,
		MAC:       projection.GuestMAC,
		NetworkID: projection.NetworkID,
		KernelID:  projection.KernelID,
		ImageID:   snapshot.ImageID,
		BinaryID: crypto.BinaryReleaseID(
			"firecracker",
			projection.ReleaseArchitecture,
			projection.ReleaseVersion,
			projection.FirecrackerSHA256,
		),
		JailerBinaryID: crypto.BinaryReleaseID(
			"jailer",
			projection.ReleaseArchitecture,
			projection.ReleaseVersion,
			projection.JailerSHA256,
		),
		VCPUCount:  projection.VCPUCount,
		MemSizeMiB: projection.MemSizeMiB,
		DiskSizeMiB: int(
			projection.RootfsBytes >> 20,
		),
		RootfsPath:   filepath.Join(directory, "rootfs.img"),
		RootfsSuffix: ".img",
		ConfigPath: filepath.Join(
			directory,
			infra.VMFirecrackerConfigFilename,
		),
		PCIEnabled:    projection.PCIEnabled,
		NestedVirt:    projection.NestedVirt,
		CgroupLimits:  projection.CgroupLimits,
		CPUConfig:     projection.CPUConfig,
		BootArgs:      projection.BootArgs,
		EnableLogging: projection.Logging,
		EnableMetrics: projection.Metrics,
		LogLevel:      projection.LogLevel,
		SSHKeys:       snapshot.SSHKeys,
		SSHUser:       snapshot.SSHUser,
		CreatedAt:     created,
		UpdatedAt:     created,
	}
	if projection.CloudInit {
		item.CloudInitMode = string(model.CloudInitModeISO)
	}
	vmRecordAttempted = true
	if err := op.Repos.VM.Upsert(ctx, item); err != nil {
		return nil, err
	}
	if projection.VsockCID != nil && snapshot.ExtraConfig != nil &&
		snapshot.ExtraConfig.VsockCID == int(*projection.VsockCID) {
		vsockRecordAttempted = true
		if err := op.Repos.Vsock.Upsert(
			ctx,
			&model.VsockConfigItem{ID: crypto.UUIDV4(), VmID: vmID, GuestCID: int(*projection.VsockCID),
				Port: snapshot.ExtraConfig.VsockPort, Token: snapshot.ExtraConfig.VsockToken},
		); err != nil {
			return nil, err
		}
	}
	rootAttempted = true
	restored, err := vm.NewController(item, op.Repos.VM).SnapshotRestore(ctx, model.SnapshotRestoreConfig{
		CacheRoot: op.CacheDir, SnapshotID: snapshot.ID, Resume: input.Resume,
	})
	if err != nil {
		// Preserve both local identity and root recovery evidence. A failed transport
		// does not authorize deleting a VM that may already have consumed memory.
		return []*model.VMItem{item}, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			"restore outcome requires inspection of VM "+vmID, err)
	}
	for _, volume := range restored.Volumes {
		if err := op.Repos.Volume.Upsert(
			ctx,
			&model.VolumeItem{
				ID:   volume.ID,
				Name: input.Name + "-" + volume.GuestDriveID,
				SizeBytes: int64(
					volume.Bytes,
				),
				Format:     model.VolumeFormatRaw,
				Path:       filepath.Join(op.CacheDir, "volumes", volume.ID+".raw"),
				Status:     model.VolumeStatusAttached,
				VMID:       &item.ID,
				IsReadOnly: volume.ReadOnly,
				CacheType:  volume.CacheType,
				CreatedAt:  created,
				UpdatedAt:  created,
			},
		); err != nil {
			return []*model.VMItem{item}, err
		}
	}
	op.AuditLog.LogOperation("snapshot.restore", map[string]any{"snapshot_id": snapshot.ID, "vm_id": vmID}, "")
	return []*model.VMItem{item}, nil
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
