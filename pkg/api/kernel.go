// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/kernel_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/operation"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// KernelPrune prunes unused kernels.
// Matches Python's KernelOperation.prune() exactly.
// Python's prune() calls KernelOperation.remove() through the full pipeline
// (resolution, enrichment, VM reference checks) — Go matches this by calling
// op.KernelRemove() instead of calling the service directly.
func (op *Operation) KernelPrune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	allKernels, err := op.Repos.Kernel.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list kernels: %v", err),
			Exception: err,
		}
	}

	defaultItem, _ := op.Repos.Kernel.GetDefault(ctx)
	var defaultID string
	if defaultItem != nil {
		defaultID = defaultItem.ID
	}

	// Get referenced kernel IDs from VMs (matches Python's vm_repo.list_all() + kernel_id check)
	vms, _ := op.Repos.VM.ListAll(ctx)
	referencedKernelIDs := make(map[string]bool)
	for _, vm := range vms {
		if vm.KernelID != "" {
			referencedKernelIDs[vm.KernelID] = true
		}
	}

	var removed []string
	for _, kernel := range allKernels {
		if !includeAll {
			if kernel.ID == defaultID {
				continue
			}
			if referencedKernelIDs[kernel.ID] {
				continue
			}
		}

		if !dryRun {
			// Python: KernelOperation.remove(KernelInput(id=[kernel.id]), force=include_all)
			// Go: call KernelRemove() through the full pipeline.
			result := op.KernelRemove(ctx, []string{kernel.ID}, includeAll)
			if result.HasErrors() {
				slog.Warn("Failed to remove kernel", "id", kernel.ID, "error", result.Errors()[0].Message)
				continue
			}
		}
		removed = append(removed, kernel.ID)
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: fmt.Sprintf("Pruned %d kernel(s)", len(removed)),
		Item:    removed,
	}
}

// KernelPull downloads or builds a kernel with full pipeline.
// Matches Python's KernelOperation.pull(inputs: KernelPullInput, *, on_progress=...) exactly.
func (op *Operation) KernelPull(ctx context.Context, input *inputs.KernelPullInput,
	onProgress func(errs.ProgressEvent)) *errs.OperationResult {

	try := func(phase, status, msg string) {
		if onProgress != nil {
			onProgress(errs.ProgressEvent{
				Phase: phase, Status: status, Message: msg,
			})
		}
	}

	kernelType := input.KernelType
	version := ""
	if input.Version != nil {
		version = *input.Version
	}

	// Phase 1: Resolve "latest" version to concrete version (matches Python).
	if version == "latest" {
		ciVersion := ""
		if kernelType == "firecracker" {
			binRepo := binary.NewRepository(nil)
			defaultBin, _ := binRepo.GetDefault(ctx, "firecracker")
			if defaultBin != nil && defaultBin.CIVersion != nil {
				ciVersion = *defaultBin.CIVersion
			}
		}
		if ciVersion == "" {
			return &errs.OperationResult{
				Status:  "error",
				Code:    "kernel.pull_failed",
				Message: "CI version is required to resolve latest kernel version",
			}
		}
		resolvedVersion, err := op.Services.Kernel.ResolveLatestVersion(ctx, kernelType, ciVersion)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "kernel.pull_failed",
				Message:   fmt.Sprintf("Failed to resolve latest version for '%s': %v", kernelType, err),
				Exception: err,
			}
		}
		version = resolvedVersion
		input.Version = &version
	}

	// Resolve through the Request pipeline (matches Python)
	request := inputs.NewKernelPullRequest(*input, op.Connection.DB())
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.pull_failed",
			Message:   fmt.Sprintf("Kernel pull input resolution failed: %v", err),
			Exception: err,
		}
	}

	// Check for existing kernel (matches Python)
	var existing *model.KernelItem
	if resolved.KernelType == "firecracker" {
		existing, _ = op.Repos.Kernel.GetByType(ctx, resolved.KernelType)
	} else if resolved.KernelType == "official" && resolved.Version != nil && *resolved.Version != "" {
		existing, _ = op.Repos.Kernel.GetByVersionAndType(ctx, *resolved.Version, resolved.KernelType)
	}

	if existing != nil {
		resolvedPath := existing.Path
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(infra.GetKernelsDir(), resolvedPath)
		}
		if _, err := os.Stat(resolvedPath); err == nil {
			slog.Info("Kernel already exists", "path", existing.Path)
			if resolved.SetDefault {
				_ = op.Repos.Kernel.SetDefault(ctx, existing.ID)
			}
			return &errs.OperationResult{
				Status:  "skipped",
				Code:    "kernel.already_present",
				Message: fmt.Sprintf("Kernel already exists: %s", existing.Path),
				Item:    existing,
			}
		}
	}

	// Resolve spec via KernelService (matches Python)
	specs, err := op.Services.Kernel.GetSpecsFor(nil, resolved.KernelType, resolvedVersionStr(resolved))
	if err != nil {
		return &errs.OperationResult{
			Status: "error",
			Code:   "kernel.pull_failed",
			Message: fmt.Sprintf(
				"Failed to get spec for '%s' version '%s': %v",
				resolved.KernelType,
				resolvedVersionStr(resolved),
				err,
			),
			Exception: err,
		}
	}
	if len(specs) != 1 {
		return &errs.OperationResult{
			Status: "error",
			Code:   "kernel.pull_failed",
			Message: fmt.Sprintf(
				"Expected exactly one kernel spec for type='%s' version='%s', got %d",
				resolved.KernelType,
				resolvedVersionStr(resolved),
				len(specs),
			),
			Exception: fmt.Errorf("unexpected spec count: %d", len(specs)),
		}
	}
	spec := specs[0]

	// Note: Python enables feature configs via spec.WithEnabledFeatures(resolved.Features),
	// but this method was not ported to Go. Features are resolved at YAML load time in the spec.

	var fetchResult *model.KernelPullResult

	// ── Dispatch based on kernel type (matches Python exactly) ──
	if resolved.KernelType == "firecracker" {

		binDir := filepath.Join(op.CacheDir, "bin")
		binaryService := binary.NewService(binary.NewRepository(op.Connection.DB()), binDir, op.CacheDir)
		defaultFirecracker, _ := binaryService.GetDefaultFirecracker(ctx)
		ciVersion := infra.DefaultFirecrackerCIVersion
		if defaultFirecracker != nil && defaultFirecracker.CIVersion != nil {
			ciVersion = *defaultFirecracker.CIVersion
		}

		try("download", "running", "Downloading Firecracker kernel...")

		fetchResult, err = op.Services.Kernel.FetchFirecrackerKernel(
			ctx,
			spec,
			ciVersion,
			resolved.Arch,
			resolved.OutputDir,
			operation.DownloadProgressBridge(onProgress),
		)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "kernel.pull_failed",
				Message:   fmt.Sprintf("Firecracker kernel download failed: %v", err),
				Exception: err,
			}
		}

		try("download", "complete", "Firecracker kernel download complete.")
	} else if resolved.KernelType == "official" {
		try("build", "running", "Building kernel (this may take a while)...")

		var configPath *string
		if resolved.KernelConfig != nil && *resolved.KernelConfig != "" {
			configPath = resolved.KernelConfig
		}

		// Python passes TWO callbacks to build_official_kernel():
		//   progress_callback=OperationUtils.download_progress_bridge(on_progress)
		//   on_status=lambda msg: on_progress(ProgressEvent(phase="build", status="running", message=msg))
		var onStatusCallback func(string)
		if onProgress != nil {
			onStatusCallback = func(msg string) {
				onProgress(errs.ProgressEvent{
					Phase: "build", Status: "running", Message: msg,
				})
			}
		}

		fetchResult, err = op.Services.Kernel.BuildOfficialKernel(ctx, spec, resolved.Arch, resolved.OutputDir,
			resolved.Jobs, resolved.KeepBuildDir, !resolved.CleanBuild, // useCache = !cleanBuild
			configPath, operation.DownloadProgressBridge(onProgress), onStatusCallback)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "kernel.pull_failed",
				Message:   fmt.Sprintf("Kernel build failed: %v", err),
				Exception: err,
			}
		}

		try("build", "complete", "Kernel build complete.")
	} else {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.pull_failed",
			Message:   fmt.Sprintf("Unsupported kernel type: %s", resolved.KernelType),
			Exception: fmt.Errorf("unsupported kernel type: %s", resolved.KernelType),
		}
	}

	// Generate kernel ID in the API layer (matches Python exactly)
	timestamp := time.Now().Format(time.RFC3339)
	kernelID, err := infra.HashGenerator{}.Kernel(fetchResult.Path, fetchResult.Version, resolved.Arch, timestamp)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.pull_failed",
			Message:   fmt.Sprintf("Failed to compute kernel ID: %v", err),
			Exception: err,
		}
	}

	// Parse filename for base_name (matches Python)
	parsed := kernel.ParseFilename(filepath.Base(fetchResult.Path))

	// Create KernelItem (matches Python exactly)
	kernelItem := &model.KernelItem{
		ID:        kernelID,
		Name:      filepath.Base(fetchResult.Path),
		BaseName:  parsed.BaseName,
		Version:   fetchResult.Version,
		Arch:      resolved.Arch,
		Type:      resolved.KernelType,
		Path:      fetchResult.Path,
		IsDefault: false,
		IsPresent: true,
		CreatedAt: timestamp,
		UpdatedAt: timestamp,
	}

	if err := op.Repos.Kernel.Upsert(ctx, kernelItem); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.pull_failed",
			Message:   fmt.Sprintf("Failed to persist kernel: %v", err),
			Exception: err,
		}
	}

	if resolved.SetDefault {
		_ = op.Repos.Kernel.SetDefault(ctx, kernelItem.ID)
	}

	// Clean up old kernel file if ID changed and path is different
	// Python compares old_path.resolve() != new_path before deleting.
	if existing != nil && existing.ID != kernelItem.ID {
		oldPath := existing.Path
		if !filepath.IsAbs(oldPath) {
			oldPath = filepath.Join(infra.GetKernelsDir(), oldPath)
		}
		newPath := kernelItem.Path
		// Resolve both paths to compare actual filesystem locations (matching Python's .resolve())
		oldPathResolved, _ := filepath.EvalSymlinks(oldPath)
		newPathResolved, _ := filepath.EvalSymlinks(newPath)
		if oldPathResolved != newPathResolved && oldPathResolved != "" {
			if _, err := os.Stat(oldPathResolved); err == nil {
				os.Remove(oldPathResolved)
				slog.Info(
					"Cleaned up old kernel file",
					"type",
					resolved.KernelType,
					"version",
					resolvedVersionStr(resolved),
				)
			}
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("kernel.pull", map[string]interface{}{
		"id": kernelItem.ID, "type": kernelItem.Type,
		"version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")

	md := map[string]interface{}{}
	if len(resolved.Features) > 0 {
		md["features"] = resolved.Features
	}

	return &errs.OperationResult{
		Status:   "success",
		Code:     "kernel.pulled",
		Message:  fmt.Sprintf("Kernel '%s' pulled successfully", kernelItem.Name),
		Item:     kernelItem,
		Metadata: md,
	}
}

// KernelImport imports a local vmlinux file as a kernel.
// Matches Python's KernelOperation.import_() exactly — uses KernelImportRequest
// resolution pipeline for input validation and default resolution before
// calling service.import_kernel().
func (op *Operation) KernelImport(ctx context.Context, input *inputs.KernelImportInput) *errs.OperationResult {
	db := op.Connection.DB()

	// Python: request = KernelImportRequest(inputs=inputs, db=db); resolved = request.resolve()
	request := inputs.NewKernelImportRequest(*input, db)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.import_failed",
			Message:   fmt.Sprintf("Kernel import input resolution failed: %v", err),
			Exception: err,
		}
	}

	kernelItem, err := op.Services.Kernel.ImportKernel(
		ctx,
		resolved.Name,
		resolved.Path,
		resolved.Version,
		resolved.Arch,
		resolved.SetDefault,
	)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.import_failed",
			Message:   fmt.Sprintf("Kernel import failed: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("kernel.import", map[string]interface{}{
		"name": kernelItem.Name, "version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "kernel.imported",
		Message: fmt.Sprintf("Kernel imported: %s", kernelItem.Name),
		Item:    kernelItem,
	}
}

// KernelRemove removes kernels by identifiers.
// Matches Python's KernelOperation.remove() exactly — uses KernelRequest.resolve()
// to resolve identifiers, then enriches with VM references.
// Each kernel removal is wrapped in per-kernel error handling (matching Python's
// try/except KernelError) and the method parameter force is combined with
// resolved.Force (matching Python's force=force or resolved.force).
func (op *Operation) KernelRemove(ctx context.Context, identifiers []string, force bool) *errs.BatchResult {
	forceVal := force
	kernelInput := inputs.KernelInput{
		ID:    identifiers,
		Force: &forceVal,
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:    "error",
					Code:      string(errs.CodeKernelNotFound),
					Message:   fmt.Sprintf("Failed to resolve kernels: %v", err),
					Exception: err,
				},
			},
		}
	}

	items := make([]errs.OperationResult, 0)

	// Batch-enrich with VM references (matches Python's Resolver(repo, include=["vm"]).enrich())
	if op.Enr != nil {
		_ = op.Enr.EnrichKernel(ctx, resolved.Kernels)
	}

	for _, kernel := range resolved.Kernels {

		// Python: force = force or resolved.force — combine method param with resolved
		effectiveForce := force || resolved.Force

		if !effectiveForce && len(kernel.VMs) > 0 {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "kernel.in_use",
				Message:   fmt.Sprintf("Kernel '%s' is in use by %d VM(s)", kernel.Name, len(kernel.VMs)),
				Exception: fmt.Errorf("kernel in use by %d VMs", len(kernel.VMs)),
			})
			continue
		}

		if _, err := op.Services.Kernel.Remove(ctx, kernel, effectiveForce); err != nil {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "kernel.remove_failed",
				Message:   fmt.Sprintf("Failed to remove kernel %s: %v", kernel.Name, err),
				Exception: err,
				Item:      kernel,
			})
			continue
		}

		auditLog := logging.NewAuditLog(op.CacheDir)
		_ = auditLog.LogOperation("kernel.remove", map[string]interface{}{
			"id": kernel.ID, "name": kernel.Name, "type": kernel.Type,
		}, "")

		items = append(items, errs.OperationResult{
			Status:  "success",
			Code:    "kernel.removed",
			Message: fmt.Sprintf("Removed kernel %s", kernel.Name),
			Item:    kernel,
		})
	}

	return &errs.BatchResult{Items: items}
}

// KernelList returns locally cached or remote kernel listing.
// Matches Python's KernelOperation.list_all() exactly.
// When remote=false, returns ([]*model.KernelItem, nil, error).
// When remote=true, returns (nil, []model.VersionInfo, error).
func (op *Operation) KernelList(
	ctx context.Context,
	remote bool,
	noCache bool,
) ([]*model.KernelItem, []model.VersionInfo, error) {
	if remote {
		versions, err := op.kernelListRemote(ctx, noCache)
		return nil, versions, err
	}
	items, err := op.Services.Kernel.List(ctx)
	return items, nil, err
}

// kernelListRemote returns available remote kernel versions as a flat list.
// Matches Python's KernelOperation._list_remote() — resolves cache_ttl,
// ci_version, and remote_list_limit from SettingsService before calling
// the HttpDirVersionResolver with a limit parameter.
func (op *Operation) kernelListRemote(ctx context.Context, noCache bool) ([]model.VersionInfo, error) {
	// Load kernel specs
	specs, err := op.Services.Kernel.LoadSpecs()
	if err != nil {
		return nil, fmt.Errorf("failed to load kernel specs: %w", err)
	}
	allSpecs := make([]*model.KernelSpec, 0, len(specs))
	for _, spec := range specs {
		allSpecs = append(allSpecs, spec)
	}

	// Resolve cache_ttl from settings (matches Python)
	var cacheTTL *int
	if !noCache && op.Services.Config != nil {
		v, err := op.Services.Config.Get(ctx, "defaults.kernel", "remote_list_cache_ttl")
		if err == nil && v != nil {
			if s, ok := v.(string); ok {
				if i, parseErr := strconv.Atoi(s); parseErr == nil {
					cacheTTL = &i
				}
			}
		}
	}

	// Resolve ci_version from default firecracker binary (matches Python)
	resolvedCIVersion := ""
	if op.Services.Config != nil {
		binaryRepo := binary.NewRepository(op.Connection.DB())
		binDir := filepath.Join(op.CacheDir, "bin")
		binaryService := binary.NewService(binaryRepo, binDir, op.CacheDir)
		defaultFC, _ := binaryService.GetDefaultFirecracker(ctx)
		if defaultFC != nil && defaultFC.CIVersion != nil {
			resolvedCIVersion = *defaultFC.CIVersion
		}
	}

	// Resolve remote_list_limit from settings (matches Python)
	// Python: remote_list_limit = int(SettingsService.resolve(db, "defaults.kernel", "remote_list_limit"))
	remoteListLimit := 0
	if op.Services.Config != nil {
		v, err := op.Services.Config.Get(ctx, "defaults.kernel", "remote_list_limit")
		if err == nil && v != nil {
			switch val := v.(type) {
			case string:
				if i, parseErr := strconv.Atoi(val); parseErr == nil {
					remoteListLimit = i
				}
			case int:
				remoteListLimit = val
			case int64:
				remoteListLimit = int(val)
			case float64:
				remoteListLimit = int(val)
			}
		}
	}

	cacheTTLVal := 0
	if cacheTTL != nil {
		cacheTTLVal = *cacheTTL
	}
	versionMap := op.Services.Kernel.ListRemoteVersions(
		ctx,
		allSpecs,
		"x86_64",
		resolvedCIVersion,
		cacheTTLVal,
		remoteListLimit,
	)
	flattened := make([]model.VersionInfo, 0)
	for _, versions := range versionMap {
		flattened = append(flattened, versions...)
	}
	return flattened, nil
}

// KernelGet returns a single kernel by identifier.
// Matches Python's KernelOperation.get() exactly — uses KernelRequest.resolve()
// internally for consistent resolution behavior.
func (op *Operation) KernelGet(ctx context.Context, id string) (*model.KernelItem, error) {
	kernelInput := inputs.KernelInput{
		ID: []string{id},
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("kernel not found: %s", id)
	}

	if len(resolved.Kernels) != 1 {
		// Python: raise KernelError(f"Expected exactly one kernel, got {len(resolved.kernels)}")
		return nil, fmt.Errorf("Expected exactly one kernel, got %d", len(resolved.Kernels))
	}

	return resolved.Kernels[0], nil
}

// KernelInspect returns grouped dict of a kernel.
// Matches Python's KernelOperation.inspect() exactly.
func (op *Operation) KernelInspect(ctx context.Context, id string) (*responses.KernelInspect, error) {
	k, err := op.KernelGet(ctx, id)
	if err != nil {
		return nil, err
	}
	return &responses.KernelInspect{
		Kernel: responses.KernelItemInfo{
			ID: k.ID, Name: k.Name, BaseName: k.BaseName,
			Version: k.Version, Arch: k.Arch, Type: k.Type,
			IsDefault: k.IsDefault, IsPresent: k.IsPresent,
		},
		Storage: responses.KernelStorageInfo{Path: k.Path},
		Timestamps: responses.KernelTimestampsInfo{
			CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
		},
	}, nil
}

// KernelSetDefault sets a kernel as default.
// Matches Python's KernelOperation.set_default() exactly — uses KernelRequest.resolve()
// for consistent identifier resolution, catches KernelError at top level.
func (op *Operation) KernelSetDefault(ctx context.Context, id string) *errs.OperationResult {
	kernelInput := inputs.KernelInput{
		ID: []string{id},
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeKernelNotFound),
			Message: fmt.Sprintf("Kernel not found: %s", id),
		}
	}

	if len(resolved.Kernels) != 1 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeKernelNotFound),
			Message: fmt.Sprintf("Kernel not found: %s", id),
		}
	}

	kItem := resolved.Kernels[0]

	ctrl, err := kernel.NewController(ctx, kItem, op.Repos.Kernel)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.default_set_failed",
			Message:   fmt.Sprintf("Failed to create controller: %v", err),
			Exception: err,
		}
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "kernel.default_set_failed",
			Message:   fmt.Sprintf("Failed to set default kernel: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("kernel.set_default", map[string]interface{}{"name": kItem.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "kernel.default_set",
		Message: fmt.Sprintf("Default kernel set to %s", kItem.Name),
		Item:    kItem,
	}
}

// resolvedVersionStr returns the version from a ResolvedKernelPullRequest,
// handling the nil pointer case.
func resolvedVersionStr(resolved *inputs.ResolvedKernelPullRequest) string {
	if resolved.Version != nil {
		return *resolved.Version
	}
	return ""
}
