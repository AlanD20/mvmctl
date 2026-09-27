// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"time"
)

// VolumeAPI defines the public interface for volume operations.
type VolumeAPI interface {
	VolumeListAll(ctx context.Context) []*model.VolumeItem
	VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error)
	VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult
	VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error)
	VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error
	VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error)
	VolumeAttach(ctx context.Context, input inputs.VolumeInput) error
	VolumeDetach(ctx context.Context, input inputs.VolumeInput) error
}

// VolumeListAll returns all volumes.
func (op *Operation) VolumeListAll(ctx context.Context) []*model.VolumeItem {
	volumes, _ := op.Repos.Volume.ListAll(ctx)
	if volumes == nil {
		return []*model.VolumeItem{}
	}
	return volumes
}

// VolumeCreate creates a new volume.
func (op *Operation) VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
	resolved, err := input.Resolve(ctx, op.Repos.Volume)
	if err != nil {
		return nil, err
	}

	// Resolve cache type: explicit flag takes precedence, then config default.
	cacheType := resolved.CacheType
	if cacheType == "" {
		cacheType, _ = op.Services.Config.GetString(ctx, "defaults.volume", "cache_type")
	}

	timestamp := time.Now().Format(time.RFC3339)
	// Generate ID matching The HashGenerator.volume(name, timestamp) exactly
	volumeID := crypto.VolumeID(resolved.Name, timestamp)
	volumeItem := &model.VolumeItem{
		ID:          volumeID,
		Name:        resolved.Name,
		SizeBytes:   resolved.SizeBytes,
		Format:      model.VolumeFormatRaw,
		IsReadOnly:  resolved.IsReadOnly,
		IsShareable: resolved.IsShareable,
		CacheType:   cacheType,
		Path:        infra.GetVolumePath(volumeID),
		Status:      model.VolumeStatusAvailable,
		CreatedAt:   timestamp,
		UpdatedAt:   timestamp,
	}
	if _, volErr := op.Services.Volume.CreateDisk(ctx, volumeItem); volErr != nil {
		return nil, errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("Failed to create volume: %v", volErr), volErr)
	}
	op.AuditLog.LogOperation("volume.create", map[string]any{"name": input.Name}, "")
	return volumeItem, nil
}

// VolumeRemove removes volumes by name or ID.
// Handles partial-match error reporting and stopped-VM volume_ids cleanup.
func (op *Operation) VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult {
	if err := input.Validate(); err != nil {
		inputErr := errs.WrapMsg(errs.CodeVolumeNotFound, err.Error(), err, errs.WithClass(errs.ClassValidation))
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:    "error",
					Code:      string(errs.CodeVolumeNotFound),
					Message:   inputErr.Error(),
					Exception: inputErr,
				},
			},
		}
	}
	selectorResults := make([]errs.OperationResult, len(input.Identifiers))
	selectorVolumeIndexes := make([]int, len(input.Identifiers))
	volumes := make([]*model.VolumeItem, 0, len(input.Identifiers))
	volumeIndexes := make(map[string]int, len(input.Identifiers))
	resolver := volume.NewResolver(op.Repos.Volume)
	for i, identifier := range input.Identifiers {
		vol, resolveErr := resolver.Resolve(ctx, identifier)
		if resolveErr != nil {
			slog.Warn("volume remove: failed to resolve selector", "identifier", identifier, "error", resolveErr)
			code := errs.CodeVolumeError
			var domainErr *errs.DomainError
			failureOptions := []errs.ErrorOption{errs.WithEntity(identifier), errs.WithClass(errs.ClassInternal)}
			if errors.As(resolveErr, &domainErr) {
				code = domainErr.Code
				failureOptions = []errs.ErrorOption{errs.WithEntity(identifier)}
			}
			failure := errs.WrapMsg(code,
				fmt.Sprintf("Failed to resolve volume '%s': %v", identifier, resolveErr), resolveErr, failureOptions...)
			selectorResults[i] = errs.OperationResult{
				Status:    "error",
				Code:      string(code),
				Message:   failure.Error(),
				Item:      identifier,
				Exception: failure,
			}
			continue
		}
		index, found := volumeIndexes[vol.ID]
		if !found {
			index = len(volumes)
			volumeIndexes[vol.ID] = index
			volumes = append(volumes, vol)
		}
		selectorVolumeIndexes[i] = index
	}
	results := make([]errs.OperationResult, 0, len(volumes))
	// Batch-enrich with VM references for VM attachment check
	if len(volumes) > 0 {
		op.Enr.EnrichVolume(ctx, volumes, "vm")
	}
	for _, vol := range volumes {
		// Shareable read-only volumes are treated as not attached
		// since we can't track individual VM attachments for them.
		isEffectivelyAttached := vol.Status == model.VolumeStatusAttached && !(vol.IsShareable && vol.IsReadOnly)
		if isEffectivelyAttached && !force {
			results = append(results, errs.OperationResult{
				Status:  "error",
				Code:    "volume.remove_failed",
				Message: fmt.Sprintf("Volume '%s' is attached to a VM. Use --force to remove anyway.", vol.Name),
				Item:    vol,
			})
			continue
		}
		// Force permits removal of an attached volume only after the authoritative
		// runtime observation proves its VM stopped. It does not hot-unplug a guest.
		if vol.Status == model.VolumeStatusAttached && force {
			if vol.VMID == nil || *vol.VMID == "" {
				results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
					Message: "attached volume has no VM identity", Item: vol})
				continue
			}
			vmItem, resolveErr := op.Repos.VM.Get(ctx, *vol.VMID)
			if resolveErr != nil {
				results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
					Message: "failed to resolve attached VM", Item: vol, Exception: resolveErr})
				continue
			}
			if vmItem == nil {
				results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
					Message: "attached VM is missing", Item: vol})
				continue
			}
			if err := op.reconcileVMRuntimeStatus(ctx, []*model.VMItem{vmItem}); err != nil {
				results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
					Message: "failed to admit attached VM runtime", Item: vol, Exception: err})
				continue
			}
			if vmItem.Status != model.VMStatusStopped {
				results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
					Message: fmt.Sprintf(
						"volume '%s' is attached to a VM that is not stopped. Stop the VM before removing it.",
						vol.Name,
					),
					Item: vol})
				continue
			}
			if vmItem.VolumeIDs != nil {
				// Remove this volume from VM's volume_ids
				var newIDs []string
				for _, vid := range vmItem.VolumeIDs {
					if vid != vol.ID {
						newIDs = append(newIDs, vid)
					}
				}
				vmItem.VolumeIDs = newIDs
				if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
					results = append(results, errs.OperationResult{Status: "error", Code: "volume.remove_failed",
						Message: "failed to persist detached VM volume list", Item: vol, Exception: err})
					continue
				}
			}
		}
		if err := op.Services.Volume.Remove(ctx, vol); err != nil {
			results = append(results, errs.OperationResult{
				Status:    "error",
				Code:      "volume.remove_failed",
				Message:   fmt.Sprintf("Failed to remove volume '%s': %v", vol.Name, err),
				Item:      vol,
				Exception: err,
			})
			continue
		}
		op.AuditLog.LogOperation("volume.remove", map[string]any{"name": vol.Name}, "")
		results = append(results, errs.OperationResult{
			Status:  "success",
			Code:    "volume.removed",
			Item:    vol,
			Message: fmt.Sprintf("Volume '%s' removed", vol.Name),
		})
	}
	for i := range selectorResults {
		if selectorResults[i].IsError() {
			continue
		}
		selectorResults[i] = results[selectorVolumeIndexes[i]]
	}
	return &errs.BatchResult{Items: selectorResults}
}

// VolumeInspect returns detailed volume info as a raw dictionary.
// returns dict[str, Any]
// with volume metadata and disk information, not wrapped in OperationResult.
func (op *Operation) VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error) {
	vol, err := op.VolumeGet(ctx, input)
	if err != nil {
		return nil, err
	}
	diskInfo, err := op.Services.Volume.InspectDisk(ctx, vol)
	if err != nil {
		return nil, err
	}
	displayPath := vol.Path
	if diskInfo.Protected {
		displayPath = ""
	}
	vmName := ""
	if vol.VMID != nil && *vol.VMID != "" {
		vm, _ := op.Repos.VM.Get(ctx, *vol.VMID)
		if vm != nil {
			vmName = vm.Name
		}
	}
	return &results.VolumeInspect{
		Volume: results.VolumeItemInfo{
			ID: vol.ID, Name: vol.Name, SizeBytes: int64(diskInfo.LogicalBytes),
			Format: string(vol.Format), IsReadOnly: vol.IsReadOnly,
			CacheType: vol.CacheType, Path: displayPath, Status: string(vol.Status),
		},
		Attachment: results.VolumeAttachmentInfo{
			VMID: vol.VMID, VMName: vmName,
		},
		DiskInfo: diskInfo,
		Timestamps: results.VolumeTimestampsInfo{
			CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt,
		},
	}, nil
}

// VolumeResize grows an unattached volume or one attached to a stopped VM.
func (op *Operation) VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error {
	volInput := inputs.VolumeInput{Identifiers: []string{input.Name}}
	volumes, err := volInput.ResolveRaw(ctx, op.Repos.Volume)
	if err != nil {
		return err
	}
	vol := volumes[0]
	sizeBytes, err := disk.ParseDiskSizeToBytes(input.Size)
	if err != nil {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid size: %v", err))
	}
	if vol.Status == model.VolumeStatusAttached {
		if vol.VMID == nil || *vol.VMID == "" {
			return errs.New(errs.CodeVolumeResizeFailed, "attached volume has no VM identity")
		}
		vmItem, err := op.Repos.VM.Get(ctx, *vol.VMID)
		if err != nil {
			return errs.WrapMsg(errs.CodeVolumeResizeFailed, "failed to resolve attached VM", err)
		}
		if vmItem == nil {
			return errs.New(errs.CodeVolumeResizeFailed, "attached VM is missing")
		}
		if err := op.reconcileVMRuntimeStatus(ctx, []*model.VMItem{vmItem}); err != nil {
			return errs.WrapMsg(errs.CodeVolumeResizeFailed, "failed to admit attached VM runtime", err)
		}
		if vmItem.Status != model.VMStatusStopped {
			return errs.New(
				errs.CodeVolumeResizeFailed,
				fmt.Sprintf(
					"volume '%s' is attached to a VM that is not stopped. Stop the VM before growing it.",
					vol.Name,
				),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	_, err = op.Services.Volume.ResizeDisk(ctx, vol, sizeBytes)
	if err != nil {
		return errs.WrapMsg(errs.CodeVolumeResizeFailed, fmt.Sprintf("Failed to resize volume: %v", err), err)
	}
	op.AuditLog.LogOperation("volume.resize", map[string]any{"name": vol.Name}, "")
	return nil
}

// VolumeGet returns a single volume by identifier.
func (op *Operation) VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error) {
	volumes, err := input.Resolve(ctx, op.Repos.Volume)
	if err != nil {
		return nil, err
	}
	if len(volumes) > 1 {
		return nil, fmt.Errorf("Expected exactly one volume identifier")
	}
	return volumes[0], nil
}

// --- AttachVolume / DetachVolume ---
// VolumeAttach attaches a volume to a VM.
func (op *Operation) VolumeAttach(
	ctx context.Context,
	input inputs.VolumeInput,
) error {
	vmItem, err := input.ResolveVM(ctx, op.Repos.VM)
	if err != nil {
		return err
	}
	volumes, resolveErr := input.ResolveRaw(ctx, op.Repos.Volume)
	if resolveErr != nil {
		return resolveErr
	}
	if len(volumes) != 1 {
		return errs.NotFound(errs.CodeVolumeNotFound, "Expected exactly one volume identifier")
	}
	if err := op.reconcileVMRuntimeStatus(ctx, []*model.VMItem{vmItem}); err != nil {
		return err
	}
	vol := volumes[0]
	alreadyAttachedHere := vol.Status == model.VolumeStatusAttached && vol.VMID != nil && *vol.VMID == vmItem.ID
	if vol.Status == model.VolumeStatusAttached && !alreadyAttachedHere {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Volume '%s' is already attached", vol.Name),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Check volume status.
	// Shareable read-only volumes are always attachable regardless of status.
	if vol.Status != model.VolumeStatusAvailable && !alreadyAttachedHere && !(vol.IsShareable && vol.IsReadOnly) {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Volume '%s' is not available", vol.Name),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Hotplug on running VM
	if vmItem.Status == model.VMStatusRunning || vmItem.Status == model.VMStatusPaused {
		if !vmItem.PCIEnabled {
			return errs.New(
				errs.CodeVMCreateFailed,
				fmt.Sprintf(
					"PCI is not enabled for VM '%s' — volume hotplug requires PCI access in the guest",
					vmItem.Name,
				),
				errs.WithClass(errs.ClassValidation),
			)
		}
		// Version gate: hotplug requires Firecracker v1.16+
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotplug) {
					return errs.New(
						errs.CodeBinaryVersionGate,
						fmt.Sprintf(
							"Volume hotplug requires Firecracker >= 1.16, got %s. "+
								"Use a newer Firecracker binary or attach the volume while the VM is stopped.",
							bin.Version,
						),
					)
				}
			}
		}
		// Attempt hotplug via Firecracker API.
		controller := vm.NewController(vmItem, op.Repos.VM)
		if err := controller.AttachManagedVolume(ctx, op.CacheDir, vol); err != nil {
			return err
		}
		client, err := op.vsockClient(ctx, vmItem)
		if err != nil {
			slog.Warn("vsock client not available for PCI rescan, device may not appear until reboot",
				"vm", vmItem.Name, "volume", vol.Name, "error", err)
		}
		if client != nil {
			if err := client.RescanPCI(ctx); err != nil {
				slog.Warn("guest PCI rescan via vsock failed, device may not appear until reboot",
					"vm", vmItem.Name, "volume", vol.Name, "error", err)
			}
		}
	}
	// VolumeController.Attach
	volController := volume.NewController(vol, op.Repos.Volume)
	if err := volController.Attach(ctx, vmItem.ID); err != nil {
		slog.Warn("failed to attach volume to VM", "vm", vmItem.Name, "volume", vol.Name, "error", err)
		return err
	}
	// Update VM's volume_ids
	var vmVolumeIDs []string
	if len(vmItem.VolumeIDs) > 0 {
		vmVolumeIDs = vmItem.VolumeIDs
	}
	found := false
	for _, id := range vmVolumeIDs {
		if id == vol.ID {
			found = true
			break
		}
	}
	if !found {
		vmVolumeIDs = append(vmVolumeIDs, vol.ID)
	}
	vmItem.VolumeIDs = vmVolumeIDs
	if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
		slog.Warn("failed to update VM volume IDs", "vm", vmItem.Name, "error", err)
		return err
	}
	op.AuditLog.LogOperation("vm.attach_volume", map[string]any{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")
	return nil
}

// VolumeDetach detaches a volume from a VM.
func (op *Operation) VolumeDetach(
	ctx context.Context,
	input inputs.VolumeInput,
) error {
	vmItem, err := input.ResolveVM(ctx, op.Repos.VM)
	if err != nil {
		return err
	}
	if err := op.reconcileVMRuntimeStatus(ctx, []*model.VMItem{vmItem}); err != nil {
		return err
	}
	// Resolve volume using VolumeInput.Resolve (uses ResolveMany).
	volumes, resolveErr := input.Resolve(ctx, op.Repos.Volume)
	if resolveErr != nil {
		return errs.NotFound(errs.CodeVolumeNotFound, fmt.Sprintf("Volume not found: %v", resolveErr))
	}
	if len(volumes) != 1 {
		return errs.NotFound(errs.CodeVolumeNotFound, "Expected exactly one volume identifier")
	}
	vol := volumes[0]
	if vol.VMID != nil && *vol.VMID != vmItem.ID {
		return errs.New(errs.CodeValidationFailed, "volume belongs to a different VM")
	}
	// Hot-unplug if running
	if vmItem.Status == model.VMStatusRunning || vmItem.Status == model.VMStatusPaused {
		// Version gate: hot-unplug requires Firecracker v1.16+
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotUnplug) {
					return errs.New(
						errs.CodeBinaryVersionGate,
						fmt.Sprintf(
							"Volume hot-unplug requires Firecracker >= 1.16, got %s. "+
								"Use a newer Firecracker binary or detach the volume while the VM is stopped.",
							bin.Version,
						),
					)
				}
			}
		}
		client, err := op.vsockClient(ctx, vmItem)
		if err != nil {
			slog.Warn("vsock client not available for PCI device removal",
				"vm", vmItem.Name, "volume", vol.Name, "error", err)
		}
		// Attempt hot-unplug via Firecracker API.
		ctrl := vm.NewController(vmItem, op.Repos.VM)
		if err := ctrl.DetachVolume(ctx, vol.ID); err != nil {
			slog.Warn("Hot-unplug failed for drive", "volume", vol.ID, "error", err)
			return err
		}

		// Post-detach PCI rescan so the guest kernel reclaims the device slot.
		if client != nil {
			if err := client.RescanPCI(ctx); err != nil {
				slog.Warn("guest PCI rescan after hot-unplug failed",
					"vm", vmItem.Name, "volume", vol.Name, "error", err)
			}
		}
	}
	// VolumeController.Detach
	volController := volume.NewController(vol, op.Repos.Volume)
	if err := volController.Detach(ctx); err != nil {
		slog.Warn("failed to detach volume from VM", "vm", vmItem.Name, "volume", vol.Name, "error", err)
		return err
	}
	// Update VM's volume_ids
	var vmVolumeIDs []string
	if len(vmItem.VolumeIDs) > 0 {
		vmVolumeIDs = vmItem.VolumeIDs
	}
	newIDs := make([]string, 0, len(vmVolumeIDs))
	for _, id := range vmVolumeIDs {
		if id != vol.ID {
			newIDs = append(newIDs, id)
		}
	}
	vmItem.VolumeIDs = newIDs
	if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
		slog.Warn("failed to update VM volume IDs", "vm", vmItem.Name, "error", err)
		return err
	}
	op.AuditLog.LogOperation("vm.detach_volume", map[string]any{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")
	return nil
}
