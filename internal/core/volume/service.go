// Package volume provides volume/disk image management for VMs.
// Layer: Core domain — never imports other core/* packages.
package volume

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

// Service handles volume disk operations — creation, removal, resizing, and inspection.
type Service struct {
	repo Repository
}

// NewService creates a new VolumeService.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// CreateDisk creates a disk file on the filesystem and persists the volume record.
func (s *Service) CreateDisk(ctx context.Context, vol *model.VolumeItem) (*model.VolumeItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeVolumeError, "create volume cancelled", err)
	}
	parentDir := filepath.Dir(vol.Path)
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("create disk directory: %s", err))
	}
	// CRITICAL: Root admission rejects group-writable files. Allocate through the
	// exclusive 0600 descriptor so umask and path replacement cannot change the inode.
	file, err := os.OpenFile(vol.Path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVolumeError, "create exclusive raw volume", err)
	}
	allocationErr := file.Chmod(0600)
	if allocationErr == nil {
		allocationErr = unix.Fallocate(int(file.Fd()), 0, 0, vol.SizeBytes)
	}
	if allocationErr == nil {
		allocationErr = file.Sync()
	}
	if err := errors.Join(allocationErr, file.Close()); err != nil {
		cleanupErr := os.Remove(vol.Path)
		if errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = nil
		}
		return nil, errs.WrapMsg(errs.CodeVolumeError, "allocate raw volume", errors.Join(err, cleanupErr))
	}
	if err := ctx.Err(); err != nil {
		cleanupErr := os.Remove(vol.Path)
		if errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = nil
		}
		return nil, errs.WrapMsg(errs.CodeVolumeError, "create volume cancelled before record publication",
			errors.Join(err, cleanupErr))
	}

	if err := s.repo.Upsert(ctx, vol); err != nil {
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("upsert volume after creation: %s", err))
	}

	return vol, nil
}

// Remove deletes authoritative storage before removing its local database projection.
func (s *Service) Remove(ctx context.Context, volume *model.VolumeItem) error {
	cacheRoot, err := infra.GetCacheDir()
	if err != nil {
		return err
	}
	if _, err := jailer.NewProtectedVolumeClient().Remove(ctx, jailer.ProtectedVolumeRemoveRequest{
		CacheRoot: cacheRoot, VolumeID: volume.ID,
	}); err != nil {
		slog.Error("remove authoritative volume storage", "volume_id", volume.ID, "error", err)
		return err
	}
	if err := s.repo.Delete(ctx, volume.ID); err != nil {
		slog.Error("delete removed volume projection", "volume_id", volume.ID, "error", err)
		return errs.WrapMsg(errs.CodeVolumeError,
			"volume storage was removed; retry to remove its local database record", err)
	}

	return nil
}

// ResizeDisk resizes a disk file and updates the DB record.
func (s *Service) ResizeDisk(
	ctx context.Context,
	vol *model.VolumeItem,
	newSizeBytes int64,
) (*model.VolumeItem, error) {
	if newSizeBytes <= 0 {
		return nil, errs.New(errs.CodeVolumeResizeFailed, "volume growth requires a positive logical size")
	}
	cacheRoot, err := infra.GetCacheDir()
	if err != nil {
		return nil, err
	}
	result, err := jailer.NewProtectedVolumeClient().Grow(ctx, jailer.ProtectedVolumeGrowRequest{
		CacheRoot: cacheRoot, VolumeID: vol.ID, TargetBytes: uint64(newSizeBytes),
	})
	if err != nil {
		slog.Error("grow authoritative volume storage", "volume_id", vol.ID, "error", err)
		return nil, err
	}

	vol.SizeBytes = int64(result.LogicalBytes)
	vol.UpdatedAt = time.Now().Format(time.RFC3339)

	if err := s.repo.Upsert(ctx, vol); err != nil {
		slog.Error("update grown volume projection", "volume_id", vol.ID, "error", err)
		return nil, errs.WrapMsg(errs.CodeVolumeError,
			"volume storage was grown; retry the same size to update its local database record", err)
	}

	return vol, nil
}

// InspectDisk reads the current authoritative backing without trusting the persisted caller path or size.
func (s *Service) InspectDisk(ctx context.Context, vol *model.VolumeItem) (jailer.ProtectedVolumeInspectResult, error) {
	cacheRoot, err := infra.GetCacheDir()
	if err != nil {
		return jailer.ProtectedVolumeInspectResult{}, err
	}
	result, err := jailer.NewProtectedVolumeClient().Inspect(ctx, jailer.ProtectedVolumeInspectRequest{
		CacheRoot: cacheRoot, VolumeID: vol.ID,
	})
	if err != nil {
		slog.Error("inspect authoritative volume storage", "volume_id", vol.ID, "error", err)
	}
	return result, err
}

// SetVolumesState updates the state of one or more volumes.
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
			return errs.New(errs.CodeValidationFailed,
				"vm_id is required when state is ATTACHED",
			)
		}
		for _, vol := range volumes {
			// Shareable read-only volumes stay available — no status tracking needed.
			if vol.IsShareable && vol.IsReadOnly {
				continue
			}
			controller := &Controller{volume: vol, repo: s.repo}
			if err := controller.Attach(ctx, *vmID); err != nil {
				slog.Debug("Failed to attach volume", "name", vol.Name, "error", err)
			}
		}
	case model.VolumeStatusAvailable:
		for _, vol := range volumes {
			if vol.Status != model.VolumeStatusAttached {
				continue
			}
			controller := &Controller{volume: vol, repo: s.repo}
			if err := controller.Detach(ctx); err != nil {
				slog.Debug("Failed to detach volume", "name", vol.Name, "error", err)
			}
		}
	}

	return nil
}
