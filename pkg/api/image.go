// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/image_operations.py exactly.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"mvmctl/internal/assets"
	"mvmctl/internal/core/image"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// ImagePrune prunes unused images.
// Matches Python's ImageOperation.prune() exactly — queries Repository for
// referenced images instead of using img.VMs field.
func (op *Operation) ImagePrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	allImages, err := op.Repos.Image.ListAll(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeDatabaseError, Message: fmt.Sprintf("Failed to list images: %v", err), Err: err,
		}
	}

	defaultItem, _ := op.Repos.Image.GetDefault(ctx)
	var defaultID string
	if defaultItem != nil {
		defaultID = defaultItem.ID
	}

	// Get referenced image IDs from VMs (matching Python's Repository.list_all() pattern)
	allVMs, _ := op.Repos.VM.ListAll(ctx)
	referencedIDs := make(map[string]bool)
	for _, vm := range allVMs {
		if vm.ImageID != "" {
			referencedIDs[vm.ImageID] = true
		}
	}

	var removed []string
	for _, img := range allImages {
		if !includeAll {
			if img.ID == defaultID {
				continue
			}
			if referencedIDs[img.ID] {
				continue
			}
		}

		if !dryRun {
			// Matches Python: ImageOperation.remove(ImageInput(id=[image.id]), force=include_all)
			// Uses the full remove pipeline (BatchResult, VM reference check, etc.)
			result := op.ImageRemove(ctx, inputs.ImageInput{Identifiers: []string{img.ID}}, includeAll)
			if result.HasErrors() {
				for _, r := range result.Errors() {
					slog.Warn("Failed to remove image", "id", img.ID, "error", r.Message)
				}
				continue
			}
		}
		removed = append(removed, img.ID)
	}

	return removed, nil
}

// ImagePull downloads an image with full orchestration.
// Matches Python's ImageOperation.pull() exactly.
func (op *Operation) ImagePull(
	ctx context.Context,
	input inputs.ImagePullInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	// Resolve pull input via ImageAcquireRequest (arch, output dir, validation)
	req := inputs.NewImageAcquireRequest(input, op.Services.Config, op.Repos.Image)
	resolved, err := req.ResolvePull(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImagePullFailed, Message: fmt.Sprintf("Failed to resolve pull input: %v", err), Err: err,
		}
	}

	// Resolve cache TTL from settings
	cacheTTL := 0
	if !resolved.NoCache {
		cacheTTL, _ = op.Services.Config.GetInt(ctx, "defaults.image", "remote_list_cache_ttl")
	}

	// Resolve ci_version from default firecracker binary
	resolvedCIVersion, err := op.resolveCIVersion(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImagePullFailed, Message: err.Error(), Err: err,
		}
	}

	// Load image types config from embedded assets
	rawYAML, err := assets.ReadFile("images.yaml")
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImagePullFailed, Message: fmt.Sprintf("Failed to load images.yaml: %v", err), Err: err,
		}
	}
	imageTypesConfig, err := image.LoadImageTypesConfig(rawYAML)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Message: fmt.Sprintf("Failed to parse image types config: %v", err),
			Err:     err,
		}
	}

	// Resolve version spec (latest, partial, or exact) to concrete version
	resolvedVersion, err := op.Services.Image.ResolveVersion(ctx, resolved.Type, resolved.Version, resolved.Arch, resolvedCIVersion, imageTypesConfig)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Message: fmt.Sprintf("Failed to resolve image version: %v", err),
			Err:     err,
		}
	}

	specs, err := image.GetSpecsFor(
		ctx,
		[]string{resolved.Type},
		resolvedVersion,
		resolved.Arch,
		cacheTTL,
		resolvedCIVersion,
		imageTypesConfig,
	)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Message: fmt.Sprintf("Failed to resolve spec for %s: %v", resolved.Type, err),
			Err:     err,
		}
	}
	if len(specs) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Message: fmt.Sprintf("No matching image spec for type=%q version=%q", resolved.Type, resolved.Version),
		}
	}
	spec := specs[0]

	// Early return check
	existing, _ := op.Repos.Image.GetByType(ctx, spec.Type)
	if !input.Force && existing != nil && existing.Version == spec.Version {
		if existing.Path != "" {
			if _, err := os.Stat(existing.Path); err == nil {
				slog.Info("Image already exists", "path", existing.Path)
				if input.SetDefault {
					_ = op.Repos.Image.SetDefault(ctx, existing.ID)
				}
				return existing, nil
			}
		}
	}

	timestamp := time.Now().Format(time.RFC3339)
	imageID := crypto.ImageID(fmt.Sprintf("%s:%s", spec.Type, spec.Version), spec.Source, timestamp)

	workDir, err := os.MkdirTemp(infra.GetTempDir(), "mvm-pull-*")
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImagePullFailed, Message: fmt.Sprintf("Failed to create temp dir: %v", err), Err: err,
		}
	}
	defer os.RemoveAll(workDir)

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "download", Status: "running", Message: "Downloading image...",
		})
	}
	progressBridge := event.FormatProgress(onProgress)
	downloadPath, err := op.Services.Image.DownloadImage(
		ctx,
		spec,
		imageID,
		workDir,
		input.Force,
		resolvedCIVersion,
		progressBridge,
	)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImagePullFailed, Message: fmt.Sprintf("Download failed: %v", err), Err: err,
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "extract", Status: "running", Message: "Extracting image...",
		})
	}

	extractedPath, err := op.Services.Image.ExtractImage(
		ctx,
		downloadPath,
		imageID,
		workDir,
		spec.Format,
		input.Partition,
		input.DisabledDetectors,
		op.ProvisionerType,
	)
	if err != nil {
		// Catch RootPartitionDetectionError and TieDetectedError (matching Python)
		if isPartitionDetectionError(err) {
			return nil, &errs.DomainError{
				Code: "image.acquire_failed", Message: err.Error(), Err: err,
			}
		}
		return nil, &errs.DomainError{
			Code: errs.CodeImageCorrupt, Message: fmt.Sprintf("Extraction failed: %v", err), Err: err,
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "optimize", Status: "running", Message: "Optimizing image...",
		})
	}

	imageItem, _, err := op.Services.Image.OptimizeImage(
		ctx,
		extractedPath,
		imageID,
		spec,
		timestamp,
		input.SkipOptimization,
		op.ProvisionerType,
		nil,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return nil, &errs.DomainError{
				Code: "image.acquire_failed", Message: err.Error(), Err: err,
			}
		}
		return nil, &errs.DomainError{
			Code: errs.CodeImageCorrupt, Message: fmt.Sprintf("Optimization failed: %v", err), Err: err,
		}
	}

	// Move compressed result to images dir
	if imageItem.Path != "" {
		src := imageItem.Path
		dst := filepath.Join(resolved.OutputDir, filepath.Base(src))
		os.MkdirAll(resolved.OutputDir, 0755)
		if dst != src {
			os.Remove(dst)
			if err := os.Rename(src, dst); err != nil {
				srcFile, openErr := os.Open(src)
				if openErr == nil {
					dstFile, createErr := os.Create(dst)
					if createErr == nil {
						_, copyErr := io.Copy(dstFile, srcFile)
						dstFile.Close()
						srcFile.Close()
						if copyErr == nil {
							os.Remove(src)
						}
					} else {
						srcFile.Close()
					}
				}
			}
			imageItem.Path = dst
		}
	}

	os.Remove(downloadPath)

	_ = op.Repos.Image.Upsert(ctx, imageItem)
	if input.SetDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	} else if existing != nil && existing.IsDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	}

	// Clean up old images
	if existing != nil && existing.ID != imageItem.ID {
		removed := op.Services.Image.RemoveImageFiles(existing)
		_ = op.Repos.Image.SoftDelete(ctx, existing.ID)
		if len(removed) > 0 {
			slog.Info("Cleaned up old image files", "count", len(removed), "type", spec.Type)
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "complete", Status: "complete", Message: "Image pull complete.",
		})
	}

	return imageItem, nil
}

// ImageImport imports a local image file.
// Matches Python's ImageOperation.import_() exactly.
func (op *Operation) ImageImport(
	ctx context.Context,
	input inputs.ImageImportInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	// Resolve import input via ImageAcquireRequest (arch, format, validation)
	req := inputs.NewImageAcquireRequest(input, op.Services.Config, op.Repos.Image)
	resolved, err := req.ResolveImport(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImageImportFailed, Message: fmt.Sprintf("Failed to resolve import input: %v", err), Err: err,
		}
	}

	existing, _ := op.Repos.Image.GetByType(ctx, resolved.Type)
	if !resolved.Force && existing != nil && existing.Path != "" {
		if _, err := os.Stat(existing.Path); err == nil {
			slog.Info("Image already exists", "path", existing.Path)
			if resolved.SetDefault {
				_ = op.Repos.Image.SetDefault(ctx, existing.ID)
			}
			return existing, nil
		}
	}

	// Format detection warning
	var importWarnings []string
	if resolved.FormatDetected != "" && resolved.Format != resolved.FormatDetected {
		importWarnings = append(
			importWarnings,
			fmt.Sprintf(
				"Declared format '%s' does not match detected format '%s'",
				resolved.Format,
				resolved.FormatDetected,
			),
		)
	}

	spec := &model.ImageSpec{
		Type:    resolved.Type,
		Version: "",
		Name:    resolved.Type,
		Arch:    resolved.Arch,
		Source:  *resolved.SourcePath,
		Format:  resolved.Format,
	}

	timestamp := time.Now().Format(time.RFC3339)
	imageID := crypto.ImageID(fmt.Sprintf("%s:%s", spec.Type, spec.Version), spec.Source, timestamp)

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "extract", Status: "running", Message: "Extracting image...",
		})
	}

	extractedPath, err := op.Services.Image.ExtractImage(
		ctx,
		*resolved.SourcePath,
		imageID,
		infra.GetImagesDir(),
		resolved.Format,
		resolved.Partition,
		resolved.DisabledDetectors,
		op.ProvisionerType,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return nil, &errs.DomainError{
				Code: "image.import_failed", Message: err.Error(), Err: err,
			}
		}
		return nil, &errs.DomainError{
			Code: errs.CodeImageImportFailed, Message: fmt.Sprintf("Extraction failed: %v", err), Err: err,
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "optimize", Status: "running", Message: "Optimizing image...",
		})
	}

	imageItem, _, err := op.Services.Image.OptimizeImage(
		ctx,
		extractedPath,
		imageID,
		spec,
		timestamp,
		input.SkipOptimization,
		op.ProvisionerType,
		importWarnings,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return nil, &errs.DomainError{
				Code: "image.import_failed", Message: err.Error(), Err: err,
			}
		}
		return nil, &errs.DomainError{
			Code: errs.CodeImageImportFailed, Message: fmt.Sprintf("Optimization failed: %v", err), Err: err,
		}
	}

	_ = op.Repos.Image.Upsert(ctx, imageItem)
	if input.SetDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	} else if existing != nil && existing.IsDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	}

	if existing != nil && existing.ID != imageItem.ID {
		removed := op.Services.Image.RemoveImageFiles(existing)
		_ = op.Repos.Image.SoftDelete(ctx, existing.ID)
		if len(removed) > 0 {
			slog.Info("Cleaned up old image files", "count", len(removed), "id", imageItem.ID)
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "complete", Status: "complete", Message: "Image import complete.",
		})
	}

	return imageItem, nil
}

// ImageWarm pre-decompresses images to ready pool for fast VM creation.
// Matches Python's ImageOperation.warm() exactly.
// Python: input can be None when all=True — Go handles with nil check.
func (op *Operation) ImageWarm(
	ctx context.Context,
	input inputs.ImageInput,
	all bool,
	onProgress event.OnProgressCallback,
) ([]string, error) {
	var images []*model.ImageItem

	if all {
		var err error
		images, err = op.Repos.Image.ListAll(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code: errs.CodeDatabaseError, Message: fmt.Sprintf("Failed to list images: %v", err), Err: err,
			}
		}
	} else {
		request := inputs.NewImageRequest(input, op.Connection.DB(), op.Repos.Image)
		resolved, err := request.Resolve(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code: errs.CodeImageNotFound, Message: fmt.Sprintf("Image resolution failed: %v", err), Err: err,
			}
		}
		images = resolved.Images
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "warm", Status: "running", Message: "Warming images...",
		})
	}

	warmed, err := op.Services.Image.EnsureCached(images)
	if err != nil {
		return nil, &errs.DomainError{
			Code: "image.warm_failed", Message: fmt.Sprintf("Warming failed: %v", err), Err: err,
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "warm", Status: "complete", Message: "Warming complete.",
		})
	}

	for _, path := range warmed {
		slog.Info("Image warmed", "path", path)
	}

	return warmed, nil
}

// ImageRemove removes images by input.
// Matches Python's ImageOperation.remove() exactly.
func (op *Operation) ImageRemove(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult {
	results := make([]errs.OperationResult, 0)

	request := inputs.NewImageRequest(input, op.Connection.DB(), op.Repos.Image)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:    "error",
					Code:      string(errs.CodeImageNotFound),
					Message:   fmt.Sprintf("Resolution failed: %v", err),
					Exception: err,
				},
			},
		}
	}

	images := resolved.Images

	// Batch-enrich with VM references (matches Python's Resolver(repo, include=["vm"]).enrich())
	op.Enr.EnrichImage(ctx, images, "vm")

	for _, img := range images {
		if !force && len(img.VMs) > 0 {
			results = append(results, errs.OperationResult{
				Status:    "error",
				Code:      "image.in_use",
				Message:   fmt.Sprintf("Image '%s' is in use by %d VM(s)", img.ID, len(img.VMs)),
				Exception: fmt.Errorf("image in use by %d VMs", len(img.VMs)),
			})
			continue
		}

		if err := op.Services.Image.RemoveImage(ctx, img, force); err != nil {
			results = append(results, errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeImageNotFound),
				Message:   fmt.Sprintf("Failed to remove image: %v", err),
				Exception: err,
			})
			continue
		}

		// Audit log AFTER successful removal
		op.AuditLog.LogOperation("image.remove", map[string]any{"id": img.ID}, "")

		results = append(results, errs.OperationResult{
			Status: "success",
			Code:   "image.removed",
			Item:   img,
		})
	}

	return &errs.BatchResult{Items: results}
}

// ImageListAll returns images.
// Matches Python's ImageOperation.list_all() exactly.
// When remote=false, returns ([]*model.ImageItem, nil, error).
// When remote=true, returns (nil, []model.VersionInfo, error).
// When type_filter is set and remote=true, only returns versions for that specific image type.
// no_cache bypasses cached version listings when remote=true.
func (op *Operation) ImageListAll(
	ctx context.Context,
	remote bool,
	typeFilter string,
	noCache bool,
	onProgress event.OnProgressCallback,
) ([]*model.ImageItem, []model.VersionInfo, error) {
	if remote {
		emitProgress(onProgress, "listing", "running", "Fetching remote images...")

		// Resolve ci_version from default firecracker binary
		resolvedCIVersion, err := op.resolveCIVersion(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Resolve cache_ttl from settings
		cacheTTL := 0
		if !noCache {
			cacheTTL, _ = op.Services.Config.GetInt(ctx, "defaults.image", "remote_list_cache_ttl")
		}

		// Arch always matches the host machine — not user-configurable
		arch := system.RuntimeArch()

		// Load image types config from embedded assets
		rawYAML, err := assets.ReadFile("images.yaml")
		if err != nil {
			return nil, nil, fmt.Errorf("load images.yaml: %w", err)
		}
		imageTypesConfig, err := image.LoadImageTypesConfig(rawYAML)
		if err != nil {
			return nil, nil, fmt.Errorf("parse image types config: %w", err)
		}

		// Filter by type BEFORE resolving (matches Python's type_filter handling)
		if typeFilter != "" {
			filtered := make([]download.ResolverConfig, 0)
			for _, cfg := range imageTypesConfig {
				if cfg.Type == typeFilter {
					filtered = append(filtered, cfg)
				}
			}
			imageTypesConfig = filtered
			if len(imageTypesConfig) == 0 {
				return nil, []model.VersionInfo{}, nil
			}
		}

		// Use ResolveVersions to get VersionInfo objects
		versionMap := image.ResolveVersions(ctx, imageTypesConfig, arch, cacheTTL, resolvedCIVersion)
		var versions []model.VersionInfo
		for _, vs := range versionMap {
			versions = append(versions, vs...)
		}

		// Mark locally cached images
		local, _ := op.Repos.Image.ListAll(ctx)
		localSet := make(map[string]bool, len(local))
		for _, l := range local {
			localSet[l.Type+":"+l.Version] = true
		}
		for i := range versions {
			if localSet[versions[i].Type+":"+versions[i].Version] {
				versions[i].IsPresent = true
			}
		}

		emitProgress(onProgress, "listing", "complete", fmt.Sprintf("Found %d remote image(s)", len(versions)))
		return nil, versions, nil
	}

	// Local images from DB
	items, err := op.Services.Image.ListAll(ctx, true, false)
	return items, nil, err
}

// ImageGet returns a single image by ID prefix or type.
// Matches Python's ImageOperation.get() exactly — uses ImageRequest for resolution.
func (op *Operation) ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error) {
	request := inputs.NewImageRequest(input, op.Connection.DB(), op.Repos.Image)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if len(resolved.Images) > 1 {
		return nil, fmt.Errorf("expected exactly one image identifier")
	}
	return resolved.Images[0], nil
}

// ImageInspect returns grouped dict of an image.
// Matches Python's ImageOperation.inspect() exactly.
func (op *Operation) ImageInspect(ctx context.Context, input inputs.ImageInput) (*responses.ImageInspect, error) {
	img, err := op.ImageGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return &responses.ImageInspect{
		Image: responses.ImageItemInfo{
			ID: img.ID, Name: img.Name, Type: img.Type,
			Arch: img.Arch, IsDefault: img.IsDefault, IsPresent: img.IsPresent,
		},
		Storage: responses.ImageStorageInfo{
			Path: img.Path, FSType: img.FSType, FSUUID: img.FSUUID,
			CompressedSize: img.CompressedSize, OriginalSize: img.OriginalSize,
		},
		Compression: responses.ImageCompressionInfo{
			Format: img.CompressedFormat, Ratio: img.CompressionRatio,
		},
		Requirements: responses.ImageRequirementsInfo{
			MinRootfsSizeMiB: img.MinRootfsSizeMiB,
		},
		Timestamps: responses.ImageTimestampsInfo{
			PulledAt: img.PulledAt, CreatedAt: img.CreatedAt, UpdatedAt: img.UpdatedAt,
		},
	}, nil
}

// ImageSetDefault sets an image as default.
// Matches Python's ImageOperation.set_default() exactly — uses ImageRequest for resolution.
func (op *Operation) ImageSetDefault(ctx context.Context, input inputs.ImageInput) error {
	request := inputs.NewImageRequest(input, op.Connection.DB(), op.Repos.Image)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.DomainError{
			Code: errs.CodeImageNotFound, Message: fmt.Sprintf("Image not found: %v", err), Err: err,
		}
	}
	if len(resolved.Images) > 1 {
		return &errs.DomainError{
			Code: errs.CodeImageNotFound, Message: "Expected exactly one image identifier",
		}
	}
	img := resolved.Images[0]
	if err := op.Repos.Image.SetDefault(ctx, img.ID); err != nil {
		return &errs.DomainError{
			Code: errs.CodeImageNotFound, Message: fmt.Sprintf("Failed to set default: %v", err), Err: err,
		}
	}

	op.AuditLog.LogOperation("image.set_default", map[string]any{"id": img.ID}, "")
	return nil
}

// isPartitionDetectionError checks if an error is a RootPartitionDetectionError
// or TieDetectedError (matching Python's exception catching pattern).
func isPartitionDetectionError(err error) bool {
	var de *errs.DomainError
	if !errors.As(err, &de) {
		return false
	}
	return de.Code == errs.CodeRootPartitionDetection || de.Code == errs.CodeTieDetected
}
