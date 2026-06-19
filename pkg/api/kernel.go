// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KernelAPI defines the public interface for kernel operations.
type KernelAPI interface {
	KernelPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	KernelPull(
		ctx context.Context,
		input inputs.KernelPullInput,
		onProgress event.OnProgressCallback,
	) (*model.KernelItem, error)
	KernelImport(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error)
	KernelRemove(ctx context.Context, input inputs.KernelInput) *errs.BatchResult
	KernelList(
		ctx context.Context,
		remote bool,
		noCache bool,
		onProgress event.OnProgressCallback,
	) ([]*model.KernelItem, []model.VersionInfo, error)
	KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error)
	KernelInspect(ctx context.Context, identifier string) (*results.KernelInspect, error)
	KernelSetDefault(ctx context.Context, identifier string) error
}

// KernelPrune prunes unused kernels.
// KernelPrune uses the full KernelRemove pipeline
// (resolution, enrichment, VM reference checks) instead of calling the service directly.
func (op *Operation) KernelPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	allKernels, err := op.Repos.Kernel.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list kernels: %v", err), err)
	}
	defaultItem, _ := op.Repos.Kernel.GetDefault(ctx)
	var defaultID string
	if defaultItem != nil {
		defaultID = defaultItem.ID
	}
	// Get kernel IDs referenced by VMs.
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
			// Call KernelRemove through the full pipeline.
			result := op.KernelRemove(ctx, inputs.KernelInput{Identifiers: []string{kernel.ID}, Force: includeAll})
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
func (op *Operation) KernelPull(ctx context.Context, input inputs.KernelPullInput,
	onProgress event.OnProgressCallback) (*model.KernelItem, error) {
	kernelType := input.KernelType
	// Determine CI version:
	// For firecracker type, the user-specified version IS the CI version.
	// Otherwise, use the default binary's CI version.
	resolvedCIVersion := ""
	ciVersionFromInput := false
	if kernelType == "firecracker" && input.Version != "" {
		v := input.Version
		if !strings.HasPrefix(v, "v") && !strings.HasPrefix(v, "V") {
			v = "v" + v
		}
		resolvedCIVersion = v
		ciVersionFromInput = true
		slog.Debug("Using user-specified version as CI version for firecracker kernel", "ci_version", v)
	}
	if !ciVersionFromInput {
		var err error
		resolvedCIVersion, err = op.resolveCIVersion(ctx)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeKernelPullFailed, err.Error(), err)
		}
	}
	// Phase 1: Resolve version spec to concrete version.
	// Skip when CI version came from user input — kernel version is auto-discovered from S3.
	if !ciVersionFromInput {
		vs, err := version.ParseSpec(input.Version)
		if err == nil && vs.IsPartial() {
			arch := system.RuntimeArch()
			resolvedVersion, err := op.Services.Kernel.ResolveVersion(
				ctx,
				kernelType,
				input.Version,
				arch,
				resolvedCIVersion,
			)
			if err != nil {
				return nil, errs.WrapMsg(
					errs.CodeKernelPullFailed,
					fmt.Sprintf("Failed to resolve version '%s' for '%s': %v", input.Version, kernelType, err),
					err,
				)
			}
			input.Version = resolvedVersion
		}
	} else {
		// Clear the version so it doesn't get used as kernel version
		input.Version = ""
	}
	// Resolve through the Request pipeline
	request := inputs.NewKernelPullRequest(input, op.Services.Config)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeKernelPullFailed,
			fmt.Sprintf("Kernel pull input resolution failed: %v", err),
			err,
		)
	}
	// Look up existing kernel for cleanup later (if rebuild produces different path)
	var existing *model.KernelItem
	if resolved.KernelType == "firecracker" {
		existing, _ = op.Repos.Kernel.GetByType(ctx, resolved.KernelType)
	} else if resolved.KernelType == "official" && resolved.Version != "" {
		existing, _ = op.Repos.Kernel.GetByVersionAndType(ctx, resolved.Version, resolved.KernelType)
	}
	// Resolve spec via KernelService
	specs, err := op.Services.Kernel.GetSpecsFor(nil, resolved.KernelType, resolved.Version)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKernelPullFailed,
			fmt.Sprintf("Failed to get spec for '%s' version '%s': %v", resolved.KernelType, resolved.Version, err),
			err)
	}
	if len(specs) != 1 {
		return nil, errs.WrapMsg(
			errs.CodeKernelPullFailed,
			fmt.Sprintf(
				"Expected exactly one kernel spec for type='%s' version='%s', got %d",
				resolved.KernelType,
				resolved.Version,
				len(specs),
			),
			fmt.Errorf("unexpected spec count: %d", len(specs)),
		)
	}
	spec := specs[0]
	var fetchResult *model.KernelPullResult
	// --- Dispatch based on kernel type ---
	switch resolved.KernelType {
	case "firecracker":
		emitProgress(onProgress, "download", "running", "Downloading Firecracker kernel...")
		fetchResult, err = op.Services.Kernel.FetchFirecrackerKernel(
			ctx,
			spec,
			resolvedCIVersion,
			resolved.Arch,
			resolved.OutputDir,
			event.FormatProgress(onProgress),
		)
		if err != nil {
			return nil, errs.WrapMsg(
				errs.CodeKernelPullFailed,
				fmt.Sprintf("Firecracker kernel download failed: %v", err),
				err,
			)
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
			return nil, errs.WrapMsg(errs.CodeKernelPullFailed, fmt.Sprintf("Kernel build failed: %v", err), err)
		}
		emitProgress(onProgress, "build", "complete", "Kernel build complete.")
	default:
		return nil, errs.WrapMsg(
			errs.CodeKernelPullFailed,
			fmt.Sprintf("Unsupported kernel type: %s", resolved.KernelType),
			fmt.Errorf("unsupported kernel type: %s", resolved.KernelType),
		)
	}
	// Generate kernel ID in the API layer
	timestamp := time.Now().Format(time.RFC3339)
	kernelID, err := crypto.KernelID(fetchResult.Path, fetchResult.Version, resolved.Arch)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKernelPullFailed, fmt.Sprintf("Failed to compute kernel ID: %v", err), err)
	}
	// Parse filename for base_name
	parsed := kernel.ParseFilename(filepath.Base(fetchResult.Path))
	// Create KernelItem
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
		return nil, errs.WrapMsg(errs.CodeKernelPullFailed, fmt.Sprintf("Failed to persist kernel: %v", err), err)
	}
	if resolved.SetDefault {
		_ = op.Repos.Kernel.SetDefault(ctx, kernelItem.ID)
	}
	// Clean up old kernel file if ID changed and path is different
	// Compare resolved paths before deleting old.
	if existing != nil && existing.ID != kernelItem.ID {
		oldPath := existing.Path
		newPath := kernelItem.Path
		// Resolve both paths to compare actual filesystem locations.
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
// uses KernelImportRequest
// resolution pipeline for input validation and default resolution before
// calling KernelService.Import.
func (op *Operation) KernelImport(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error) {
	db := op.Connection.DB()
	request := inputs.NewKernelImportRequest(input, db)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeKernelImportFailed,
			fmt.Sprintf("Kernel import input resolution failed: %v", err),
			err,
		)
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
		return nil, errs.WrapMsg(errs.CodeKernelImportFailed, fmt.Sprintf("Kernel import failed: %v", err), err)
	}
	op.AuditLog.LogOperation("kernel.import", map[string]any{
		"name": kernelItem.Name, "version": kernelItem.Version, "arch": kernelItem.Arch,
	}, "")
	return kernelItem, nil
}

// KernelRemove removes kernels by identifiers.
// uses KernelRequest.Resolve
// to resolve identifiers, then enriches with VM references.
// Each kernel removal has per-kernel error handling.
// The method parameter force is combined with
// resolved.Force.
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
	// Enrich kernels with VM and snapshot references.
	op.Enr.EnrichKernel(ctx, resolved.Kernels, "vm", "snapshots")
	for _, kernel := range resolved.Kernels {
		if !resolved.Force && (len(kernel.VMs) > 0 || len(kernel.Snapshots) > 0) {
			var refs []string
			if len(kernel.VMs) > 0 {
				refs = append(refs, fmt.Sprintf("%d VM(s)", len(kernel.VMs)))
			}
			if len(kernel.Snapshots) > 0 {
				refs = append(refs, fmt.Sprintf("%d snapshot(s)", len(kernel.Snapshots)))
			}
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "kernel.in_use",
				Message:   fmt.Sprintf("Kernel '%s' is in use by %s", kernel.Name, strings.Join(refs, ", ")),
				Exception: fmt.Errorf("kernel in use by %s", strings.Join(refs, ", ")),
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
// uses KernelRequest.Resolve
// internally for consistent resolution behavior.
func (op *Operation) KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error) {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{identifier},
	}
	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("kernel not found %q: %w", identifier, err)
	}
	if len(resolved.Kernels) != 1 {
		return nil, fmt.Errorf("expected exactly one kernel, got %d", len(resolved.Kernels))
	}
	return resolved.Kernels[0], nil
}

// KernelInspect returns detailed kernel information.
func (op *Operation) KernelInspect(ctx context.Context, identifier string) (*results.KernelInspect, error) {
	k, err := op.KernelGet(ctx, identifier)
	if err != nil {
		return nil, err
	}
	return &results.KernelInspect{
		Kernel: results.KernelItemInfo{
			ID: k.ID, Name: k.Name, BaseName: k.BaseName,
			Version: k.Version, Arch: k.Arch, Type: k.Type,
			IsDefault: k.IsDefault, IsPresent: k.IsPresent,
		},
		Storage: results.KernelStorageInfo{Path: k.Path},
		Timestamps: results.KernelTimestampsInfo{
			CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
		},
	}, nil
}

// KernelSetDefault sets a kernel as default.
// uses KernelRequest.Resolve
// for consistent identifier resolution, catches errors at the top level.
func (op *Operation) KernelSetDefault(ctx context.Context, identifier string) error {
	kernelInput := inputs.KernelInput{
		Identifiers: []string{identifier},
	}
	request := inputs.NewKernelRequest(kernelInput, op.Connection.DB(), op.Repos.Kernel)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return errs.NotFound(errs.CodeKernelNotFound, fmt.Sprintf("Kernel not found: %s", identifier))
	}
	if len(resolved.Kernels) != 1 {
		return errs.NotFound(errs.CodeKernelNotFound, fmt.Sprintf("Kernel not found: %s", identifier))
	}
	kItem := resolved.Kernels[0]
	ctrl := kernel.NewController(kItem, op.Repos.Kernel)
	if err := ctrl.SetDefault(ctx); err != nil {
		return errs.WrapMsg(errs.CodeKernelDefaultSetFailed, fmt.Sprintf("Failed to set default kernel: %v", err), err)
	}
	op.AuditLog.LogOperation("kernel.set_default", map[string]any{"name": kItem.Name}, "")
	return nil
}
