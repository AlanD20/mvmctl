// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/kernel_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
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
			result := op.KernelRemove(ctx, inputs.KernelInput{Identifiers: []string{kernel.ID}, Force: &includeAll})
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
func (op *Operation) KernelPull(ctx context.Context, input inputs.KernelPullInput,
	onProgress event.OnProgressCallback) (*model.KernelItem, error) {

	kernelType := input.KernelType

	// Phase 1: Resolve version spec to concrete version.
	vs, err := version.ParseSpec(input.Version)
	if err == nil && vs.IsPartial() {
		ciVersion, err := op.resolveCIVersion(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code: "kernel.pull_failed", Message: err.Error(), Err: err,
			}
		}
		arch := system.RuntimeArch()
		resolvedVersion, err := op.Services.Kernel.ResolveVersion(ctx, kernelType, input.Version, arch, ciVersion)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    "kernel.pull_failed",
				Message: fmt.Sprintf("Failed to resolve version '%s' for '%s': %v", input.Version, kernelType, err),
				Err:     err,
			}
		}
		input.Version = resolvedVersion
	}

	// Resolve through the Request pipeline (matches Python)
	request := inputs.NewKernelPullRequest(input, op.Services.Config)
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
	} else if resolved.KernelType == "official" && resolved.Version != "" {
		existing, _ = op.Repos.Kernel.GetByVersionAndType(ctx, resolved.Version, resolved.KernelType)
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
	specs, err := op.Services.Kernel.GetSpecsFor(nil, resolved.KernelType, resolved.Version)
	if err != nil {
		return nil, &errs.DomainError{
			Code: "kernel.pull_failed",
			Message: fmt.Sprintf(
				"Failed to get spec for '%s' version '%s': %v",
				resolved.KernelType,
				resolved.Version,
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
				resolved.Version,
				len(specs),
			),
			Err: fmt.Errorf("unexpected spec count: %d", len(specs)),
		}
	}
	spec := specs[0]

	var fetchResult *model.KernelPullResult

	// ── Dispatch based on kernel type (matches Python exactly) ──
	switch resolved.KernelType {
	case "firecracker":

		ciVersion, err := op.resolveCIVersion(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code: "kernel.pull_failed", Message: err.Error(), Err: err,
			}
		}

		emitProgress(onProgress, "download", "running", "Downloading Firecracker kernel...")

		fetchResult, err = op.Services.Kernel.FetchFirecrackerKernel(
			ctx,
			spec,
			ciVersion,
			resolved.Arch,
			resolved.OutputDir,
			event.FormatProgress(onProgress),
		)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    "kernel.pull_failed",
				Message: fmt.Sprintf("Firecracker kernel download failed: %v", err),
				Err:     err,
			}
		}

		emitProgress(onProgress, "download", "complete", "Firecracker kernel download complete.")
	case "official":
		emitProgress(onProgress, "build", "running", "Building kernel (this may take a while)...")

		var configPath *string
		if resolved.KernelConfig != nil && *resolved.KernelConfig != "" {
			configPath = resolved.KernelConfig
		}

		// Merge feature enforces from selected features.
		featureEnforces := make(map[string]string, len(resolved.Features))
		for _, name := range resolved.Features {
			if f, ok := spec.Features[name]; ok {
				maps.Copy(featureEnforces, f.Enforce)
			}
		}

		fetchResult, err = op.Services.Kernel.BuildOfficialKernel(ctx, spec, resolved.Arch, resolved.OutputDir,
			resolved.Jobs, resolved.KeepBuildDir, !resolved.CleanBuild,
			configPath, featureEnforces,
			event.FormatProgress(onProgress), onProgress)
		if err != nil {
			return nil, &errs.DomainError{
				Code: "kernel.pull_failed", Message: fmt.Sprintf("Kernel build failed: %v", err), Err: err,
			}
		}

		emitProgress(onProgress, "build", "complete", "Kernel build complete.")
	default:
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
					resolved.Version,
				)
			}
		}
	}

	op.AuditLog.LogOperation("kernel.pull", map[string]any{
		"id": kernelItem.ID, "type": kernelItem.Type,
		"version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")

	return kernelItem, nil
}

// KernelImport imports a local vmlinux file as a kernel.
// Matches Python's KernelOperation.import_() exactly — uses KernelImportRequest
// resolution pipeline for input validation and default resolution before
// calling service.import_kernel().
func (op *Operation) KernelImport(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error) {
	db := op.Connection.DB()

	// Python: request = KernelImportRequest(inputs=inputs, db=db); resolved = request.resolve()
	request := inputs.NewKernelImportRequest(input, db)
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

	op.AuditLog.LogOperation("kernel.import", map[string]any{
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
func (op *Operation) KernelRemove(ctx context.Context, input inputs.KernelInput) *errs.BatchResult {
	request := inputs.NewKernelRequest(input, op.Connection.DB(), op.Repos.Kernel)
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

		if !resolved.Force && len(kernel.VMs) > 0 {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "kernel.in_use",
				Message:   fmt.Sprintf("Kernel '%s' is in use by %d VM(s)", kernel.Name, len(kernel.VMs)),
				Exception: fmt.Errorf("kernel in use by %d VMs", len(kernel.VMs)),
			})
			continue
		}

		if _, err := op.Services.Kernel.Remove(ctx, kernel, resolved.Force); err != nil {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "kernel.remove_failed",
				Message:   fmt.Sprintf("Failed to remove kernel %s: %v", kernel.Name, err),
				Exception: err,
				Item:      kernel,
			})
			continue
		}

		op.AuditLog.LogOperation("kernel.remove", map[string]any{
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
	onProgress event.OnProgressCallback,
) ([]*model.KernelItem, []model.VersionInfo, error) {
	if remote {
		emitProgress(onProgress, "listing", "running", "Fetching remote kernel versions...")

		specs, err := op.Services.Kernel.LoadSpecs()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load kernel specs: %w", err)
		}
		allSpecs := make([]*model.KernelSpec, 0, len(specs))
		for _, spec := range specs {
			allSpecs = append(allSpecs, spec)
		}

		cacheTTL := 0
		if !noCache {
			cacheTTL, _ = op.Services.Config.GetInt(ctx, "defaults.kernel", "remote_list_cache_ttl")
		}

		resolvedCIVersion, err := op.resolveCIVersion(ctx)
		if err != nil {
			return nil, nil, err
		}

		remoteListLimit, _ := op.Services.Config.GetInt(ctx, "defaults.kernel", "remote_list_limit")
		versionMap := op.Services.Kernel.ListRemoteVersions(
			ctx,
			allSpecs,
			system.RuntimeArch(),
			resolvedCIVersion,
			cacheTTL,
			remoteListLimit,
		)
		flattened := make([]model.VersionInfo, 0)
		for _, versions := range versionMap {
			flattened = append(flattened, versions...)
		}

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

		emitProgress(onProgress, "listing", "complete", fmt.Sprintf("Found %d remote kernel(s)", len(flattened)))
		return nil, flattened, nil
	}
	items, err := op.Services.Kernel.List(ctx)
	return items, nil, err
}

// KernelGet returns a single kernel by identifier.
// Matches Python's KernelOperation.get() exactly — uses KernelRequest.resolve()
// internally for consistent resolution behavior.
func (op *Operation) KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error) {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{identifier},
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("kernel not found: %s", identifier)
	}

	if len(resolved.Kernels) != 1 {
		// Python: raise KernelError(f"Expected exactly one kernel, got {len(resolved.kernels)}")
		return nil, fmt.Errorf("expected exactly one kernel, got %d", len(resolved.Kernels))
	}

	return resolved.Kernels[0], nil
}

// KernelInspect returns grouped dict of a kernel.
// Matches Python's KernelOperation.inspect() exactly.
func (op *Operation) KernelInspect(ctx context.Context, identifier string) (*responses.KernelInspect, error) {
	k, err := op.KernelGet(ctx, identifier)
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
func (op *Operation) KernelSetDefault(ctx context.Context, identifier string) error {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{identifier},
	}

	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.DomainError{
			Code: errs.CodeKernelNotFound, Message: fmt.Sprintf("Kernel not found: %s", identifier),
		}
	}

	if len(resolved.Kernels) != 1 {
		return &errs.DomainError{
			Code: errs.CodeKernelNotFound, Message: fmt.Sprintf("Kernel not found: %s", identifier),
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

	op.AuditLog.LogOperation("kernel.set_default", map[string]any{"name": kItem.Name}, "")

	return nil
}
