// Package cache provides stateless cache cleanup operations — guestfs, appliance, warm images.
// Matches src/mvmctl/core/cache/_service.py exactly.
package cache

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	loopmountsvc "mvmctl/internal/service/loopmount"
)

// Service provides stateless cache cleanup operations.
// Matches Python's CacheService exactly — all methods are the equivalent of
// Python's @staticmethod methods (no instance state needed).
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
// Matches Python's CacheService.scan_orphan_processes() exactly.
//
// Python structure:
//
//	try:
//	    for entry in Path("/proc").iterdir():
//	        if not entry.name.isdigit():
//	            continue
//	        try:
//	            comm = (entry / "comm").read_text().strip()
//	            if comm in _KNOWN_MVM_COMMS:
//	                orphans.append({"pid": int(entry.name), "comm": comm})
//	        except (OSError, PermissionError, ValueError):
//	            continue
//	except PermissionError:
//	    logger.warning(
//	        "Cannot scan /proc for orphan processes (permission denied)"
//	    )
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

		// Check /proc/PID/comm for separate binaries (e.g. firecracker).
		commBytes, err := os.ReadFile(filepath.Join(procPath, "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))

		isKnown := false
		for _, known := range knownMVMComms {
			if comm == known {
				isKnown = true
				break
			}
		}

		// Check /proc/PID/environ for MVM_BACKGROUND_SERVICE=1 marker.
		// Set by SpawnSubprocess for all "mvm run <service>" subprocesses.
		if !isKnown {
			environBytes, err := os.ReadFile(filepath.Join(procPath, "environ"))
			if err != nil {
				continue
			}
			// environ is null-byte separated: "KEY=val\0KEY2=val2\0"
			for _, entry := range bytes.Split(environBytes, []byte{0}) {
				if string(entry) == infra.MVMBackgroundServiceEnv {
					isKnown = true
					break
				}
			}
		}

		if isKnown {
			p, _ := strconv.Atoi(pid)
			orphans = append(orphans, map[string]any{
				"pid":  p,
				"comm": comm,
			})
		}
	}

	return orphans
}

// PruneWarmImages removes warm images from the tmpfs ready pool.
func (s *Service) PruneWarmImages(ctx context.Context, dryRun bool) bool {
	warmDir := infra.GetWarmImagesDir()
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

// CleanStaleProvisionMounts cleans stale provision mount directories in tempDir.
// Matches Python's CacheService.clean_stale_provision_mounts() exactly.
func (s *Service) CleanStaleProvisionMounts(ctx context.Context, dryRun bool) bool {
	cleaned := false

	entries, err := os.ReadDir(s.tempDir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), infra.MVMProvisionPrefix) {
			continue
		}
		if !entry.IsDir() {
			continue
		}

		fullPath := filepath.Join(s.tempDir, entry.Name())

		if !dryRun {
			// Python: single try/except OSError wraps both unmount and rmdir.
			// Both are attempted; if either fails, a single warning is logged.
			var hadError bool
			if isMountPoint(fullPath) {
				slog.Info("Unmounting stale provision mount", "path", fullPath)
				if !loopmountsvc.CleanupMount(fullPath) {
					hadError = true
				}
			}

			slog.Info("Removing stale provision mount point", "path", fullPath)
			if err := os.Remove(fullPath); err != nil {
				hadError = true
			}

			if hadError {
				slog.Warn("Failed to clean stale provision mount", "path", fullPath)
			}
		}

		cleaned = true
	}

	return cleaned
}
