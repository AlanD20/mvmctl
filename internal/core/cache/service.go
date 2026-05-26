// Package cache provides stateless cache cleanup operations — guestfs, appliance, warm images.
// Matches src/mvmctl/core/cache/_service.py exactly.
package cache

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/guestfs"
	loopmountsvc "mvmctl/internal/service/loopmount"
)

// knownMVMComms lists mvmctl-managed process comm names.
// Matches Python's _KNOWN_MVM_COMMS frozenset exactly (unexported).
var knownMVMComms = map[string]struct{}{
	"firecracker":   {},
	"mvm-provision": {},
	"mvm-services":  {},
}

// Service provides stateless cache cleanup operations.
// Matches Python's CacheService exactly — all methods are the equivalent of
// Python's @staticmethod methods (no instance state needed).
// The cacheDir field is kept only for delegation to loopmount operations that
// require it for binary path resolution.
type Service struct {
	cacheDir string
}

// NewService creates a new CacheService.
func NewService(cacheDir string) *Service {
	return &Service{
		cacheDir: cacheDir,
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
func (s *Service) ScanOrphanProcesses(ctx context.Context) []map[string]interface{} {
	var orphans []map[string]interface{}

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

		// Python inner try: comm = (entry / "comm").read_text().strip()
		// Python except (OSError, PermissionError, ValueError): continue
		commBytes, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))

		if _, ok := knownMVMComms[comm]; ok {
			pid, _ := strconv.Atoi(entry.Name())
			orphans = append(orphans, map[string]interface{}{
				"pid":  pid,
				"comm": comm,
			})
		}
	}

	return orphans
}

// CleanStaleGuestfsState removes stale libguestfs processes, locks, sockets, and caches.
// Matches Python's CacheService.clean_stale_guestfs_state() exactly.
// Delegates to GuestfsService. Returns True if any stale state was removed.
func (s *Service) CleanStaleGuestfsState(ctx context.Context) bool {
	// Python: return GuestfsService.clean_stale_guestfs_state()
	// GuestfsService is an empty struct (stateless), matching Python's classmethod pattern.
	return (&guestfs.GuestfsService{}).CleanStaleGuestfsState()
}

// PruneAppliance removes the libguestfs appliance folder and stale system state.
// Matches Python's CacheService.prune_appliance().
// Delegates to GuestfsService.
func (s *Service) PruneAppliance(ctx context.Context, dryRun bool) bool {
	// Python: return GuestfsService.prune_appliance(dry_run)
	return (&guestfs.GuestfsService{}).PruneAppliance(s.cacheDir, dryRun)
}

// PruneWarmImages removes warm images from the tmpfs ready pool.
// Matches Python's CacheService.prune_warm_images() exactly.
//
// Python implementation:
//
//	warm_dir = CacheUtils.get_warm_image_dir()
//	if not warm_dir.exists():
//	    return False
//	has_content = any(warm_dir.iterdir())
//	if not has_content:
//	    return False
//	if not dry_run:
//	    for item in warm_dir.iterdir():
//	        try:
//	            if item.is_dir():
//	                shutil.rmtree(item)
//	            else:
//	                item.unlink()
//	        except OSError:
//	            pass
//	return True
func (s *Service) PruneWarmImages(ctx context.Context, dryRun bool) bool {
	warmDir := infra.GetWarmImageDir("")
	if _, err := os.Stat(warmDir); os.IsNotExist(err) {
		return false
	}

	// Python: has_content = any(warm_dir.iterdir())
	// iter() never yields "." or "..", so standard ReadDir is equivalent.
	entries, err := os.ReadDir(warmDir)
	if err != nil {
		return false
	}

	hasContent := len(entries) > 0
	if !hasContent {
		return false
	}

	if !dryRun {
		for _, entry := range entries {
			fullPath := filepath.Join(warmDir, entry.Name())
			// Python: except OSError: pass — silently skip items that can't be removed
			if entry.IsDir() {
				_ = os.RemoveAll(fullPath)
			} else {
				_ = os.Remove(fullPath)
			}
		}
	}
	return true
}

// CleanStaleProvisionMounts cleans stale mvm-provision mount directories in /tmp/.
// Matches Python's CacheService.clean_stale_provision_mounts() exactly.
//
// Python implementation:
//
//	tmp = Path("/tmp")
//	cleaned = False
//	for path in tmp.glob("mvm-provision-*"):
//	    if not path.is_dir():
//	        continue
//	    if not dry_run:
//	        try:
//	            if path.is_mount():
//	                logger.info("Unmounting stale provision mount: %s", path)
//	                LoopMountManager.cleanup_mount(str(path))
//	            logger.info("Removing stale provision mount point: %s", path)
//	            path.rmdir()
//	        except OSError:
//	            logger.warning("Failed to clean stale provision mount: %s", path)
//	    cleaned = True
//	return cleaned
func (s *Service) CleanStaleProvisionMounts(ctx context.Context, dryRun bool) bool {
	tmp := "/tmp"
	cleaned := false

	entries, err := os.ReadDir(tmp)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "mvm-provision-") {
			continue
		}
		if !entry.IsDir() {
			continue
		}

		fullPath := filepath.Join(tmp, entry.Name())

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

// isMountPoint checks if a path is currently a mount point by comparing
// device numbers (st_dev) of path and its parent — matching Python's
// path.is_mount() behavior exactly.
func isMountPoint(path string) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	var parentSt syscall.Stat_t
	parent := filepath.Dir(path)
	if err := syscall.Stat(parent, &parentSt); err != nil {
		return false
	}
	return st.Dev != parentSt.Dev
}
