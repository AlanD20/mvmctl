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

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/operation"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// KernelPrune prunes unused kernels.
// Matches Python's KernelOperation.prune() exactly.
// Python's prune() calls KernelOperation.remove() through the full pipeline
// (resolution, enrichment, VM reference checks) — Go matches this by calling
// op.KernelRemove() instead of calling the service directly.
func (op *Operation) KernelPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	allKernels, err := op.Repos.Kernel.ListAll(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeDatabaseError, Message: fmt.Sprintf("Failed to list kernels: %v", err), Err: err,
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

	return removed, nil
}

// KernelPull downloads or builds a kernel with full pipeline.
// Matches Python's KernelOperation.pull(inputs: KernelPullInput, *, on_progress=...) exactly.
func (op *Operation) KernelPull(ctx context.Context, input *inputs.KernelPullInput,
	onProgress func(errs.ProgressEvent)) (*model.KernelItem, error) {

	kernelType := input.KernelType
	version := ""
	if input.Version != "" {
		version = input.Version
	}

	// Phase 1: Resolve "latest" version to concrete version (matches Python).
	if version == "latest" {
		ciVersion := infra.DefaultFirecrackerCIVersion
		if kernelType == "firecracker" {
			defaultFC, _ := op.Services.Binary.GetDefaultFirecracker(ctx)
			if defaultFC != nil && defaultFC.CIVersion != nil {
				ciVersion = *defaultFC.CIVersion
			}
		}
		if ciVersion == "" {
			return nil, &errs.DomainError{
				Code: "kernel.pull_failed", Message: "CI version is required to resolve latest kernel version",
			}
		}
		// Arch is resolved here for ResolveLatestVersion; the request.Resolve()
		// below also resolves it independently — both use system.RuntimeArch(),
		// so the value is identical (compile-time constant).
		arch := system.RuntimeArch()
		resolvedVersion, err := op.Services.Kernel.ResolveLatestVersion(ctx, kernelType, arch, ciVersion)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    "kernel.pull_failed",
				Message: fmt.Sprintf("Failed to resolve latest version for '%s': %v", kernelType, err),
				Err:     err,
			}
		}
		version = resolvedVersion
		input.Version = version
	}

	// Resolve through the Request pipeline (matches Python)
	request := inputs.NewKernelPullRequest(*input, op.Connection.DB())
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed", Message: fmt.Sprintf("Kernel pull input resolution failed: %v", err), Err: err,
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
		if _, err := os.Stat(existing.Path); err == nil {
			slog.Info("Kernel already exists", "path", existing.Path)
			if resolved.SetDefault {
				_ = op.Repos.Kernel.SetDefault(ctx, existing.ID)
			}
			return existing, nil
		}
	}

	// Resolve spec via KernelService (matches Python)
	resolvedVersion := ""
	if resolved.Version != nil {
		resolvedVersion = *resolved.Version
	}
	specs, err := op.Services.Kernel.GetSpecsFor(nil, resolved.KernelType, resolvedVersion)
	if err != nil {
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed",
			Message: fmt.Sprintf(
				"Failed to get spec for '%s' version '%s': %v",
				resolved.KernelType,
				resolvedVersion,
				err,
			),
			Err: err,
		}
	}
	if len(specs) != 1 {
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed",
			Message: fmt.Sprintf(
				"Expected exactly one kernel spec for type='%s' version='%s', got %d",
				resolved.KernelType,
				resolvedVersion,
				len(specs),
			),
			Err: fmt.Errorf("unexpected spec count: %d", len(specs)),
		}
	}
	spec := specs[0]

	// Note: Python enables feature configs via spec.WithEnabledFeatures(resolved.Features),
	// but this method was not ported to Go. Features are resolved at YAML load time in the spec.

	var fetchResult *model.KernelPullResult

	// ── Dispatch based on kernel type (matches Python exactly) ──
	if resolved.KernelType == "firecracker" {

		defaultFirecracker, _ := op.Services.Binary.GetDefaultFirecracker(ctx)
		ciVersion := infra.DefaultFirecrackerCIVersion
		if defaultFirecracker != nil && defaultFirecracker.CIVersion != nil {
			ciVersion = *defaultFirecracker.CIVersion
		}

		emitProgress(onProgress, "download", "running", "Downloading Firecracker kernel...")

		fetchResult, err = op.Services.Kernel.FetchFirecrackerKernel(
			ctx,
			spec,
			ciVersion,
			resolved.Arch,
			resolved.OutputDir,
			operation.DownloadProgressBridge(onProgress),
		)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    "kernel.pull_failed",
				Message: fmt.Sprintf("Firecracker kernel download failed: %v", err),
				Err:     err,
			}
		}

		emitProgress(onProgress, "download", "complete", "Firecracker kernel download complete.")
	} else if resolved.KernelType == "official" {
		emitProgress(onProgress, "build", "running", "Building kernel (this may take a while)...")

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
			return nil, &errs.DomainError{
				Code: "kernel.pull_failed", Message: fmt.Sprintf("Kernel build failed: %v", err), Err: err,
			}
		}

		emitProgress(onProgress, "build", "complete", "Kernel build complete.")
	} else {
		return nil, &errs.DomainError{
			Code:    "kernel.pull_failed",
			Message: fmt.Sprintf("Unsupported kernel type: %s", resolved.KernelType),
			Err:     fmt.Errorf("unsupported kernel type: %s", resolved.KernelType),
		}
	}

	// Generate kernel ID in the API layer (matches Python exactly)
	timestamp := time.Now().Format(time.RFC3339)
	kernelID, err := crypto.KernelID(fetchResult.Path, fetchResult.Version, resolved.Arch, timestamp)
	if err != nil {
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed", Message: fmt.Sprintf("Failed to compute kernel ID: %v", err), Err: err,
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
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed", Message: fmt.Sprintf("Failed to persist kernel: %v", err), Err: err,
		}
	}

	if resolved.SetDefault {
		_ = op.Repos.Kernel.SetDefault(ctx, kernelItem.ID)
	}

	// Clean up old kernel file if ID changed and path is different
	// Python compares old_path.resolve() != new_path before deleting.
	if existing != nil && existing.ID != kernelItem.ID {
		oldPath := existing.Path
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
					resolvedVersion,
				)
			}
		}
	}

	op.AuditLog.LogOperation("kernel.pull", map[string]interface{}{
		"id": kernelItem.ID, "type": kernelItem.Type,
		"version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")

	return kernelItem, nil
}

// KernelImport imports a local vmlinux file as a kernel.
// Matches Python's KernelOperation.import_() exactly — uses KernelImportRequest
// resolution pipeline for input validation and default resolution before
// calling service.import_kernel().
func (op *Operation) KernelImport(ctx context.Context, input *inputs.KernelImportInput) (*model.KernelItem, error) {
	db := op.Connection.DB()

	// Python: request = KernelImportRequest(inputs=inputs, db=db); resolved = request.resolve()
	request := inputs.NewKernelImportRequest(*input, db)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "kernel.import_failed",
			Message: fmt.Sprintf("Kernel import input resolution failed: %v", err),
			Err:     err,
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
		return nil, &errs.DomainError{
			Code: "kernel.import_failed", Message: fmt.Sprintf("Kernel import failed: %v", err), Err: err,
		}
	}

	op.AuditLog.LogOperation("kernel.import", map[string]interface{}{
		"name": kernelItem.Name, "version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")

	return kernelItem, nil
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
		Identifiers: identifiers,
		Force:       &forceVal,
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
	op.Enr.EnrichKernel(ctx, resolved.Kernels, "vm")

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

		op.AuditLog.LogOperation("kernel.remove", map[string]interface{}{
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
	onProgress func(errs.ProgressEvent),
) ([]*model.KernelItem, []model.VersionInfo, error) {
	if remote {
		emitProgress(onProgress, "listing", "running", "Fetching remote kernel versions...")
		versions, err := op.kernelListRemote(ctx, noCache)
		if err != nil {
			return nil, nil, err
		}
		emitProgress(onProgress, "listing", "complete", fmt.Sprintf("Found %d remote kernel(s)", len(versions)))
		return nil, versions, nil
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
	if !noCache {
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
	defaultFC, _ := op.Services.Binary.GetDefaultFirecracker(ctx)
	if defaultFC != nil && defaultFC.CIVersion != nil {
		resolvedCIVersion = *defaultFC.CIVersion
	}

	// Resolve remote_list_limit from settings (matches Python)
	// Python: remote_list_limit = int(SettingsService.resolve(db, "defaults.kernel", "remote_list_limit"))
	remoteListLimit := 0
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

	cacheTTLVal := 0
	if cacheTTL != nil {
		cacheTTLVal = *cacheTTL
	}
	versionMap := op.Services.Kernel.ListRemoteVersions(
		ctx,
		allSpecs,
		system.RuntimeArch(),
		resolvedCIVersion,
		cacheTTLVal,
		remoteListLimit,
	)
	flattened := make([]model.VersionInfo, 0)
	for _, versions := range versionMap {
		flattened = append(flattened, versions...)
	}
	// Mark locally cached kernels
	local, _ := op.Repos.Kernel.ListAll(ctx)
	localSet := make(map[string]bool, len(local))
	for _, l := range local {
		localSet[l.Version] = true
	}
	for i := range flattened {
		if localSet[flattened[i].Version] {
			flattened[i].IsPresent = true
		}
	}
	return flattened, nil
}

// KernelGet returns a single kernel by identifier.
// Matches Python's KernelOperation.get() exactly — uses KernelRequest.resolve()
// internally for consistent resolution behavior.
func (op *Operation) KernelGet(ctx context.Context, id string) (*model.KernelItem, error) {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{id},
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
func (op *Operation) KernelSetDefault(ctx context.Context, id string) error {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{id},
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.DomainError{
			Code: errs.CodeKernelNotFound, Message: fmt.Sprintf("Kernel not found: %s", id),
		}
	}

	if len(resolved.Kernels) != 1 {
		return &errs.DomainError{
			Code: errs.CodeKernelNotFound, Message: fmt.Sprintf("Kernel not found: %s", id),
		}
	}

	kItem := resolved.Kernels[0]

	ctrl, err := kernel.NewController(ctx, kItem, op.Repos.Kernel)
	if err != nil {
		return &errs.DomainError{
			Code: "kernel.default_set_failed", Message: fmt.Sprintf("Failed to create controller: %v", err), Err: err,
		}
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return &errs.DomainError{
			Code: "kernel.default_set_failed", Message: fmt.Sprintf("Failed to set default kernel: %v", err), Err: err,
		}
	}

	op.AuditLog.LogOperation("kernel.set_default", map[string]interface{}{"name": kItem.Name}, "")

	return nil
}

// resolvedVersionStr returns the version from a ResolvedKernelPullRequest,
// handling the nil pointer case.
func resolvedVersionStr(resolved *inputs.ResolvedKernelPullRequest) string {
	if resolved.Version != nil {
		return *resolved.Version
	}
	return ""
}
