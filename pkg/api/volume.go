// Package api provides the public orchestration layer for all operations.
package api
import (
	"context"
	"fmt"
	"time"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)
// VolumeAPI defines the public interface for volume operations.
type VolumeAPI interface {
	VolumeListAll(ctx context.Context) []*model.VolumeItem
	VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error)
	VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult
	VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error)
	VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error
	VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error)
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
// uses VolumeCreateRequest
// resolution pipeline and HashGenerator.volume() for ID.
func (op *Operation) VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
	req := inputs.NewVolumeCreateRequest(input, op.Connection.DB(), op.Repos.Volume)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().Format(time.RFC3339)
	// Generate ID matching The HashGenerator.volume(name, timestamp) exactly
	volumeID := crypto.VolumeID(resolved.Name, timestamp)
	volumeItem := &model.VolumeItem{
		ID:         volumeID,
		Name:       resolved.Name,
		SizeBytes:  resolved.SizeBytes,
		Format:     resolved.Format,
		IsReadOnly: resolved.IsReadOnly,
		Path:       resolved.Path,
		Status:     model.VolumeStatusAvailable,
		CreatedAt:  timestamp,
		UpdatedAt:  timestamp,
	}
	if _, volErr := op.Services.Volume.CreateDisk(ctx, volumeItem); volErr != nil {
		return nil, errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("Failed to create volume: %v", volErr), volErr)
	}
	op.AuditLog.LogOperation("volume.create", map[string]any{"name": input.Name}, "")
	return volumeItem, nil
}
// VolumeRemove removes volumes by name or ID.
// uses VolumeRequest resolution
// with partial-match error reporting, VM volume_ids cleanup, and hot-unplug.
func (op *Operation) VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult {
	req := inputs.NewVolumeRequest(input, op.Connection.DB(), op.Repos.Volume)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:    "error",
					Code:      "volume.not_found",
					Message:   err.Error(),
					Exception: err,
				},
			},
		}
	}
	results := make([]errs.OperationResult, 0)
	// for error_msg in request.errors:
	for _, errMsg := range req.Errors() {
		results = append(results, errs.OperationResult{
			Status:  "error",
			Code:    "volume.not_found",
			Message: errMsg,
		})
	}
	if len(resolved.Volumes) == 0 && len(results) == 0 {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:  "error",
					Code:    "volume.not_found",
					Message: "No volumes found matching the given identifiers",
				},
			},
		}
	}
	// Batch-enrich with VM references for VM attachment check
	op.Enr.EnrichVolume(ctx, resolved.Volumes, "vm")
	for _, vol := range resolved.Volumes {
		if vol.Status == model.VolumeStatusAttached && !force {
			results = append(results, errs.OperationResult{
				Status:  "error",
				Code:    "volume.remove_failed",
				Message: fmt.Sprintf("Volume '%s' is attached to a VM. Use --force to remove anyway.", vol.Name),
				Item:    vol,
			})
			continue
		}
		// When force-removing attached volume, clean up the VM's volume_ids reference,
		// hot-unplug if running, and update config on disk.
		if vol.Status == model.VolumeStatusAttached && force && vol.VMID != nil {
			vmItem, _ := op.Repos.VM.Get(ctx, *vol.VMID)
			if vmItem != nil && vmItem.VolumeIDs != nil {
				// Remove this volume from VM's volume_ids
				var newIDs []string
				for _, vid := range vmItem.VolumeIDs {
					if vid != vol.ID {
						newIDs = append(newIDs, vid)
					}
				}
				vmItem.VolumeIDs = newIDs
				_ = op.Repos.VM.Upsert(ctx, vmItem)
				vmCtrl := vm.NewController(vmItem, op.Repos.VM)
				_ = vmCtrl.DetachVolume(ctx, vol.ID)
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
	return &errs.BatchResult{Items: results}
}
// VolumeInspect returns detailed volume info as a raw dictionary.
// returns dict[str, Any]
// with volume metadata and disk information, not wrapped in OperationResult.
func (op *Operation) VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error) {
	vol, err := op.VolumeGet(ctx, input)
	if err != nil {
		return nil, err
	}
	diskInfo, _ := volume.GetDiskInfo(ctx, vol.Path)
	vmName := ""
	if vol.VMID != nil && *vol.VMID != "" {
		vm, _ := op.Repos.VM.Get(ctx, *vol.VMID)
		if vm != nil {
			vmName = vm.Name
		}
	}
	return &results.VolumeInspect{
		Volume: results.VolumeItemInfo{
			ID: vol.ID, Name: vol.Name, SizeBytes: vol.SizeBytes,
			Format: string(vol.Format), IsReadOnly: vol.IsReadOnly,
			Path: vol.Path, Status: string(vol.Status),
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
// VolumeResize resizes a volume.
// uses VolumeRequest resolution
// for identifier lookup and separate size parsing.
func (op *Operation) VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error {
	// VolumeRequest resolves the volume input to a volume entity for resizing.
	volInput := inputs.VolumeInput{Identifiers: []string{input.Name}}
	req := inputs.NewVolumeRequest(volInput, op.Connection.DB(), op.Repos.Volume)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return errs.WrapMsg(errs.CodeVolumeNotFound, err.Error(), err)
	}
	vol := resolved.Volumes[0]
	sizeBytes, err := disk.ParseDiskSizeToBytes(input.Size)
	if err != nil {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid size: %v", err))
	}
	_, err = op.Services.Volume.ResizeDisk(ctx, vol, sizeBytes)
	if err != nil {
		return errs.WrapMsg(errs.CodeVolumeResizeFailed, fmt.Sprintf("Failed to resize volume: %v", err), err)
	}
	op.AuditLog.LogOperation("volume.resize", map[string]any{"name": vol.Name}, "")
	return nil
}
// VolumeGet returns a single volume by identifier.
// uses VolumeRequest pipeline.
func (op *Operation) VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error) {
	req := inputs.NewVolumeRequest(input, op.Connection.DB(), op.Repos.Volume)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if len(resolved.Volumes) > 1 {
		return nil, fmt.Errorf("Expected exactly one volume identifier")
	}
	return resolved.Volumes[0], nil
}
