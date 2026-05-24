// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/volume_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// VolumeOperation orchestrates volume lifecycle across core domains.
// Matches Python's VolumeOperation exactly.
type VolumeOperation struct {
	svc      *volume.Service
	repo     volume.Repository
	vmRepo   vm.Repository
	cacheDir string
	db       *sql.DB
}

// NewVolumeOperation creates a VolumeOperation.
func NewVolumeOperation(svc *volume.Service, repo volume.Repository, vmRepo vm.Repository, cacheDir string, db *sql.DB) *VolumeOperation {
	return &VolumeOperation{
		svc:      svc,
		repo:     repo,
		vmRepo:   vmRepo,
		cacheDir: cacheDir,
		db:       db,
	}
}

// Create creates a new volume.
// Matches Python's VolumeOperation.create() exactly — uses VolumeCreateRequest
// resolution pipeline and HashGenerator.volume() for ID.
func (o *VolumeOperation) Create(ctx context.Context, input *inputs.VolumeCreateInput) *errs.OperationResult {
	req := inputs.NewVolumeCreateRequest(*input, o.db, o.repo)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		// Extract the original error code from DomainError — for duplicate names
		// this correctly returns "volume.already_exists" (matching Python), for
		// other validation errors it returns the specific code.
		code := "volume.create_failed"
		var de *errs.DomainError
		if errors.As(err, &de) {
			code = string(de.Code)
		}
		return &errs.OperationResult{
			Status:  "error",
			Code:    code,
			Message: err.Error(),
		}
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Generate ID matching Python's HashGenerator.volume(name, timestamp) exactly
	var hg infra.HashGenerator
	volumeID := hg.Volume(resolved.Name, timestamp)

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

	if _, volErr := o.svc.CreateDisk(ctx, volumeItem); volErr != nil {
		// Python: VolumeError from create_disk propagates uncaught; in Go we
		// return an OperationResult with the correct error code.
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeInternal),
			Message:   fmt.Sprintf("Failed to create volume: %v", volErr),
			Exception: volErr,
		}
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("volume.create", map[string]interface{}{"name": input.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "volume.created",
		Item:    volumeItem,
		Message: fmt.Sprintf("Volume '%s' created", input.Name),
	}
}

// ListAll returns all volumes.
// Matches Python's VolumeOperation.list_all() exactly.
func (o *VolumeOperation) ListAll(ctx context.Context) []*model.VolumeItem {
	volumes, _ := o.repo.ListAll(ctx)
	return volumes
}

// Get returns a single volume by identifier.
// Matches Python's VolumeOperation.get() exactly — uses VolumeRequest pipeline.
func (o *VolumeOperation) Get(ctx context.Context, input *inputs.VolumeInput) (*model.VolumeItem, error) {
	// Python: resolved = VolumeRequest(inputs=inputs, db=Database()).resolve()
	req := inputs.NewVolumeRequest(*input, o.db, o.repo)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	// Python: if len(resolved.volumes) > 1: raise VolumeNotFoundError(...)
	if len(resolved.Volumes) > 1 {
		return nil, fmt.Errorf("Expected exactly one volume identifier")
	}

	return resolved.Volumes[0], nil
}

// Remove removes volumes by name or ID.
// Matches Python's VolumeOperation.remove() exactly — uses VolumeRequest resolution
// with partial-match error reporting, VM volume_ids cleanup, and hot-unplug.
func (o *VolumeOperation) Remove(ctx context.Context, input *inputs.VolumeInput, force bool) *errs.BatchResult {
	// Python: request = VolumeRequest(inputs=inputs, db=db)
	//         try: resolved = request.resolve()
	//         except VolumeNotFoundError as e: return BatchResult(items=[OperationResult(...)])
	req := inputs.NewVolumeRequest(*input, o.db, o.repo)
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

	// Python: Surface partial-match errors from resolver
	//         for error_msg in request.errors:
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
		// hot-unplug if running, and update config on disk (matching Python).
		if vol.Status == model.VolumeStatusAttached && force && vol.VMID != nil {
			vmItem, _ := o.vmRepo.Get(ctx, *vol.VMID)
			if vmItem != nil && vmItem.VolumeIDs != nil {
				// Remove this volume from VM's volume_ids
				newIDs := make([]string, 0)
				for _, vid := range vmItem.VolumeIDs {
					if vid != vol.ID {
						newIDs = append(newIDs, vid)
					}
				}
				vmItem.VolumeIDs = newIDs
				_ = o.vmRepo.Upsert(ctx, vmItem)

				// Python: try: ctrl.detach_volume(volume) except Exception: pass
				if vmCtrl, ctrlErr := vm.NewController(vmItem, o.vmRepo); ctrlErr == nil {
					_ = vmCtrl.DetachVolume(ctx, vol)
				}
			}
		}

		if err := o.svc.Remove(ctx, vol); err != nil {
			results = append(results, errs.OperationResult{
				Status:    "error",
				Code:      "volume.remove_failed",
				Message:   fmt.Sprintf("Failed to remove volume '%s': %v", vol.Name, err),
				Item:      vol,
				Exception: err,
			})
			continue
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("volume.remove", map[string]interface{}{"name": vol.Name}, "")

		results = append(results, errs.OperationResult{
			Status:  "success",
			Code:    "volume.removed",
			Item:    vol,
			Message: fmt.Sprintf("Volume '%s' removed", vol.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

// Inspect returns detailed volume info as a raw dictionary.
// Matches Python's VolumeOperation.inspect() exactly — returns dict[str, Any]
// with volume metadata and disk information, not wrapped in OperationResult.
func (o *VolumeOperation) Inspect(ctx context.Context, input *inputs.VolumeInput) (map[string]interface{}, error) {
	vol, err := o.Get(ctx, input)
	if err != nil {
		return nil, err // Propagate error, matching Python's raise VolumeNotFoundError
	}

	// Use service.GetDiskInfo() matching Python's service.get_disk_info(Path(volume_item.path))
	diskInfo, _ := volume.GetDiskInfo(ctx, vol.Path)

	vmName := ""
	if vol.VMID != nil && *vol.VMID != "" {
		vm, _ := o.vmRepo.Get(ctx, *vol.VMID)
		if vm != nil {
			vmName = vm.Name
		}
	}

	return map[string]interface{}{
		"volume": map[string]interface{}{
			"id":           vol.ID,
			"name":         vol.Name,
			"size_bytes":   vol.SizeBytes,
			"format":       vol.Format,
			"is_read_only": vol.IsReadOnly,
			"path":         vol.Path,
			"status":       string(vol.Status),
		},
		"attachment": map[string]interface{}{
			"vm_id":   vol.VMID,
			"vm_name": vmName,
		},
		"disk_info": diskInfo,
		"timestamps": map[string]interface{}{
			"created_at": vol.CreatedAt,
			"updated_at": vol.UpdatedAt,
		},
	}, nil
}

// Resize resizes a volume.
// Matches Python's VolumeOperation.resize() exactly — uses VolumeRequest resolution
// for identifier lookup and separate size parsing.
func (o *VolumeOperation) Resize(ctx context.Context, input *inputs.VolumeCreateInput) *errs.OperationResult {
	// Python: vol_input = VolumeInput(identifiers=[inputs.name])
	//         resolved_vol = VolumeRequest(inputs=vol_input, db=db).resolve()
	//         volume = resolved_vol.volumes[0]
	volInput := inputs.VolumeInput{Identifiers: []string{input.Name}}
	req := inputs.NewVolumeRequest(volInput, o.db, o.repo)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeVolumeNotFound),
			Message:   err.Error(),
			Exception: err,
		}
	}

	vol := resolved.Volumes[0]

	// Python: size_bytes = DiskUtils.parse_disk_size_to_bytes(inputs.size)
	sizeBytes, err := infra.ParseDiskSizeToBytes(input.Size)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeValidationFailed),
			Message: fmt.Sprintf("Invalid size: %v", err),
		}
	}

	updated, err := o.svc.ResizeDisk(ctx, vol, sizeBytes)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "volume.resize_failed",
			Message:   fmt.Sprintf("Failed to resize volume: %v", err),
			Exception: err,
		}
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("volume.resize", map[string]interface{}{"name": vol.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "volume.resized",
		Item:    updated,
		Message: fmt.Sprintf("Volume '%s' resized", vol.Name),
	}
}

// Compile-time check
var _ = slog.Default()
