// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/cache_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/cache"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/host"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/guestfs"
	"mvmctl/internal/infra/model"
	infraslice "mvmctl/internal/infra/slice"
)

// CacheOperation provides cache management orchestration.
// Matches Python's CacheOperation exactly. Prune methods delegate to domain operations.
// Python: HostOperation.clean(cache_dir) is imported and called unconditionally.
// Go: hostOp is a required constructor dependency, matching Python's unconditional availability.
type CacheOperation struct {
	cacheSvc *cache.Service
	vmRepo   vm.Repository
	vmOp     *VMOperation
	netOp    *NetworkOperation
	imgOp    *ImageOperation
	kernOp   *KernelOperation
	binOp    *BinaryOperation
	binSvc   *binary.Service
	cacheDir string
	db       *sql.DB
	hostOp   *HostOperation // required - matching Python's unconditional HostOperation.clean() call
}

// NewCacheOperation creates a CacheOperation.
// hostOp is required (matching Python's unconditional HostOperation import).
func NewCacheOperation(
	cacheSvc *cache.Service,
	vmRepo vm.Repository,
	vmOp *VMOperation,
	netOp *NetworkOperation,
	imgOp *ImageOperation,
	kernOp *KernelOperation,
	binOp *BinaryOperation,
	binSvc *binary.Service,
	cacheDir string,
	db *sql.DB,
	hostOp *HostOperation,
) *CacheOperation {
	return &CacheOperation{
		cacheSvc: cacheSvc,
		vmRepo:   vmRepo,
		vmOp:     vmOp,
		netOp:    netOp,
		imgOp:    imgOp,
		kernOp:   kernOp,
		binOp:    binOp,
		binSvc:   binSvc,
		cacheDir: cacheDir,
		db:       db,
		hostOp:   hostOp,
	}
}

// InitAll initializes all cache directories.
// Matches Python's CacheOperation.init_all() exactly.
// CheckPrivileges checks if the current process has the required system privileges
// for destructive cache operations. Returns nil if OK, or an error describing what's missing.
func (o *CacheOperation) CheckPrivileges(binary, operation string) error {
	helper := &host.PrivilegeHelper{}
	return helper.CheckPrivileges(binary, operation)
}

// SessionHasGroup returns true if the current process has the mvm group active.
func (o *CacheOperation) SessionHasGroup() bool {
	helper := &host.PrivilegeHelper{}
	return helper.SessionHasGroup()
}

func (o *CacheOperation) InitAll(ctx context.Context, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	cacheDir := o.cacheDir
	created := make([]string, 0)

	// Ensure DB schema exists before any DB writes. (Python: Database().migrate())
	if o.db != nil {
		if _, err := db.RunMigrationsCtx(ctx, o.db); err != nil {
			slog.Warn("Failed to run DB migrations during cache init", "error", err)
		}
	}

	// Core directories (Python: CacheUtils.get_vms_dir(), get_images_dir(), etc.)
	// Python: 6 directories: vms, images, kernels, bin, logs, keys
	// Go: Use infra.Get*Dir() functions which create and return the canonical paths.
	dirFuncs := []func() string{
		infra.GetVmsDir,
		infra.GetImagesDir,
		infra.GetKernelsDir,
		infra.GetBinDir,
		infra.GetLogsDir,
		infra.GetKeysDir,
	}

	for _, fn := range dirFuncs {
		dir := fn()
		created = append(created, dir)
	}

	// Check whether guestfs was enabled by the user (Python: SettingsService.resolve(db, "settings", "guestfs_enabled"))
	// Python: try: guestfs_enabled = bool(SettingsService.resolve(db, "settings", "guestfs_enabled"))
	//         except Exception: pass
	guestfsEnabled := false
	if o.db != nil {
		raw, err := config.Resolve(ctx, o.db, "settings", "guestfs_enabled")
		if err == nil {
			if b, ok := raw.(bool); ok {
				guestfsEnabled = b
			}
		}
	}

	// libguestfs fixed appliance (heavy operation) — only when enabled
	// Python: if guestfs_enabled: GuestfsService.build_appliance(cache_dir)
	var appliancePath string
	if guestfsEnabled {
		if onProgress != nil {
			onProgress(errs.ProgressEvent{
				Phase:   "appliance",
				Status:  "running",
				Message: "Building libguestfs appliance...",
			})
		}
		gstSvc := &guestfs.GuestfsService{}
		appliancePath, _ = gstSvc.BuildAppliance(ctx, cacheDir)
	}

	// Detected guestfs kernel (Python: KernelDetector.find_best_kernel())
	kernelPath, _, _ := guestfs.FindBestKernel(ctx)

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.initialized",
		Message: "Cache initialized successfully",
		Item: map[string]interface{}{
			"cache_dir":         cacheDir,
			"directories":       created,
			"guestfs_appliance": appliancePath,
			"guestfs_kernel":    kernelPath,
		},
	}
}

// PruneVMs prunes VMs via VMOperation.Prune.
// Matches Python's CacheOperation.prune_vms() exactly.
func (o *CacheOperation) PruneVMs(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if o.vmOp == nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "cache.pruned",
			Message: "VM prune not available",
		}
	}
	return o.vmOp.Prune(ctx, dryRun, includeAll)
}

// PruneNetworks prunes networks via NetworkOperation.Prune.
// Matches Python's CacheOperation.prune_networks() exactly.
func (o *CacheOperation) PruneNetworks(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if o.netOp == nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "cache.pruned",
			Message: "Network prune not available",
		}
	}
	return o.netOp.Prune(ctx, dryRun, includeAll)
}

// PruneImages prunes images via ImageOperation.Prune.
// Matches Python's CacheOperation.prune_images() exactly.
func (o *CacheOperation) PruneImages(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if o.imgOp == nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "cache.pruned",
			Message: "Image prune not available",
		}
	}
	return o.imgOp.Prune(ctx, dryRun, includeAll)
}

// PruneKernels prunes kernels via KernelOperation.Prune.
// Matches Python's CacheOperation.prune_kernels() exactly.
func (o *CacheOperation) PruneKernels(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if o.kernOp == nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "cache.pruned",
			Message: "Kernel prune not available",
		}
	}
	return o.kernOp.Prune(ctx, dryRun, includeAll)
}

// PruneBinaries prunes binaries via BinaryOperation.Prune.
// Matches Python's CacheOperation.prune_binaries() exactly.
func (o *CacheOperation) PruneBinaries(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if o.binOp == nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "cache.pruned",
			Message: "Binary prune not available",
		}
	}
	return o.binOp.Prune(ctx, dryRun, includeAll)
}

// PruneMisc prunes miscellaneous cache items.
// Matches Python's CacheOperation.prune_misc() exactly.
func (o *CacheOperation) PruneMisc(ctx context.Context, dryRun bool) *errs.OperationResult {
	// Clean service binaries (Python: shutil.rmtree(bin_dir))
	binDir := filepath.Join(o.cacheDir, "bin")
	serviceBinariesCleaned := false
	if _, err := os.Stat(binDir); err == nil && !dryRun {
		os.RemoveAll(binDir)
		serviceBinariesCleaned = true
	}

	// Python:
	//   "service_binaries": shutil.rmtree(bin_dir, ignore_errors=True),
	//   "appliance": GuestfsService.prune_appliance(dry_run),
	//   "warm_images": CacheService.prune_warm_images(dry_run),
	//   "guestfs_state": GuestfsService.clean_stale_guestfs_state(),
	//   "stale_provision_mounts": CacheService.clean_stale_provision_mounts(dry_run),
	//
	// Go: Delegate to GuestfsService directly for appliance and guestfs_state,
	// matching Python's GuestfsService.prune_appliance() and
	// GuestfsService.clean_stale_guestfs_state() calls.
	appliancePruned := (&guestfs.GuestfsService{}).PruneAppliance(dryRun)
	warmPruned := o.cacheSvc.PruneWarmImages(ctx, dryRun)
	guestfsStateCleaned := (&guestfs.GuestfsService{}).CleanStaleGuestfsState()
	staleProvisionCleaned := o.cacheSvc.CleanStaleProvisionMounts(ctx, dryRun)

	result := map[string]interface{}{
		"service_binaries":       serviceBinariesCleaned,
		"appliance":              appliancePruned,
		"warm_images":            warmPruned,
		"guestfs_state":          guestfsStateCleaned,
		"stale_provision_mounts": staleProvisionCleaned,
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: "Misc cache pruned",
		Item:    result,
	}
}

// PruneAll performs complete cache prune across all resource types.
// Matches Python's CacheOperation.prune_all() exactly.
// Returns OperationResult with item of type *model.PruneAllResult matching Python's PruneAllResult dataclass.
func (o *CacheOperation) PruneAll(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune all cache resources")
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "prune all cache resources"); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	// Detect running VMs (Python: Repository(db).list_all() then check status)
	hadRunningVMs := false
	if o.vmRepo != nil {
		vms, err := o.vmRepo.ListAll(ctx)
		if err == nil {
			for _, v := range vms {
				if v.Status == model.StatusRunning || v.Status == model.StatusStarting {
					hadRunningVMs = true
					break
				}
			}
		}
	}

	prunedIDs := make([]string, 0)
	failedIDs := make([]string, 0)

	for _, opResult := range []*errs.OperationResult{
		o.PruneVMs(ctx, dryRun, includeAll),
		o.PruneNetworks(ctx, dryRun, includeAll),
		o.PruneImages(ctx, dryRun, includeAll),
		o.PruneKernels(ctx, dryRun, includeAll),
		o.PruneBinaries(ctx, dryRun, includeAll),
	} {
		if opResult != nil {
			if opResult.IsOK() && opResult.Item != nil {
				if items, ok := opResult.Item.([]string); ok {
					prunedIDs = append(prunedIDs, items...)
				}
			}
			// Collect failed IDs from failed operations.
			// Matches Python's declared (but unused-in-python) failed_ids list.
			if opResult.IsError() && opResult.Item != nil {
				if items, ok := opResult.Item.([]string); ok {
					failedIDs = append(failedIDs, items...)
				}
			}
		}
	}

	miscResult := o.PruneMisc(ctx, dryRun)
	if miscResult != nil && miscResult.IsOK() && miscResult.Item != nil {
		if misc, ok := miscResult.Item.(map[string]interface{}); ok {
			if infraslice.IsTrue(misc["appliance"]) {
				prunedIDs = append(prunedIDs, "appliance")
			}
			if infraslice.IsTrue(misc["warm_images"]) {
				prunedIDs = append(prunedIDs, "warm_images")
			}
			if infraslice.IsTrue(misc["guestfs_state"]) {
				prunedIDs = append(prunedIDs, "guestfs_state")
			}
			if infraslice.IsTrue(misc["stale_provision_mounts"]) {
				prunedIDs = append(prunedIDs, "stale_provision_mounts")
			}
		}
	}

	// Use proper PruneAllResult struct matching Python's PruneAllResult dataclass
	result := &model.PruneAllResult{
		PrunedIDs:     prunedIDs,
		FailedIDs:     failedIDs,
		HadRunningVMs: hadRunningVMs,
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: fmt.Sprintf("Pruned %d item(s)", len(prunedIDs)),
		Item:    result,
	}
}

// Clean performs complete cache clean.
// Matches Python's CacheOperation.clean() exactly.
// Returns OperationResult with item of type *model.CleanResult matching Python's CleanResult dataclass.
func (o *CacheOperation) Clean(ctx context.Context, dryRun bool) *errs.OperationResult {
	// Step 1: Prune all cached resources (Python: CacheOperation.prune_all(dry_run=dry_run, include_all=True))
	pruneOpResult := o.PruneAll(ctx, dryRun, true)

	// Extract PruneAllResult from the prune operation
	var pruneResult *model.PruneAllResult
	if pruneOpResult != nil && pruneOpResult.Item != nil {
		if p, ok := pruneOpResult.Item.(*model.PruneAllResult); ok {
			pruneResult = p
		}
	}

	// Step 1b: Abort if any VMs failed to prune or orphan processes remain.
	// (Python: checks prune_result.failed_ids and CacheService.scan_orphan_processes())
	if !dryRun && pruneResult != nil {
		failedIDs := pruneResult.FailedIDs
		orphanProcesses := o.cacheSvc.ScanOrphanProcesses(ctx)

		if len(failedIDs) > 0 || len(orphanProcesses) > 0 {
			messages := make([]string, 0)
			if len(failedIDs) > 0 {
				messages = append(messages, fmt.Sprintf("Failed to remove %d VM(s): %s",
					len(failedIDs), strings.Join(failedIDs, ", ")))
			}
			if len(orphanProcesses) > 0 {
				pids := make([]string, 0, len(orphanProcesses))
				commSet := make(map[string]struct{})
				for _, p := range orphanProcesses {
					if pid, ok := p["pid"]; ok {
						pids = append(pids, fmt.Sprintf("%v", pid))
					}
					if comm, ok := p["comm"]; ok {
						commSet[fmt.Sprintf("%v", comm)] = struct{}{}
					}
				}
				names := make([]string, 0, len(commSet))
				for n := range commSet {
					names = append(names, n)
				}
				sort.Strings(names)
				messages = append(messages, fmt.Sprintf(
					"Orphan process(es) still running (PID(s) %s: %s). Kill them manually and re-run ``mvm clean``.",
					strings.Join(pids, ", "), strings.Join(names, ", ")))
			}

			result := &model.CleanResult{
				PruneResult:     *pruneResult,
				CacheDirRemoved: false,
				CacheDir:        o.cacheDir,
			}
			return &errs.OperationResult{
				Status:  "error",
				Code:    "cache.clean_failed",
				Message: strings.Join(messages, "; "),
				Item:    result,
			}
		}
	}

	// Step 2: Clean host networking (while DB still exists in cache dir)
	// Python: HostOperation.clean(cache_dir) — unconditional call (hostOp is required constructor param)
	if !dryRun {
		_ = o.hostOp.Clean(ctx, o.cacheDir)
	}

	// Step 3: Remove the cache directory itself
	// Python: shutil.rmtree(cache_dir)
	cacheDirRemoved := false
	if _, err := os.Stat(o.cacheDir); err == nil {
		if !dryRun {
			os.RemoveAll(o.cacheDir)
		}
		cacheDirRemoved = true
	}

	// Build CleanResult (Python: CleanResult(prune_result=..., cache_dir_removed=..., cache_dir=...))
	if pruneResult == nil {
		pruneResult = &model.PruneAllResult{
			PrunedIDs:     []string{},
			FailedIDs:     []string{},
			HadRunningVMs: false,
		}
	}

	result := &model.CleanResult{
		PruneResult:     *pruneResult,
		CacheDirRemoved: cacheDirRemoved,
		CacheDir:        o.cacheDir,
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.cleaned",
		Message: "Cache cleaned successfully",
		Item:    result,
	}
}

// Compile-time check
