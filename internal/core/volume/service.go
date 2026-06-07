package volume

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// Service handles volume disk operations — creation, removal, resizing, and inspection.
// Matches Python's VolumeService exactly.
type Service struct {
	repo Repository
}

// NewService creates a new VolumeService.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// CreateDisk creates a disk file on the filesystem and persists the volume record.
// Matches Python's VolumeService.create_disk() exactly.
func (s *Service) CreateDisk(ctx context.Context, vol *model.VolumeItem) (*model.VolumeItem, error) {
	parentDir := filepath.Dir(vol.Path)
	if err := os.MkdirAll(parentDir, os.ModePerm); err != nil {
		return nil, NewVolumeErrorf("create disk directory: %s", err)
	}

	switch vol.Format {
	case model.VolumeFormatRaw:
		result := system.RunCmdCompat(
			ctx,
			[]string{"fallocate", "-l", strconv.FormatInt(vol.SizeBytes, 10), vol.Path},
			system.DefaultRunCmdOpts(),
		)
		if result.Err != nil {
			// Python: raise VolumeError(f"fallocate failed: {e}") from e
			return nil, NewVolumeErrorf("fallocate failed: %s", result.Err.Error())
		}
	case model.VolumeFormatQCOW2:
		result := system.RunCmdCompat(
			ctx,
			[]string{"qemu-img", "create", "-f", string(model.VolumeFormatQCOW2), vol.Path, strconv.FormatInt(vol.SizeBytes, 10)},
			system.DefaultRunCmdOpts(),
		)
		if result.Err != nil {
			return nil, NewVolumeErrorf("qemu-img create failed: %s", result.Err.Error())
		}
	default:
		return nil, NewVolumeErrorf("Unsupported format: %s", vol.Format)
	}

	if err := s.repo.Upsert(ctx, vol); err != nil {
		return nil, NewVolumeErrorf("upsert volume after creation: %s", err)
	}

	return vol, nil
}

// Remove deletes the disk file and its DB record.
// Matches Python's VolumeService.remove() exactly.
// Go must match: silently ignore ALL errors from repo.Delete and os.Remove.
// Return nil always (matching Python's None return).
func (s *Service) Remove(ctx context.Context, volume *model.VolumeItem) error {
	// Python: self._repo.delete(volume.id) — silently ignores all failures
	_ = s.repo.Delete(ctx, volume.ID)

	// Python: if disk_path.exists(): disk_path.unlink(missing_ok=True)
	if _, err := os.Stat(volume.Path); err == nil {
		_ = os.Remove(volume.Path)
	}

	return nil
}

// ResizeDisk resizes a disk file and updates the DB record.
// Matches Python's VolumeService.resize_disk() exactly.
func (s *Service) ResizeDisk(
	ctx context.Context,
	vol *model.VolumeItem,
	newSizeBytes int64,
) (*model.VolumeItem, error) {
	if _, err := os.Stat(vol.Path); err != nil {
		if os.IsNotExist(err) {
			return nil, NewVolumeErrorf("Disk file not found: %s", vol.Path)
		}
		return nil, NewVolumeErrorf("stat disk file: %s", err)
	}

	switch vol.Format {
	case model.VolumeFormatRaw:
		result := system.RunCmdCompat(
			ctx,
			[]string{"fallocate", "-l", strconv.FormatInt(newSizeBytes, 10), vol.Path},
			system.DefaultRunCmdOpts(),
		)
		if result.Err != nil {
			return nil, NewVolumeErrorf("fallocate resize failed: %s", result.Err.Error())
		}
	case model.VolumeFormatQCOW2:
		result := system.RunCmdCompat(
			ctx,
			[]string{"qemu-img", "resize", vol.Path, strconv.FormatInt(newSizeBytes, 10)},
			system.DefaultRunCmdOpts(),
		)
		if result.Err != nil {
			return nil, NewVolumeErrorf("qemu-img resize failed: %s", result.Err.Error())
		}
	default:
		return nil, NewVolumeErrorf("Unsupported format: %s", vol.Format)
	}

	// Update volume fields — matches Python's resize_disk() which sets
	// size_bytes and updated_at before upserting.
	vol.SizeBytes = newSizeBytes
	vol.UpdatedAt = time.Now().Format(time.RFC3339)

	if err := s.repo.Upsert(ctx, vol); err != nil {
		return nil, NewVolumeErrorf("upsert volume after resize: %s", err)
	}

	return vol, nil
}

// SetVolumesState updates the state of one or more volumes.
// Matches Python's VolumeService.set_volumes_state() exactly.
// Python: vm_id: str | None = None — defaults to None.
// Python fire-and-forgets individual failures: logs a warning and continues.
// Go must match — don't aggregate errors, just log warnings.
func (s *Service) SetVolumesState(
	ctx context.Context,
	volumes []*model.VolumeItem,
	status model.VolumeStatus,
	vmID *string,
) error {
	if len(volumes) == 0 {
		return nil
	}

	switch status {
	case model.VolumeStatusAttached:
		if vmID == nil || *vmID == "" {
			return errs.ValidationFailed(
				errs.CodeValidationFailed,
				"vm_id is required when state is ATTACHED",
			)
		}
		for _, vol := range volumes {
			controller := &Controller{volume: vol, repo: s.repo}
			if err := controller.Attach(ctx, *vmID); err != nil {
				// Python: logger.warning("Failed to attach volume '%s': %s", vol.name, exc)
				slog.Warn("Failed to attach volume", "name", vol.Name, "error", err)
			}
		}
	case model.VolumeStatusAvailable:
		for _, vol := range volumes {
			if vol.Status != model.VolumeStatusAttached {
				continue
			}
			controller := &Controller{volume: vol, repo: s.repo}
			if err := controller.Detach(ctx); err != nil {
				// Python: logger.warning("Failed to detach volume '%s': %s", vol.name, exc)
				slog.Warn("Failed to detach volume", "name", vol.Name, "error", err)
			}
		}
	}

	return nil
}
