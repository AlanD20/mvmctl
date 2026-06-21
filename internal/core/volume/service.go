// Package volume provides volume/disk image management for VMs.
// Layer: Core domain — never imports other core/* packages.
package volume

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
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
	parentDir := filepath.Dir(vol.Path)
	if err := os.MkdirAll(parentDir, os.ModePerm); err != nil {
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("create disk directory: %s", err))
	}

	switch vol.Format {
	case model.VolumeFormatRaw:
		_, err := system.DefaultRunner.Run(
			ctx,
			[]string{"fallocate", "-l", strconv.FormatInt(vol.SizeBytes, 10), vol.Path},
			system.RunCmdOpts{Check: true, Capture: true},
		)
		if err != nil {
			return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("fallocate failed: %s", err.Error()))
		}
	case model.VolumeFormatQCOW2:
		_, err := system.DefaultRunner.Run(
			ctx,
			[]string{
				"qemu-img",
				"create",
				"-f",
				string(model.VolumeFormatQCOW2),
				vol.Path,
				strconv.FormatInt(vol.SizeBytes, 10),
			},
			system.RunCmdOpts{Check: true, Capture: true},
		)
		if err != nil {
			return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("qemu-img create failed: %s", err.Error()))
		}
	default:
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("Unsupported format: %s", vol.Format))
	}

	if err := s.repo.Upsert(ctx, vol); err != nil {
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("upsert volume after creation: %s", err))
	}

	return vol, nil
}

// Remove deletes the disk file and its DB record. Always returns nil.
func (s *Service) Remove(ctx context.Context, volume *model.VolumeItem) error {
	_ = s.repo.Delete(ctx, volume.ID)

	if _, err := os.Stat(volume.Path); err == nil {
		_ = os.Remove(volume.Path)
	}

	return nil
}

// ResizeDisk resizes a disk file and updates the DB record.
func (s *Service) ResizeDisk(
	ctx context.Context,
	vol *model.VolumeItem,
	newSizeBytes int64,
) (*model.VolumeItem, error) {
	if _, err := os.Stat(vol.Path); err != nil {
		if os.IsNotExist(err) {
			return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("Disk file not found: %s", vol.Path))
		}
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("stat disk file: %s", err))
	}

	switch vol.Format {
	case model.VolumeFormatRaw:
		_, err := system.DefaultRunner.Run(
			ctx,
			[]string{"fallocate", "-l", strconv.FormatInt(newSizeBytes, 10), vol.Path},
			system.RunCmdOpts{Check: true, Capture: true},
		)
		if err != nil {
			return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("fallocate resize failed: %s", err.Error()))
		}
	case model.VolumeFormatQCOW2:
		_, err := system.DefaultRunner.Run(
			ctx,
			[]string{"qemu-img", "resize", vol.Path, strconv.FormatInt(newSizeBytes, 10)},
			system.RunCmdOpts{Check: true, Capture: true},
		)
		if err != nil {
			return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("qemu-img resize failed: %s", err.Error()))
		}
	default:
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("Unsupported format: %s", vol.Format))
	}

	// Update volume fields before upserting.
	vol.SizeBytes = newSizeBytes
	vol.UpdatedAt = time.Now().Format(time.RFC3339)

	if err := s.repo.Upsert(ctx, vol); err != nil {
		return nil, errs.New(errs.CodeVolumeError, fmt.Sprintf("upsert volume after resize: %s", err))
	}

	return vol, nil
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
