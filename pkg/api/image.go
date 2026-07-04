// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mvmctl/internal/assets"
	"mvmctl/internal/core/image"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/timinglog"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ImageAPI defines the public interface for image operations.
type ImageAPI interface {
	ImagePrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	ImagePull(
		ctx context.Context,
		input inputs.ImagePullInput,
		onProgress event.OnProgressCallback,
	) (*model.ImageItem, error)
	ImageImport(
		ctx context.Context,
		input inputs.ImageImportInput,
		onProgress event.OnProgressCallback,
	) (*model.ImageItem, error)
	ImageWarm(
		ctx context.Context,
		input inputs.ImageInput,
		all bool,
		onProgress event.OnProgressCallback,
	) ([]string, error)
	ImageRemove(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult
	ImageListAll(
		ctx context.Context,
		remote bool,
		typeFilter string,
		noCache bool,
		onProgress event.OnProgressCallback,
	) ([]*model.ImageItem, []model.VersionInfo, error)
	ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error)
	ImageInspect(ctx context.Context, input inputs.ImageInput) (*results.ImageInspect, error)
	ImageSetDefault(ctx context.Context, input inputs.ImageInput) error
}

// ImagePrune prunes unused images.
// queries Repository for
// referenced images instead of using img.VMs field.
func (op *Operation) ImagePrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	allImages, err := op.Repos.Image.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list images: %v", err), err)
	}
	defaultItem, _ := op.Repos.Image.GetDefault(ctx)
	var defaultID string
	if defaultItem != nil {
		defaultID = defaultItem.ID
	}
	// Get referenced image IDs from VMs pattern)
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
			// Calls ImageRemove through the full remove pipeline (BatchResult, VM reference, etc.)
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
func (op *Operation) ImagePull(
	ctx context.Context,
	input inputs.ImagePullInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	// Resolve pull input (arch, output dir, validation)
	resolved, err := input.Resolve(ctx, op.Services.Config, op.Repos.Image)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImagePullFailed, fmt.Sprintf("Failed to resolve pull input: %v", err), err)
	}
	// Resolve cache TTL from settings
	cacheTTL := 0
	if !resolved.NoCache {
		cacheTTL, _ = op.Services.Config.GetInt(ctx, "defaults.image", "remote_list_cache_ttl")
	}
	// Load image types config from embedded assets
	rawYAML, err := assets.ReadFile("images.yaml")
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImagePullFailed, fmt.Sprintf("Failed to load images.yaml: %v", err), err)
	}
	imageTypesConfig, err := image.LoadImageTypesConfig(rawYAML)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeImagePullFailed,
			fmt.Sprintf("Failed to parse image types config: %v", err),
			err,
		)
	}
	// Determine CI version and ubuntu version:
	// For firecracker-s3 types, the user-specified version IS the CI version.
	// The ubuntu version is auto-discovered from the S3 listing.
	resolvedCIVersion := ""
	ciVersionFromInput := false
	if resolved.Version != "" {
		for _, cfg := range imageTypesConfig {
			if cfg.Type == resolved.Type && cfg.Resolver == "firecracker-s3" {
				v := resolved.Version
				if !strings.HasPrefix(v, "v") && !strings.HasPrefix(v, "V") {
					v = "v" + v
				}
				resolvedCIVersion = v
				ciVersionFromInput = true
				resolved.Version = "" // Auto-discover ubuntu version from S3 listing
				slog.Debug("Using firecracker-s3 version as CI version", "ci_version", v, "type", resolved.Type)
				break
			}
		}
	}
	if !ciVersionFromInput {
		resolvedCIVersion, err = op.resolveCIVersion(ctx)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeImagePullFailed, err.Error(), err)
		}
	}
	// Resolve version spec (latest, partial, or exact) to concrete version
	resolvedVersion, err := op.Services.Image.ResolveVersion(
		ctx,
		resolved.Type,
		resolved.Version,
		resolved.Arch,
		resolvedCIVersion,
		imageTypesConfig,
	)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImagePullFailed, fmt.Sprintf("Failed to resolve image version: %v", err), err)
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
		return nil, errs.WrapMsg(
			errs.CodeImagePullFailed,
			fmt.Sprintf("Failed to resolve spec for %s: %v", resolved.Type, err),
			err,
		)
	}
	if len(specs) == 0 {
		return nil, errs.New(
			errs.CodeImagePullFailed,
			fmt.Sprintf("No matching image spec for type=%q version=%q", resolved.Type, resolved.Version),
		)
	}
	spec := specs[0]
	tl := timinglog.Start("image_pull", "image_type", spec.Type, "image_version", spec.Version)
	defer tl.Complete()

	tl.Stage("resolve")

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
		return nil, errs.WrapMsg(errs.CodeImagePullFailed, fmt.Sprintf("Failed to create temp dir: %v", err), err)
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
		return nil, errs.WrapMsg(errs.CodeImagePullFailed, fmt.Sprintf("Download failed: %v", err), err)
	}
	tl.Stage("download")

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
		// Catch RootPartitionDetectionError and TieDetectedError
		if isPartitionDetectionError(err) {
			return nil, errs.WrapMsg(errs.CodeImageAcquireFailed, err.Error(), err)
		}
		return nil, errs.WrapMsg(errs.CodeImageCorrupt, fmt.Sprintf("Extraction failed: %v", err), err)
	}
	tl.Stage("extract")

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "optimize", Status: "running", Message: "Debloating and shrinking filesystem...",
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
			return nil, errs.WrapMsg(errs.CodeImageAcquireFailed, err.Error(), err)
		}
		return nil, errs.WrapMsg(errs.CodeImageCorrupt, fmt.Sprintf("Optimization failed: %v", err), err)
	}
	tl.Stage("optimize")

	imageItem.IsImported = false

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
	tl.Stage("finalize")

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "complete", Status: "complete", Message: "Image pull complete.",
		})
	}
	return imageItem, nil
}

// ImageImport imports a local image file.
func (op *Operation) ImageImport(
	ctx context.Context,
	input inputs.ImageImportInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	// Resolve import input (arch, format, validation)
	resolved, err := input.Resolve(ctx, op.Services.Config, op.Repos.Image, op.Repos.VM)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeImageImportFailed,
			fmt.Sprintf("Failed to resolve import input: %v", err),
			err,
		)
	}
	// Sync source VM filesystem before copying rootfs (best-effort)
	if resolved.SourceVM != nil && resolved.SourceVM.Status == model.VMStatusRunning {
		slog.Debug("Syncing filesystem on source VM before import", "vm", resolved.SourceVM.Name)
		if client, err := op.vsockClient(ctx, resolved.SourceVM); err == nil {
			_, _ = client.Exec(ctx, "sync", "root", 30, nil, false)
			client.Teardown(ctx)
		} else {
			slog.Warn("Failed to connect to VM for sync before import", "vm", resolved.SourceVM.Name, "error", err)
		}
	}
	existing, _ := op.Repos.Image.GetByVersionAndType(ctx, resolved.Version, resolved.Type)
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
		Version: resolved.Version,
		Arch:    resolved.Arch,
		Source:  *resolved.Source,
		Format:  resolved.Format,
	}
	if resolved.Version != "" {
		spec.Name = resolved.Type + " " + resolved.Version
	} else {
		spec.Name = resolved.Type
	}
	tl := timinglog.Start("image_import", "image_type", resolved.Type)
	defer tl.Complete()

	timestamp := time.Now().Format(time.RFC3339)
	imageID := crypto.ImageID(fmt.Sprintf("%s:%s", spec.Type, spec.Version), spec.Source, timestamp)
	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "extract", Status: "running", Message: "Extracting image...",
		})
	}
	extractedPath, err := op.Services.Image.ExtractImage(
		ctx,
		*resolved.Source,
		imageID,
		infra.GetImagesDir(),
		resolved.Format,
		resolved.Partition,
		resolved.DisabledDetectors,
		op.ProvisionerType,
	)
	if err != nil {
		if isPartitionDetectionError(err) {
			return nil, errs.WrapMsg(errs.CodeImageImportFailed, err.Error(), err)
		}
		return nil, errs.WrapMsg(errs.CodeImageImportFailed, fmt.Sprintf("Extraction failed: %v", err), err)
	}
	tl.Stage("extract")

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "optimize", Status: "running", Message: "Debloating and shrinking filesystem...",
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
			return nil, errs.WrapMsg(errs.CodeImageImportFailed, err.Error(), err)
		}
		return nil, errs.WrapMsg(errs.CodeImageImportFailed, fmt.Sprintf("Optimization failed: %v", err), err)
	}
	tl.Stage("optimize")

	imageItem.IsImported = true
	_ = op.Repos.Image.Upsert(ctx, imageItem)
	if input.SetDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	} else if existing != nil && existing.IsDefault {
		_ = op.Repos.Image.SetDefault(ctx, imageItem.ID)
	}
	if existing != nil && existing.ID != imageItem.ID {
		removed := op.Services.Image.RemoveImageFiles(existing)
		// Hard-delete when no VMs reference the old image, soft-delete otherwise.
		vms, vmErr := op.Repos.VM.GetByImageIDs(ctx, []string{existing.ID})
		if vmErr == nil && len(vms) == 0 {
			_ = op.Repos.Image.Delete(ctx, existing.ID)
		} else {
			_ = op.Repos.Image.SoftDelete(ctx, existing.ID)
		}
		if len(removed) > 0 {
			slog.Info("Cleaned up old image files", "count", len(removed), "id", imageItem.ID)
		}
	}
	tl.Stage("finalize")

	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "complete", Status: "complete", Message: "Image import complete.",
		})
	}
	return imageItem, nil
}

// ImageWarm pre-decompresses images to ready pool for fast VM creation.
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
			return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list images: %v", err), err)
		}
	} else {
		var resolveErr error
		images, resolveErr = input.Resolve(ctx, op.Repos.Image)
		if resolveErr != nil {
			return nil, errs.WrapMsg(
				errs.CodeImageNotFound,
				fmt.Sprintf("Image resolution failed: %v", resolveErr),
				resolveErr,
			)
		}
	}
	if onProgress != nil {
		onProgress(event.Progress{
			Phase: "warm", Status: "running", Message: "Warming images...",
		})
	}
	warmed, err := op.Services.Image.EnsureCached(images)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageWarmFailed, fmt.Sprintf("Warming failed: %v", err), err)
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
func (op *Operation) ImageRemove(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult {
	results := make([]errs.OperationResult, 0)
	images, err := input.Resolve(ctx, op.Repos.Image)
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
	// Batch-enrich with VM references
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
		// Filter by type BEFORE resolving
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
func (op *Operation) ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error) {
	images, err := input.Resolve(ctx, op.Repos.Image)
	if err != nil {
		return nil, err
	}
	if len(images) > 1 {
		return nil, fmt.Errorf("expected exactly one image identifier")
	}
	return images[0], nil
}

// ImageInspect returns grouped dict of an image.
func (op *Operation) ImageInspect(ctx context.Context, input inputs.ImageInput) (*results.ImageInspect, error) {
	img, err := op.ImageGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return &results.ImageInspect{
		Image: results.ImageItemInfo{
			ID: img.ID, Name: img.Name, Type: img.Type,
			Arch: img.Arch, IsDefault: img.IsDefault, IsPresent: img.IsPresent,
		},
		Storage: results.ImageStorageInfo{
			Path: img.Path, FSType: img.FSType, FSUUID: img.FSUUID,
			CompressedSize: img.CompressedSize, OriginalSize: img.OriginalSize,
		},
		Compression: results.ImageCompressionInfo{
			Format: img.CompressedFormat, Ratio: img.CompressionRatio,
		},
		Requirements: results.ImageRequirementsInfo{
			MinRootfsSizeMiB: img.MinRootfsSizeMiB,
		},
		Timestamps: results.ImageTimestampsInfo{
			PulledAt: img.PulledAt, CreatedAt: img.CreatedAt, UpdatedAt: img.UpdatedAt,
		},
	}, nil
}

// ImageSetDefault sets an image as default.
func (op *Operation) ImageSetDefault(ctx context.Context, input inputs.ImageInput) error {
	images, err := input.Resolve(ctx, op.Repos.Image)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageNotFound, fmt.Sprintf("Image not found: %v", err), err)
	}
	if len(images) > 1 {
		return errs.New(errs.CodeImageNotFound, "Expected exactly one image identifier")
	}
	img := images[0]
	if err := op.Repos.Image.SetDefault(ctx, img.ID); err != nil {
		return errs.WrapMsg(errs.CodeImageNotFound, fmt.Sprintf("Failed to set default: %v", err), err)
	}
	op.AuditLog.LogOperation("image.set_default", map[string]any{"id": img.ID}, "")
	return nil
}

// isPartitionDetectionError checks if an error is a RootPartitionDetectionError
// or TieDetectedError.
func isPartitionDetectionError(err error) bool {
	var de *errs.DomainError
	if !errors.As(err, &de) {
		return false
	}
	return de.Code == errs.CodeRootPartitionDetection || de.Code == errs.CodeTieDetected
}
