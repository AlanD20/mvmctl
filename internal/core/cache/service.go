// Package cache provides stateless cache cleanup operations — guestfs, appliance, warm images.
package cache

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// Service provides stateless cache cleanup operations.
// The cacheDir and tempDir fields are kept for delegation to operations that
// require them for binary path resolution or temp directory scanning.
type Service struct {
	cacheDir string
	tempDir  string
}

// NewService creates a new CacheService.
func NewService(cacheDir, tempDir string) *Service {
	return &Service{
		cacheDir: cacheDir,
		tempDir:  tempDir,
	}
}

// ScanOrphanProcesses scans /proc for mvmctl-managed processes still running.
func (s *Service) ScanOrphanProcesses(ctx context.Context) []map[string]any {
	var orphans []map[string]any

	procDir, err := os.Open("/proc")
	if err != nil {
		if os.IsPermission(err) {
			slog.Warn("Cannot scan /proc for orphan processes (permission denied)")
		}
		return orphans
	}
	defer procDir.Close()

	entries, err := procDir.Readdir(-1)
	if err != nil {
		if os.IsPermission(err) {
			slog.Warn("Cannot scan /proc for orphan processes (permission denied)")
		}
		return orphans
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}

		pid := entry.Name()
		procPath := filepath.Join("/proc", pid)

		// Check /proc/PID/comm — only firecracker/jailer count as VM processes.
		commBytes, err := os.ReadFile(filepath.Join(procPath, "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))

		for _, known := range knownMVMComms {
			if comm == known {
				p, _ := strconv.Atoi(pid)
				orphans = append(orphans, map[string]any{
					"pid":  p,
					"comm": comm,
				})
				break
			}
		}
	}

	return orphans
}

// PruneWarmImages removes warm images from the tmpfs ready pool.
func (s *Service) PruneWarmImages(ctx context.Context, dryRun bool) bool {
	warmDir, err := infra.GetWarmImagesDir(ctx)
	if err != nil {
		slog.Warn("cannot inspect warm-image pool", "error", err)
		return false
	}
	if _, err := os.Stat(warmDir); os.IsNotExist(err) {
		return false
	}

	entries, err := os.ReadDir(warmDir)
	if err != nil {
		return false
	}

	if len(entries) == 0 {
		return false
	}

	if dryRun {
		return true
	}

	removed := 0
	for _, entry := range entries {
		fullPath := filepath.Join(warmDir, entry.Name())
		var rmErr error
		if entry.IsDir() {
			rmErr = os.RemoveAll(fullPath)
		} else {
			rmErr = os.Remove(fullPath)
		}
		if rmErr != nil {
			slog.Warn("Failed to remove from warm cache", "entry", entry.Name(), "error", rmErr)
		} else {
			removed++
			slog.Debug("Removed from warm cache", "entry", entry.Name())
		}
	}

	slog.Info("Pruned warm cache", "removed", removed, "total", len(entries))
	return true
}

// CleanStaleProvisionMounts refuses legacy mount state without attempting adoption or path-based unmount.
// Typed LoopMount operations retain private mount namespaces and do not create these legacy directories.
func (s *Service) CleanStaleProvisionMounts(ctx context.Context, dryRun bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, errs.WrapMsg(errs.CodeCacheCleanFailed, "inspect legacy provision state", err)
	}
	entries, err := os.ReadDir(s.tempDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errs.WrapMsg(errs.CodeCacheCleanFailed, "inspect legacy provision state", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), infra.MVMProvisionPrefix) {
			continue
		}
		// CRITICAL: A name, mountpoint observation, or process path is not root-owned authority.
		// Even dry-run refuses this state instead of reporting it as safely reclaimable.
		return false, errs.New(
			errs.CodeCacheCleanFailed,
			"legacy LoopMount state cannot be adopted or automatically cleaned; an administrator must stop the old installation, remove its mounts and processes using independently established ownership, and perform a clean installation",
			errs.WithClass(errs.ClassConflict),
			errs.WithDetails(
				map[string]any{
					"legacy_provision_state": filepath.Join(s.tempDir, entry.Name()),
					"clean_install_required": true,
				},
			),
		)
	}
	return false, nil
}
