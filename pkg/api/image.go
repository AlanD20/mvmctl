// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/image_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mvmctl/internal/assets"
	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/operation"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// ImagePrune prunes unused images.
// Matches Python's ImageOperation.prune() exactly — queries Repository for
// referenced images instead of using img.VMs field.
func (op *Operation) ImagePrune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	allImages, err := op.Repos.Image.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list images: %v", err),
			Exception: err,
		}
	}

	defaultItem, _ := op.Repos.Image.GetDefault(ctx)
	var defaultID string
	if defaultItem != nil {
		defaultID = defaultItem.ID
	}

	// Get referenced image IDs from VMs (matching Python's Repository.list_all() pattern)
	vmRepo := vm.NewRepository(op.Connection.DB())
	allVMs, _ := vmRepo.ListAll(ctx)
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
			result := op.ImageRemove(ctx, &inputs.ImageInput{Identifiers: []string{img.ID}}, includeAll)
			if result.HasErrors() {
				for _, r := range result.Errors() {
					slog.Warn("Failed to remove image", "id", img.ID, "error", r.Message)
				}
				continue
			}
		}
		removed = append(removed, img.ID)
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: fmt.Sprintf("Pruned %d image(s)", len(removed)),
		Item:    removed,
	}
}

// ImagePull downloads an image with full orchestration.
// Matches Python's ImageOperation.pull() exactly.
// Matches Python's return type: OperationResult[ImageItem] | NeedsInteraction
// Returns *errs.OperationResult with Item of type *model.ImageItem (success)
// or *errs.NeedsInteraction (when user confirmation required).
func (op *Operation) ImagePull(
	ctx context.Context,
	input *inputs.ImagePullInput,
	onProgress func(errs.ProgressEvent),
) interface{} {
	var version string
	if input.Version != nil {
		version = *input.Version
	}
	arch := ""
	if input.Arch != nil {
		arch = *input.Arch
	}
	if arch == "" {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImagePullFailed),
			Message: "arch is required",
		}
	}

	// Use custom output dir if specified, otherwise the default images dir
	imagesDir := filepath.Join(op.CacheDir, "images")
	if input.OutputDir != "" {
		imagesDir = input.OutputDir
	}
	if imagesDir == "" {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImagePullFailed),
			Message: "Failed to resolve output_dir",
		}
	}

	// Resolve cache TTL and ci_version from settings/binary (matches Python)
	cacheTTL := 0
	if !input.NoCache {
		if op.Services.Config != nil {
			if ttlRaw, err := op.Services.Config.Get(ctx, "defaults.image", "remote_list_cache_ttl"); err == nil {
				if ttl, ok := ttlRaw.(int); ok {
					cacheTTL = ttl
				}
			}
		}
	}
	resolvedCIVersion := ""

	// Resolve ci_version from default firecracker binary (matches Python)
	binRepo := binary.NewRepository(op.Connection.DB())
	defaultBin, _ := binRepo.GetDefault(ctx, "firecracker")
	if defaultBin != nil && defaultBin.CIVersion != nil {
		resolvedCIVersion = *defaultBin.CIVersion
	}

	// Load image types config from embedded assets
	rawYAML, err := assets.ReadFile("images.yaml")
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImagePullFailed),
			Message:   fmt.Sprintf("Failed to load images.yaml: %v", err),
			Exception: err,
		}
	}
	imageTypesConfig, err := image.LoadImageTypesConfig(rawYAML)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImagePullFailed),
			Message:   fmt.Sprintf("Failed to parse image types config: %v", err),
			Exception: err,
		}
	}

	specs, err := image.GetSpecsFor(
		ctx,
		[]string{input.Type},
		version,
		arch,
		cacheTTL,
		resolvedCIVersion,
		imageTypesConfig,
	)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImagePullFailed),
			Message:   fmt.Sprintf("Failed to resolve spec for %s: %v", input.Type, err),
			Exception: err,
		}
	}
	if len(specs) == 0 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImagePullFailed),
			Message: fmt.Sprintf("No matching image spec for type=%q version=%q", input.Type, version),
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
				return &errs.OperationResult{
					Status: "skipped",
					Code:   "image.already_present",
					Item:   existing,
				}
			}
		}
	}

	timestamp := time.Now().Format(time.RFC3339)
	var hg infra.HashGenerator
	imageID := hg.Image(fmt.Sprintf("%s:%s", spec.Type, spec.Version), spec.Source, timestamp)

	workDir, err := os.MkdirTemp("", "mvm-pull-*")
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImagePullFailed),
			Message:   fmt.Sprintf("Failed to create temp dir: %v", err),
			Exception: err,
		}
	}
	defer os.RemoveAll(workDir)

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "download", Status: "running", Message: "Downloading image...",
		})
	}
	progressBridge := operation.DownloadProgressBridge(onProgress)
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
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImagePullFailed),
			Message:   fmt.Sprintf("Download failed: %v", err),
			Exception: err,
		}
	}

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "extract", Status: "running", Message: "Extracting image...",
		})
	}

	provisionerType := op.resolveProvisionerType(ctx)
	extractedPath, err := op.Services.Image.ExtractImage(
		ctx,
		downloadPath,
		imageID,
		workDir,
		spec.Format,
		input.Partition,
		input.DisabledDetectors,
		provisionerType,
	)
	if err != nil {
		// Catch RootPartitionDetectionError and TieDetectedError (matching Python)
		if isPartitionDetectionError(err) {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "image.acquire_failed",
				Message:   err.Error(),
				Exception: err,
			}
		}
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageCorrupt),
			Message:   fmt.Sprintf("Extraction failed: %v", err),
			Exception: err,
		}
	}

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "optimize", Status: "running", Message: "Optimizing image...",
		})
	}

	imageItem, warnings, err := op.Services.Image.OptimizeImage(
		ctx,
		extractedPath,
		imageID,
		spec,
		timestamp,
		input.SkipOptimization,
		provisionerType,
		nil,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "image.acquire_failed",
				Message:   err.Error(),
				Exception: err,
			}
		}
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageCorrupt),
			Message:   fmt.Sprintf("Optimization failed: %v", err),
			Exception: err,
		}
	}

	// Move compressed result to images dir
	if imageItem.Path != "" {
		src := imageItem.Path
		dst := filepath.Join(imagesDir, filepath.Base(src))
		os.MkdirAll(imagesDir, 0755)
		if dst != src {
			os.Remove(dst)
			if err := os.Rename(src, dst); err != nil {
				data, readErr := os.ReadFile(src)
				if readErr == nil {
					os.WriteFile(dst, data, 0644)
					os.Remove(src)
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
		onProgress(errs.ProgressEvent{
			Phase: "complete", Status: "complete", Message: "Image pull complete.",
		})
	}

	msg := "Image pulled successfully"
	if len(warnings) > 0 {
		msg += fmt.Sprintf(" (%s)", strings.Join(warnings, "; "))
	}

	return &errs.OperationResult{
		Status:   "success",
		Code:     "image.acquired",
		Item:     imageItem,
		Message:  msg,
		Warnings: warnings,
	}
}

// ImageImport imports a local image file.
// Matches Python's ImageOperation.import_() exactly.
func (op *Operation) ImageImport(
	ctx context.Context,
	input *inputs.ImageImportInput,
	onProgress func(errs.ProgressEvent),
) *errs.OperationResult {
	arch := ""
	if input.Arch != nil {
		arch = *input.Arch
	}
	if arch == "" {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImageImportFailed),
			Message: "arch is required",
		}
	}

	format := ""
	if input.Format != nil {
		format = *input.Format
	}
	if format == "" {
		format = image.DetectImageFormat(input.SourcePath)
		if format == "" {
			return &errs.OperationResult{
				Status:  "error",
				Code:    string(errs.CodeImageFormatInvalid),
				Message: fmt.Sprintf("Cannot detect format for %s", input.SourcePath),
			}
		}
	}

	var importWarnings []string
	detected := image.DetectImageFormat(input.SourcePath)
	if detected != "" && detected != format {
		importWarnings = append(importWarnings,
			fmt.Sprintf("Declared format '%s' does not match detected format '%s'", format, detected))
	}

	existing, _ := op.Repos.Image.GetByType(ctx, input.Name)
	if !input.Force && existing != nil && existing.Path != "" {
		if _, err := os.Stat(existing.Path); err == nil {
			slog.Info("Image already exists", "path", existing.Path)
			if input.SetDefault {
				_ = op.Repos.Image.SetDefault(ctx, existing.ID)
			}
			return &errs.OperationResult{
				Status: "skipped",
				Code:   "image.already_present",
				Item:   existing,
			}
		}
	}

	spec := &model.ImageSpec{
		Type:    input.Name,
		Version: "",
		Name:    input.Name,
		Arch:    arch,
		Source:  input.SourcePath,
		Format:  format,
	}

	timestamp := time.Now().Format(time.RFC3339)
	var hg infra.HashGenerator
	imageID := hg.Image(fmt.Sprintf("%s:%s", spec.Type, spec.Version), spec.Source, timestamp)

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "extract", Status: "running", Message: "Extracting image...",
		})
	}

	provisionerType := op.resolveProvisionerType(ctx)
	extractedPath, err := op.Services.Image.ExtractImage(
		ctx,
		input.SourcePath,
		imageID,
		filepath.Join(op.CacheDir, "images"),
		format,
		input.Partition,
		input.DisabledDetectors,
		provisionerType,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "image.import_failed",
				Message:   err.Error(),
				Exception: err,
			}
		}
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageImportFailed),
			Message:   fmt.Sprintf("Extraction failed: %v", err),
			Exception: err,
		}
	}

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
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
		provisionerType,
		importWarnings,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "image.import_failed",
				Message:   err.Error(),
				Exception: err,
			}
		}
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageImportFailed),
			Message:   fmt.Sprintf("Optimization failed: %v", err),
			Exception: err,
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
		onProgress(errs.ProgressEvent{
			Phase: "complete", Status: "complete", Message: "Image import complete.",
		})
	}

	importMsg := "Image imported successfully"
	if len(importWarnings) > 0 {
		importMsg += fmt.Sprintf(" (%s)", strings.Join(importWarnings, "; "))
	}

	return &errs.OperationResult{
		Status:   "success",
		Code:     "image.imported",
		Item:     imageItem,
		Message:  importMsg,
		Warnings: importWarnings,
	}
}

// ImageWarm pre-decompresses images to ready pool for fast VM creation.
// Matches Python's ImageOperation.warm() exactly.
// Python: input can be None when all=True — Go handles with nil check.
func (op *Operation) ImageWarm(
	ctx context.Context,
	input *inputs.ImageInput,
	all bool,
	onProgress func(errs.ProgressEvent),
) *errs.OperationResult {
	var images []*model.ImageItem

	if all {
		var err error
		images, err = op.Repos.Image.ListAll(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeDatabaseError),
				Message:   fmt.Sprintf("Failed to list images: %v", err),
				Exception: err,
			}
		}
	} else if input != nil {
		request := inputs.NewImageRequest(*input, op.Connection.DB(), op.Repos.Image)
		resolved, err := request.Resolve(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeImageNotFound),
				Message:   fmt.Sprintf("Image resolution failed: %v", err),
				Exception: err,
			}
		}
		images = resolved.Images
	} else {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImageNotFound),
			Message: "Image input required when all=false",
		}
	}

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "warm", Status: "running", Message: "Warming images...",
		})
	}

	warmed, err := op.Services.Image.EnsureCached(images)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "image.warm_failed",
			Message:   fmt.Sprintf("Warming failed: %v", err),
			Exception: err,
		}
	}

	if onProgress != nil {
		onProgress(errs.ProgressEvent{
			Phase: "warm", Status: "complete", Message: "Warming complete.",
		})
	}

	for _, path := range warmed {
		slog.Info("Image warmed", "path", path)
	}

	return &errs.OperationResult{
		Status: "success",
		Code:   "image.warmed",
		Item:   warmed,
	}
}

// ImageRemove removes images by input.
// Matches Python's ImageOperation.remove() exactly.
func (op *Operation) ImageRemove(ctx context.Context, input *inputs.ImageInput, force bool) *errs.BatchResult {
	results := make([]errs.OperationResult, 0)

	request := inputs.NewImageRequest(*input, op.Connection.DB(), op.Repos.Image)
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
	if op.Enr != nil {
		_ = op.Enr.EnrichImage(ctx, images)
	}

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

		results = append(results, errs.OperationResult{
			Status: "success",
			Code:   "image.removed",
			Item:   img,
		})
	}

	return &errs.BatchResult{Items: results}
}

// ImageFindExisting checks DB for existing image for a spec.
// Matches Python's ImageOperation.find_existing_image().
func (op *Operation) ImageFindExisting(spec *model.ImageSpec) *model.ImageItem {
	item, _ := op.Repos.Image.GetByType(context.Background(), spec.Type)
	if item == nil && spec.Version != "" {
		item, _ = op.Repos.Image.GetByVersionAndType(context.Background(), spec.Version, spec.Type)
	}
	if item != nil && item.Path != "" {
		if _, err := os.Stat(item.Path); err == nil {
			return item
		}
	}
	return nil
}

// ImageListAll returns images.
// Matches Python's ImageOperation.list_all() exactly.
// When remote=false, returns ([]*model.ImageItem, nil, error).
// When remote=true, returns (nil, []*model.ImageVersion, error).
// When type_filter is set and remote=true, only returns versions for that specific image type.
// When inputs is set and remote=false, filters local images by the given identifiers.
// no_cache bypasses cached version listings when remote=true.
func (op *Operation) ImageListAll(
	ctx context.Context,
	remote bool,
	typeFilter string,
	imgInputs *inputs.ImageInput,
	noCache bool,
) ([]*model.ImageItem, []*model.ImageVersion, error) {
	if remote {
		// Discover remote images via version resolver
		// Resolve ci_version from default firecracker binary
		resolvedCIVersion := ""
		binRepo := binary.NewRepository(op.Connection.DB())
		defaultBin, _ := binRepo.GetDefault(ctx, "firecracker")
		if defaultBin != nil && defaultBin.CIVersion != nil {
			resolvedCIVersion = *defaultBin.CIVersion
		}

		// Resolve cache_ttl from settings
		var cacheTTL int
		if noCache {
			cacheTTL = 0
		} else {
			cacheTTL = 3600
			if op.Services.Config != nil {
				if ttlRaw, err := op.Services.Config.Get(ctx, "defaults.image", "remote_list_cache_ttl"); err == nil {
					if ttl, ok := ttlRaw.(int); ok && ttl > 0 {
						cacheTTL = ttl
					}
				}
			}
		}

		// If noCache, pass 0 to skip cache
		cacheTTLParam := cacheTTL
		if noCache {
			cacheTTLParam = 0
		}

		// Resolve arch from settings (matches Python: SettingsService.resolve(db, "defaults.image", "arch"))
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x86_64"
		} else if arch == "arm64" {
			arch = "aarch64"
		}
		if op.Services.Config != nil {
			if archRaw, err := op.Services.Config.Get(ctx, "defaults.image", "arch"); err == nil {
				if archStr, ok := archRaw.(string); ok && archStr != "" {
					arch = archStr
				}
			}
		}

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
			filtered := make([]map[string]any, 0)
			for _, cfg := range imageTypesConfig {
				if t, ok := cfg["type"].(string); ok && t == typeFilter {
					filtered = append(filtered, cfg)
				}
			}
			imageTypesConfig = filtered
			if len(imageTypesConfig) == 0 {
				return nil, []*model.ImageVersion{}, nil
			}
		}

		// Use HttpDirVersionResolver to get ImageVersion objects (matches Python exactly)
		resolver := image.NewHttpDirVersionResolver()
		versionMap := resolver.Resolve(ctx, imageTypesConfig, arch, cacheTTLParam, resolvedCIVersion)
		var versions []*model.ImageVersion
		for _, vs := range versionMap {
			for i := range vs {
				versions = append(versions, &vs[i])
			}
		}

		return nil, versions, nil
	}

	// Local images from DB
	if imgInputs != nil {
		// Filter by identifiers if provided
		request := inputs.NewImageRequest(*imgInputs, op.Connection.DB(), op.Repos.Image)
		resolved, err := request.Resolve(ctx)
		if err != nil {
			return nil, nil, err
		}
		return resolved.Images, nil, nil
	}
	items, err := op.Services.Image.ListAll(ctx, true, false)
	return items, nil, err
}

// ImageGet returns a single image by ID prefix or type.
// Matches Python's ImageOperation.get() exactly — uses ImageRequest for resolution.
func (op *Operation) ImageGet(ctx context.Context, input *inputs.ImageInput) (*model.ImageItem, error) {
	request := inputs.NewImageRequest(*input, op.Connection.DB(), op.Repos.Image)
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
func (op *Operation) ImageInspect(ctx context.Context, input *inputs.ImageInput) (*responses.ImageInspect, error) {
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
func (op *Operation) ImageSetDefault(ctx context.Context, input *inputs.ImageInput) *errs.OperationResult {
	request := inputs.NewImageRequest(*input, op.Connection.DB(), op.Repos.Image)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageNotFound),
			Message:   fmt.Sprintf("Image not found: %v", err),
			Exception: err,
		}
	}
	if len(resolved.Images) > 1 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeImageNotFound),
			Message: "Expected exactly one image identifier",
		}
	}
	img := resolved.Images[0]
	if err := op.Repos.Image.SetDefault(ctx, img.ID); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeImageNotFound),
			Message:   fmt.Sprintf("Failed to set default: %v", err),
			Exception: err,
		}
	}
	auditLog := logging.NewAuditLog(op.CacheDir)
	truncatedID := img.ID
	if len(truncatedID) > 6 {
		truncatedID = truncatedID[:6]
	}
	_ = auditLog.LogOperation("image.set_default", map[string]interface{}{"id": truncatedID}, "")
	return &errs.OperationResult{
		Status: "success",
		Code:   "image.default_set",
		Item:   img,
	}
}

// resolveProvisionerType reads settings.guestfs_enabled to choose provisioner type.
// Matches Python's: guestfs_enabled = bool(SettingsService.resolve(db, "settings", "guestfs_enabled"))
//
//	provisioner_type = ProvisionerType.GUESTFS if guestfs_enabled else ProvisionerType.LOOP_MOUNT
//
// Returns image.ProvisionerType to match the type expected by Service methods.
func (op *Operation) resolveProvisionerType(ctx context.Context) image.ProvisionerType {
	if op.Services.Config != nil {
		guestfsEnabledRaw, err := op.Services.Config.Get(ctx, "settings", "guestfs_enabled")
		if err == nil {
			if guestfsEnabled, ok := guestfsEnabledRaw.(bool); ok && guestfsEnabled {
				return image.ProvisionerTypeGuestFS
			}
		}
	}
	return image.ProvisionerTypeLoopMount
}

// isPartitionDetectionError checks if an error is a RootPartitionDetectionError
// or TieDetectedError (matching Python's exception catching pattern).
func isPartitionDetectionError(err error) bool {
	if err == nil {
		return false
	}
	// Check if it's a DomainError with the root partition detection code
	var de *errs.DomainError
	if asErr, ok := err.(*errs.DomainError); ok {
		de = asErr
	} else if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if asErr, ok := wrapped.Unwrap().(*errs.DomainError); ok {
			de = asErr
		}
	}
	if de != nil {
		// RootPartitionDetectionError and TieDetectedError now use CodeInternal
		// as the generic fallback, so detect by message pattern instead.
		if de.Message == "no partitions to evaluate" ||
			strings.HasPrefix(de.Message, "Tie detected between partitions") {
			return true
		}
	}
	return false
}

// Compile-time check
