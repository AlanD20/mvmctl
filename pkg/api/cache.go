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
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/provisioner/guestfs"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api/responses"
	"mvmctl/pkg/errs"
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
func (op *Operation) CacheInitAll(
	ctx context.Context,
	onProgress event.OnProgressCallback,
) (*responses.CacheInitResult, error) {
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
		infra.GetKeysDir,
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
			onProgress(event.Progress{
				Phase:   "appliance",
				Status:  "running",
				Message: "Building libguestfs appliance...",
			})
		}
		appliancePath, _ = guestfs.BuildAppliance(ctx, cacheDir)
	}

	// Detected guestfs kernel (Python: KernelDetector.find_best_kernel())
	kernelPath, _, _ := guestfs.FindBestKernel(ctx)

	return &responses.CacheInitResult{
		CacheDir:         cacheDir,
		Directories:      created,
		GuestfsAppliance: appliancePath,
		GuestfsKernel:    kernelPath,
	}, nil
}

// CachePruneVMs prunes VMs via VMOperation.Prune.
// Matches Python's CacheOperation.prune_vms() exactly.
func (op *Operation) CachePruneVMs(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	ids, err := op.VMPrune(ctx, dryRun, includeAll)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "cache.prune_failed",
			Message:   fmt.Sprintf("Failed to prune VMs: %v", err),
			Exception: err,
		}
	}
	return &errs.OperationResult{
		Status: "success",
		Code:   "cache.pruned",
		Item:   ids,
	}
}

// CachePruneNetworks prunes networks via NetworkOperation.Prune.
// Matches Python's CacheOperation.prune_networks() exactly.
func (op *Operation) CachePruneNetworks(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	return op.NetworkPrune(ctx, dryRun, includeAll)
}

// CachePruneImages prunes images via ImageOperation.Prune.
// Matches Python's CacheOperation.prune_images() exactly.
func (op *Operation) CachePruneImages(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	return op.ImagePrune(ctx, dryRun, includeAll)
}

// CachePruneKernels prunes kernels via KernelOperation.Prune.
// Matches Python's CacheOperation.prune_kernels() exactly.
func (op *Operation) CachePruneKernels(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	return op.KernelPrune(ctx, dryRun, includeAll)
}

// CachePruneBinaries prunes binaries via BinaryOperation.Prune.
// Matches Python's CacheOperation.prune_binaries() exactly.
func (op *Operation) CachePruneBinaries(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	return op.BinaryPrune(ctx, dryRun, includeAll)
}

// CachePruneMisc prunes miscellaneous cache items.
func (op *Operation) CachePruneMisc(ctx context.Context, dryRun bool) (map[string]any, error) {
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

	return result, nil
}

// CachePruneAll performs complete cache prune across all resource types.
// Matches Python's CacheOperation.prune_all() exactly.
// Returns *model.PruneAllResult matching Python's PruneAllResult dataclass.
func (op *Operation) CachePruneAll(ctx context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error) {
	// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune all cache resources")
	if err := system.CheckPrivileges("/usr/sbin/ip", "prune all cache resources"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("Privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}

	// Detect running VMs (Python: Repository(db).list_all() then check status)
	hadRunningVMs := false
	if op.Repos.VM != nil {
		vms, err := op.Repos.VM.ListAll(ctx)
		if err == nil {
			for _, v := range vms {
				if v.Status == model.VMStatusRunning || v.Status == model.VMStatusStarting {
					hadRunningVMs = true
					break
				}
			}
		}
	}

	// Prune each resource type — collect IDs from results.
	var prunedIDs []string
	var failedIDs []string

	// CachePruneVMs still returns *errs.OperationResult (not yet refactored)
	pruneVMsResult := op.CachePruneVMs(ctx, dryRun, includeAll)
	if pruneVMsResult != nil && pruneVMsResult.IsOK() && pruneVMsResult.Item != nil {
		if items, ok := pruneVMsResult.Item.([]string); ok {
			prunedIDs = append(prunedIDs, items...)
		}
	}

	netIDs, err := op.CachePruneNetworks(ctx, dryRun, includeAll)
	if err == nil {
		prunedIDs = append(prunedIDs, netIDs...)
	}
	imgIDs, err := op.CachePruneImages(ctx, dryRun, includeAll)
	if err == nil {
		prunedIDs = append(prunedIDs, imgIDs...)
	}
	kernelIDs, err := op.CachePruneKernels(ctx, dryRun, includeAll)
	if err == nil {
		prunedIDs = append(prunedIDs, kernelIDs...)
	}
	binIDs, err := op.CachePruneBinaries(ctx, dryRun, includeAll)
	if err == nil {
		prunedIDs = append(prunedIDs, binIDs...)
	}

	miscMap, miscErr := op.CachePruneMisc(ctx, dryRun)
	if miscErr == nil {
		if infra.IsTrue(miscMap["appliance"]) {
			prunedIDs = append(prunedIDs, "appliance")
		}
		if infra.IsTrue(miscMap["warm_images"]) {
			prunedIDs = append(prunedIDs, "warm_images")
		}
		if infra.IsTrue(miscMap["guestfs_state"]) {
			prunedIDs = append(prunedIDs, "guestfs_state")
		}
		if infra.IsTrue(miscMap["stale_provision_mounts"]) {
			prunedIDs = append(prunedIDs, "stale_provision_mounts")
		}
	}

	// Use proper PruneAllResult struct matching Python's PruneAllResult dataclass
	result := &model.PruneAllResult{
		PrunedIDs:     prunedIDs,
		FailedIDs:     failedIDs,
		HadRunningVMs: hadRunningVMs,
	}

	return result, nil
}

// CacheClean performs complete cache clean.
// Matches Python's CacheOperation.clean() exactly.
// Returns *model.CleanResult matching Python's CleanResult dataclass.
func (op *Operation) CacheClean(ctx context.Context, dryRun bool) (*model.CleanResult, error) {
	// Step 1: Prune all cached resources (Python: CacheOperation.prune_all(dry_run=dry_run, include_all=True))
	pruneResult, pruneErr := op.CachePruneAll(ctx, dryRun, true)

	// Step 2: Abort if any VMs failed to prune or orphan processes remain post-prune.
	// (Python: checks prune_result.failed_ids and CacheService.scan_orphan_processes())
	if !dryRun && pruneErr == nil && pruneResult != nil {
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

			return &model.CleanResult{
				PruneResult:     *pruneResult,
				CacheDirRemoved: false,
				CacheDir:        op.CacheDir,
			}, errs.New(errs.CodeCacheCleanFailed, strings.Join(messages, "; "))
		}
	}

	// Step 3: Clean host networking (while DB still exists in cache dir)
	// Python: HostOperation.clean(cache_dir) — unconditional call (hostOp is required constructor param)
	if !dryRun {
		_, _ = op.HostClean(ctx)
	}

	// Step 4: Remove the cache directory itself
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

	return &model.CleanResult{
		PruneResult:     *pruneResult,
		CacheDirRemoved: cacheDirRemoved,
		CacheDir:        op.CacheDir,
	}, nil
}
