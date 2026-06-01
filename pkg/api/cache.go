// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/cache_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/provisioner/guestfs"
	infraslice "mvmctl/internal/infra/slice"
	"mvmctl/internal/infra/system"
)

// CacheCheckPrivileges checks if the current process has the required system privileges
// for destructive cache operations. Returns nil if OK, or an error describing what's missing.
func (op *Operation) CacheCheckPrivileges(binary, operation string) error {
	return system.CheckPrivileges(binary, operation)
}

// CacheSessionHasGroup returns true if the current process has the mvm group active.
func (op *Operation) CacheSessionHasGroup() bool {
	return system.SessionHasGroup()
}

// CacheInitAll initializes all cache directories.
// Matches Python's CacheOperation.init_all() exactly.
func (op *Operation) CacheInitAll(ctx context.Context, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	cacheDir := op.CacheDir
	var created []string

	// Ensure DB schema exists before any DB writes. (Python: Database().migrate())
	if op.Connection != nil {
		if _, err := op.Connection.RunMigrationsCtx(ctx); err != nil {
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
		infra.GetKeyDir,
	}

	for _, fn := range dirFuncs {
		dir := fn()
		created = append(created, dir)
	}

	// Check whether guestfs was enabled by the user
	guestfsEnabled := false
	raw, err := op.ConfigGet(ctx, "settings", "guestfs_enabled")
	if err == nil {
		if b, ok := raw.(bool); ok {
			guestfsEnabled = b
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
		appliancePath, _ = guestfs.BuildAppliance(ctx, cacheDir)
	}

	// Detected guestfs kernel (Python: KernelDetector.find_best_kernel())
	kernelPath, _, _ := guestfs.FindBestKernel(ctx)

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.initialized",
		Message: "Cache initialized successfully",
		Item: map[string]any{
			"cache_dir":         cacheDir,
			"directories":       created,
			"guestfs_appliance": appliancePath,
			"guestfs_kernel":    kernelPath,
		},
	}
}

// CachePruneVMs prunes VMs via VMOperation.Prune.
// Matches Python's CacheOperation.prune_vms() exactly.
func (op *Operation) CachePruneVMs(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	return op.VMPrune(ctx, dryRun, includeAll)
}

// CachePruneNetworks prunes networks via NetworkOperation.Prune.
// Matches Python's CacheOperation.prune_networks() exactly.
func (op *Operation) CachePruneNetworks(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	return op.NetworkPrune(ctx, dryRun, includeAll)
}

// CachePruneImages prunes images via ImageOperation.Prune.
// Matches Python's CacheOperation.prune_images() exactly.
func (op *Operation) CachePruneImages(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	return op.ImagePrune(ctx, dryRun, includeAll)
}

// CachePruneKernels prunes kernels via KernelOperation.Prune.
// Matches Python's CacheOperation.prune_kernels() exactly.
func (op *Operation) CachePruneKernels(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	return op.KernelPrune(ctx, dryRun, includeAll)
}

// CachePruneBinaries prunes binaries via BinaryOperation.Prune.
// Matches Python's CacheOperation.prune_binaries() exactly.
func (op *Operation) CachePruneBinaries(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	return op.BinaryPrune(ctx, dryRun, includeAll)
}

// CachePruneMisc prunes miscellaneous cache items.
func (op *Operation) CachePruneMisc(ctx context.Context, dryRun bool) *errs.OperationResult {
	binDir := infra.GetBinDir()
	serviceBinariesCleaned := false
	if _, err := os.Stat(binDir); err == nil && !dryRun {
		os.RemoveAll(binDir)
		serviceBinariesCleaned = true
	}

	appliancePruned := guestfs.PruneAppliance(op.CacheDir, dryRun)
	warmPruned := op.Services.Cache.PruneWarmImages(ctx, dryRun)
	guestfsStateCleaned := guestfs.CleanStaleState()
	staleProvisionCleaned := op.Services.Cache.CleanStaleProvisionMounts(ctx, dryRun)

	result := map[string]any{
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

// CachePruneAll performs complete cache prune across all resource types.
// Matches Python's CacheOperation.prune_all() exactly.
// Returns OperationResult with item of type *model.PruneAllResult matching Python's PruneAllResult dataclass.
func (op *Operation) CachePruneAll(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune all cache resources")
	if err := system.CheckPrivileges("/usr/sbin/ip", "prune all cache resources"); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	// Detect running VMs (Python: Repository(db).list_all() then check status)
	hadRunningVMs := false
	if op.Repos.VM != nil {
		vms, err := op.Repos.VM.ListAll(ctx)
		if err == nil {
			for _, v := range vms {
				if v.Status == model.StatusRunning || v.Status == model.StatusStarting {
					hadRunningVMs = true
					break
				}
			}
		}
	}

	var prunedIDs []string
	var failedIDs []string

	for _, opResult := range []*errs.OperationResult{
		op.CachePruneVMs(ctx, dryRun, includeAll),
		op.CachePruneNetworks(ctx, dryRun, includeAll),
		op.CachePruneImages(ctx, dryRun, includeAll),
		op.CachePruneKernels(ctx, dryRun, includeAll),
		op.CachePruneBinaries(ctx, dryRun, includeAll),
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

	miscResult := op.CachePruneMisc(ctx, dryRun)
	if miscResult != nil && miscResult.IsOK() && miscResult.Item != nil {
		if misc, ok := miscResult.Item.(map[string]any); ok {
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

// CacheClean performs complete cache clean.
// Matches Python's CacheOperation.clean() exactly.
// Returns OperationResult with item of type *model.CleanResult matching Python's CleanResult dataclass.
func (op *Operation) CacheClean(ctx context.Context, dryRun bool) *errs.OperationResult {
	// Step 1: Prune all cached resources (Python: CacheOperation.prune_all(dry_run=dry_run, include_all=True))
	pruneOpResult := op.CachePruneAll(ctx, dryRun, true)

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
		orphanProcesses := op.Services.Cache.ScanOrphanProcesses(ctx)

		if len(failedIDs) > 0 || len(orphanProcesses) > 0 {
			var messages []string
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
				CacheDir:        op.CacheDir,
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
		_ = op.HostClean(ctx)
	}

	// Step 3: Remove the cache directory itself
	// Python: shutil.rmtree(cache_dir)
	cacheDirRemoved := false
	if _, err := os.Stat(op.CacheDir); err == nil {
		if !dryRun {
			os.RemoveAll(op.CacheDir)
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
		CacheDir:        op.CacheDir,
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.cleaned",
		Message: "Cache cleaned successfully",
		Item:    result,
	}
}
