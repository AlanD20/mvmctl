package image

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/disk"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/pool"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
)

// Time constants matching Python's CONST_MEBIBYTE_BYTES etc.
// ── Compression defaults ──
const (
	CompressionLevel  = 3
	CompressionFormat = "zst"
)

const (
	MiB              = 1024 * 1024
	Percent          = 100
	RatioMin         = 1.0
	RuntimeBufferMB  = 160
	RootfsHeadroom   = 1.25
	MinRootfsSizeMiB = 128
)

// FSCanShrink contains filesystem types that support shrink/deblob (ext family only).
var FSCanShrink = map[string]bool{
	"ext4": true,
	"ext3": true,
	"ext2": true,
}

// Service matches Python's Service in _service.py.
// Handles image processing: compression, decompression, shrinking, format conversion, and pool management.
type Service struct {
	repo Repository
	dl   *download.Downloader
}

// NewService creates a new Service.
func NewService(repo Repository) *Service {
	return &Service{
		repo: repo,
		dl:   download.New(),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Public API
// ──────────────────────────────────────────────────────────────────────────────

// RemoveImage removes an image, handling file deletion and hard/soft delete.
// The image must be pre-enriched with VM references by the caller.
func (s *Service) RemoveImage(ctx context.Context, image *model.ImageItem, force bool) error {
	vms := image.VMs
	hasVMs := len(vms) > 0

	if hasVMs && !force {
		return NewImageError("Image is referenced by VMs")
	}

	// Delete ALL related files from disk
	removed := s.RemoveImageFiles(image)
	if len(removed) > 0 {
		slog.Info("Removed image files", "files", strings.Join(removed, ", "))
	}

	// Hard delete if no VMs, soft delete if VMs exist (with force)
	if hasVMs {
		if err := s.repo.SoftDelete(ctx, image.ID); err != nil {
			return err
		}
	} else {
		if err := s.repo.Delete(ctx, image.ID); err != nil {
			return err
		}
	}

	return nil
}

// RemoveManyPaths removes files for multiple images from disk. No DB changes.
// Matches Python's Service.remove_many_paths().
func (s *Service) RemoveManyPaths(images []*model.ImageItem) []string {
	var removed []string
	for _, image := range images {
		removed = append(removed, s.RemoveImageFiles(image)...)
	}
	return removed
}

// RemoveImageFiles removes all files for an image from disk. No DB changes.
// Matches Python's Service._remove_image_files().
func (s *Service) RemoveImageFiles(image *model.ImageItem) []string {
	var removed []string

	entries, err := os.ReadDir(infra.GetImagesDir())
	if err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), image.ID) && !entry.IsDir() {
				if err := os.Remove(filepath.Join(infra.GetImagesDir(), entry.Name())); err == nil {
					removed = append(removed, entry.Name())
				}
			}
		}
	}

	entries, err = os.ReadDir(infra.GetWarmImagesDir())
	if err != nil {
		return removed
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(infra.GetWarmImagesDir(), entry.Name())); err == nil {
			removed = append(removed, entry.Name())
		}
	}

	return removed
}

// ListAll lists all images, syncing is_present flag with filesystem.
// remote controls whether to also list remote images (matches Python signature).
func (s *Service) ListAll(ctx context.Context, remote bool, verify bool) ([]*model.ImageItem, error) {
	images, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return images, nil
	}

	var missingIDs []string
	for _, img := range images {
		resolved := s.resolveImagePath(img)
		if resolved == "" {
			missingIDs = append(missingIDs, img.ID)
		} else {
			if _, err := os.Stat(resolved); os.IsNotExist(err) {
				missingIDs = append(missingIDs, img.ID)
			}
		}
	}

	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		images, err = s.repo.ListAll(ctx)
		if err != nil {
			return nil, err
		}
	}

	return images, nil
}

// resolveImagePath resolves the actual filesystem path for an image.
// Tries the stored path first, then known extensions. Returns "" if no file found.
func (s *Service) resolveImagePath(image *model.ImageItem) string {
	if image.Path != "" {
		if _, err := os.Stat(image.Path); err == nil {
			return image.Path
		}
	}
	for _, ext := range infra.SupportedImageExtensions {
		candidate := filepath.Join(infra.GetImagesDir(), image.ID+ext)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// OptimizeImage shrinks and compresses an image. Returns fully constructed model.ImageItem and warnings.
// Matches Python's Service.optimize_image() parameter order EXACTLY.
func (s *Service) OptimizeImage(
	ctx context.Context,
	imagePath string,
	imageID string,
	spec *model.ImageSpec,
	timestamp string,
	skipOptimization bool,
	provisionerType provisioner.ProvisionerType,
	warnings []string,
) (*model.ImageItem, []string, error) {
	t0 := time.Now()
	fsType, resolveErr := s.resolveFSType(ctx, imagePath)
	if resolveErr != nil {
		return nil, warnings, resolveErr
	}
	fsUUID := system.DetectFilesystemUUID(ctx, imagePath)
	t1 := time.Now()
	slog.Debug("fs detect", "elapsed_seconds", t1.Sub(t0).Seconds())

	// ── Single Provisioner reused across all phases ──
	// Each method (DetectOS, Run) creates a fresh backend session internally,
	// so the struct itself is just shared config. Flags are cleared after Run()
	// to keep the struct reusable.
	p := NewProvisioner(imagePath, provisionerType, fsType)

	// ── Detect OS type from the image (always, even when skipping) ──
	detectedOS := ""
	osResult, osErr := p.DetectOS(ctx)
	if osErr == nil {
		detectedOS = osResult
	} else {
		slog.Warn("OS detection failed, falling back to spec type", "image_id", imageID)
	}

	imageType := detectedOS
	if imageType == "" {
		imageType = spec.Type
	}
	imageName := fmt.Sprintf("%s (imported)", imageType)

	if skipOptimization {
		slog.Info("Skipping optimization (shrink and compression)")
		info, _ := os.Stat(imagePath)
		actualSize := info.Size()
		return &model.ImageItem{
			ID:               imageID,
			Type:             imageType,
			Version:          spec.Version,
			Name:             imageName,
			Arch:             spec.Arch,
			Path:             imagePath,
			FSType:           fsType,
			Distro:           detectedOS,
			MinRootfsSizeMiB: int(actualSize / MiB),
			OriginalSize:     actualSize,
			IsDefault:        false,
			IsPresent:        true,
			PulledAt:         timestamp,
			CreatedAt:        timestamp,
			UpdatedAt:        timestamp,
			FSUUID:           fsUUID,
			CompressedSize:   nil,
			CompressionRatio: nil,
			CompressedFormat: nil,
		}, warnings, nil
	}

	if _, statErr := os.Stat(imagePath); os.IsNotExist(statErr) {
		return nil, warnings, NewImageError(
			fmt.Sprintf("Image processing failed: output file not created at %s", imagePath),
		)
	}

	// ── Convert, deblob, shrink in a single backend session ──
	if fsType == "btrfs" {
		slog.Info("Converting filesystem from btrfs to ext4...")
		p.ConvertTo("ext4")
		fsType = "ext4"
	}

	preShrinkInfo, _ := os.Stat(imagePath)
	preShrinkSize := preShrinkInfo.Size()

	p.Deblob()
	if FSCanShrink[fsType] {
		p.Shrink()
	}
	optimized, runErr := p.Run(ctx)
	if runErr != nil {
		return nil, warnings, runErr
	}
	if !optimized && warnings != nil {
		warnings = append(warnings,
			"Image optimization skipped: no provisioner backend available. "+
				"Run 'python scripts/build_services.py' or enable libguestfs for "+
				"faster boot times.")
	}

	postShrinkInfo, _ := os.Stat(imagePath)
	postShrinkSize := postShrinkInfo.Size()

	t2 := time.Now()
	slog.Debug("shrink", "elapsed_seconds", t2.Sub(t1).Seconds())

	shrinkSuccessful := preShrinkSize > 0 && postShrinkSize > 0
	if shrinkSuccessful {
		reduction := float64(preShrinkSize-postShrinkSize) / float64(preShrinkSize) * 100.0
		slog.Info("Image shrunk",
			"before_mib", float64(preShrinkSize)/MiB,
			"after_mib", float64(postShrinkSize)/MiB,
			"reduction_pct", reduction)
	} else {
		slog.Warn("Image shrinking not performed (filesystem type may be unsupported or detection failed)")
	}

	compressedPath, compErr := s.compress(imagePath, CompressionLevel, false)
	if compErr != nil {
		return nil, warnings, compErr
	}

	t3 := time.Now()
	slog.Debug("compress", "elapsed_seconds", t3.Sub(t2).Seconds())
	compressedInfo, _ := os.Stat(compressedPath)
	compressedSize := compressedInfo.Size()

	compressionRatio := float64(preShrinkSize) / float64(compressedSize)
	if compressedSize <= 0 {
		compressionRatio = RatioMin
	}

	minimumRootfsSizeMiB := int(postShrinkSize/MiB) + RuntimeBufferMB

	slog.Info("Optimization complete", "total_seconds", t3.Sub(t0).Seconds())

	compressionFormatVal := CompressionFormat

	return &model.ImageItem{
		ID:               imageID,
		Type:             spec.Type,
		Version:          spec.Version,
		Name:             spec.Name,
		Arch:             spec.Arch,
		Distro:           detectedOS,
		Path:             compressedPath,
		FSType:           fsType,
		MinRootfsSizeMiB: minimumRootfsSizeMiB,
		OriginalSize:     preShrinkSize,
		IsDefault:        false,
		IsPresent:        true,
		PulledAt:         timestamp,
		CreatedAt:        timestamp,
		UpdatedAt:        timestamp,
		FSUUID:           fsUUID,
		CompressedSize:   &compressedSize,
		CompressionRatio: &compressionRatio,
		CompressedFormat: &compressionFormatVal,
	}, warnings, nil
}

// DownloadImage downloads image from remote source. Returns path to downloaded file.
// progress is optional (nil allowed, matching Python's progress_callback=None).
// ctx is passed through to the shared HttpDownload infrastructure for proper
// cancellation and timeout propagation.
func (s *Service) DownloadImage(
	ctx context.Context,
	spec *model.ImageSpec,
	imageID string,
	outputDir string,
	force bool,
	ciVersion string,
	progress func(int64, int64),
) (string, error) {
	downloadPath := filepath.Join(outputDir, imageID+".download")

	if force {
		os.Remove(downloadPath)
	}

	templateVars := s.getTemplateVariables(spec, ciVersion)
	source := spec.Source
	if strings.Contains(spec.Source, "{") {
		var err error
		source, err = s.resolveSourceTemplate(ctx, spec, templateVars)
		if err != nil {
			return "", err
		}
	}

	var resolvedSHA256 string
	if spec.SHA256 != "" {
		resolvedSHA256 = strings.ToLower(spec.SHA256)
	}

	sha256URL := spec.SHA256URL
	if sha256URL != "" {
		if r, err := infra.RenderTemplate(sha256URL, templateVars); err == nil {
			sha256URL = r
		}
	}

	if resolvedSHA256 == "" && sha256URL != "" {
		sourceBasename := ""
		if idx := strings.LastIndex(source, "/"); idx >= 0 {
			sourceBasename = source[idx+1:]
		}
		sha, err := s.fetchSHA256FromURL(ctx, sha256URL, sourceBasename)
		if err == nil && sha != "" {
			resolvedSHA256 = sha
		}
	}

	// Download the file
	if err := s.downloadFile(ctx, source, downloadPath, resolvedSHA256, progress); err != nil {
		return "", err
	}

	// Validate downloaded file
	if err := s.validateDownloadedFile(ctx, downloadPath, spec.Format); err != nil {
		return "", err
	}

	return downloadPath, nil
}

// ExtractImage extracts/converts a source image to a root filesystem.
// Handles all formats: qcow2, vhd, vhdx, raw, tar-rootfs, squashfs.
// partition is optional (nil = auto-detect), matching Python's partition: int | None = None.
func (s *Service) ExtractImage(
	ctx context.Context,
	sourcePath string,
	imageID string,
	outputDir string,
	format string,
	partition int,
	disabledDetectors []string,
	provisionerType provisioner.ProvisionerType,
) (string, error) {
	finalPath := filepath.Join(outputDir, imageID+".img")

	switch format {
	case "qcow2", "vhd", "vhdx", "raw":
		return s.extractDiskImage(ctx, sourcePath, finalPath, format, partition, disabledDetectors, provisionerType)
	case "tar-rootfs":
		if err := s.createExt4FromTar(ctx, sourcePath, finalPath, "dynamic"); err != nil {
			return "", err
		}
		return finalPath, nil
	case "squashfs":
		return s.handleSquashfs(ctx, sourcePath, finalPath, "dynamic")
	default:
		return "", NewImageError(fmt.Sprintf("Unknown format: %s", format))
	}
}

// MaterializeTo performs fast durable copy from tmpfs cache to destination.
func (s *Service) MaterializeTo(ctx context.Context, imageID, fsType, outputPath string) error {
	cachedPath := filepath.Join(infra.GetWarmImagesDir(), fmt.Sprintf("%s.%s", imageID, fsType))
	if _, err := os.Stat(cachedPath); os.IsNotExist(err) {
		return NewImageError(fmt.Sprintf("Image not in cache: %s", imageID))
	}

	os.MkdirAll(filepath.Dir(outputPath), infra.DirPerm)

	// Try reflink copy (matching Python: run_cmd(["cp", "--reflink=auto", ...]) with ProcessError fallback)
	result := system.RunCmdCompat(
		ctx,
		[]string{"cp", "--reflink=auto", "--sparse=always", cachedPath, outputPath},
		system.DefaultRunCmdOpts(),
	)
	combined := string(result.StdoutBytes) + string(result.StderrBytes)
	if result.Err != nil {
		if err := system.CopyWithDD(ctx, cachedPath, outputPath, true); err != nil {
			return NewImageError(fmt.Sprintf("cp and dd fallback both failed: cp: %s; dd: %s", combined, err))
		}
	}

	// fdatasync (matching Python's os.fdatasync(f.fileno()), reading file in RB mode)
	f, err := os.Open(outputPath)
	if err != nil {
		return NewImageError(fmt.Sprintf("Failed to open file for fdatasync: %s", err))
	}
	if err := syscall.Fdatasync(int(f.Fd())); err != nil {
		f.Close()
		return NewImageError(fmt.Sprintf("fdatasync failed: %s", err))
	}
	f.Close()

	slog.Info("Copied image", "output", filepath.Base(outputPath))
	return nil
}

// EnsureCached ensures images are decompressed to tmpfs cache, creating if needed.
func (s *Service) EnsureCached(images []*model.ImageItem) ([]string, error) {
	var results []string
	for _, image := range images {
		cachedPath := filepath.Join(infra.GetWarmImagesDir(), fmt.Sprintf("%s.%s", image.ID, image.FSType))

		if _, err := os.Stat(cachedPath); err == nil {
			slog.Debug("Found image in cache", "path", cachedPath)
			results = append(results, cachedPath)
			continue
		}

		if image.CompressedFormat == nil || *image.CompressedFormat == "" {
			slog.Debug("Copying uncompressed image to cache", "path", filepath.Base(cachedPath))
			if err := infra.CopyFile(image.Path, cachedPath); err != nil {
				return nil, fmt.Errorf("copy to cache: %w", err)
			}
		} else {
			fmt_ := *image.CompressedFormat
			suffix := "." + fmt_
			if strings.HasPrefix(fmt_, ".") {
				suffix = fmt_
			}
			compressedPath := image.Path
			ext := filepath.Ext(compressedPath)
			compressedPath = compressedPath[:len(compressedPath)-len(ext)] + suffix

			slog.Debug("Decompressing to cache", "path", filepath.Base(cachedPath))
			if err := s.decompress(compressedPath, cachedPath, fmt_); err != nil {
				return nil, err
			}
		}

		results = append(results, cachedPath)
	}
	return results, nil
}

// GetSpecsFor resolves ImageSpecs from image_types config by type identifiers.
// Matches Python's Service.get_specs_for() EXACTLY with two-phase resolution.
// This is a package-level function (not a method) matching Python's pattern.
func GetSpecsFor(
	ctx context.Context,
	types []string,
	version string,
	arch string,
	cacheTTLSeconds int,
	ciVersion string,
	imageTypesConfig []download.ResolverConfig,
) ([]*model.ImageSpec, error) {
	typeConfigMap := make(map[string]download.ResolverConfig, len(imageTypesConfig))
	for _, cfg := range imageTypesConfig {
		typeConfigMap[cfg.Type] = cfg
	}

	// "latest" alias → "" (select latest from directory listing)
	if version != "" && strings.ToLower(version) == "latest" {
		version = ""
	}

	var results []*model.ImageSpec
	remaining := make([]string, len(types))
	copy(remaining, types)

	// ── Phase 1a: fast-path — construct from type config when version is explicit ──
	if version != "" && len(remaining) > 0 {
		var newRemaining []string
		for _, type_ := range remaining {
			config, ok := typeConfigMap[type_]
			if !ok {
				newRemaining = append(newRemaining, type_)
				continue
			}

			fileDiscovery := false
			if config.Options.FileDiscovery != nil && config.Options.FileDiscovery.Enabled {
				fileDiscovery = true
			}
			if fileDiscovery {
				newRemaining = append(newRemaining, type_)
				continue
			}

			spec, err := ConstructSpecFromTypeConfig(config, version, arch, ciVersion)
			if err != nil {
				return nil, NewImageError(fmt.Sprintf("Failed to construct spec for type '%s': %s", type_, err))
			}
			results = append(results, spec)
		}
		remaining = newRemaining
	}

	// ── Phase 2: try version resolver for types not in flat spec_map ──
	if len(remaining) > 0 {
		availableHTTPTypes := make(map[string]bool)
		var remaining2 []string

		// Single pass: handle http-dir types, collect non-http-dir for next loop.
		for _, type_ := range remaining {
			config, ok := typeConfigMap[type_]
			if !ok {
				remaining2 = append(remaining2, type_)
				continue
			}

			if config.Resolver != "http-dir" {
				remaining2 = append(remaining2, type_)
				continue
			}

			availableHTTPTypes[type_] = true

			versionResult := ResolveVersions(ctx, []download.ResolverConfig{config}, arch, cacheTTLSeconds, ciVersion)
			listings := versionResult[type_]
			if len(listings) == 0 {
				continue
			}

			var chosen model.VersionInfo
			if version != "" {
				found := false
				for _, v := range listings {
					if v.Version == version {
						chosen = v
						found = true
						break
					}
				}
				if !found {
					continue
				}
			} else {
				chosen = listings[0]
			}

			results = append(results, specFromVersion(chosen, arch))
		}
		remaining = remaining2

		// Second loop: remaining resolver types (firecracker-s3, single-source, etc.)
		for _, type_ := range remaining {
			config, ok := typeConfigMap[type_]
			if !ok {
				continue
			}

			versionResult := ResolveVersions(ctx, []download.ResolverConfig{config}, arch, cacheTTLSeconds, ciVersion)
			listings := versionResult[type_]
			if len(listings) == 0 {
				continue
			}

			results = append(results, specFromVersion(listings[0], arch))
		}

		// Compute unresolved types by checking which types in the
		// original request have no result — matching Python's `remaining`
		// semantics where ANY type still in remaining (whether configured
		// or not) is an error.
		var unresolved []string
		typeInResults := make(map[string]bool)
		for _, r := range results {
			typeInResults[r.Type] = true
		}
		for _, type_ := range types {
			if !typeInResults[type_] {
				unresolved = append(unresolved, type_)
			}
		}

		if len(unresolved) > 0 {
			availableTypes := make([]string, 0, len(availableHTTPTypes))
			for k := range availableHTTPTypes {
				availableTypes = append(availableTypes, k)
			}
			sort.Strings(availableTypes)

			if version != "" {
				msg := fmt.Sprintf("Image(s) not found for version '%s': %s. ", version, strings.Join(unresolved, ", "))
				if len(availableTypes) > 0 {
					msg += fmt.Sprintf("Available image types: %s", strings.Join(availableTypes, ", "))
				}
				return nil, NewImageError(msg)
			}
			msg := fmt.Sprintf("Image(s) not found: %s. ", strings.Join(unresolved, ", "))
			if len(availableTypes) > 0 {
				msg += fmt.Sprintf("Available image types: %s", strings.Join(availableTypes, ", "))
			}
			return nil, NewImageError(msg)
		}
	}

	return results, nil
}

// specFromVersion constructs an ImageSpec from a resolved VersionInfo.
func specFromVersion(v model.VersionInfo, arch string) *model.ImageSpec {
	return &model.ImageSpec{
		Type:    v.Type,
		Version: v.Version,
		Name:    fmt.Sprintf("%s %s", v.Type, v.Version),
		Source:  v.DownloadURL,
		Format:  v.Format,
		Arch:    arch,
	}
}

// ResolveRemoteSizes resolves remote image sizes via concurrent HEAD requests.
// Matches Python's Service.resolve_remote_sizes() with max_workers=5.
// Uses download.Downloader.HeadSize (which includes retry + cache) matching
// Python's HttpDownload.head_size().
func (s *Service) ResolveRemoteSizes(
	ctx context.Context,
	specs []*model.ImageSpec,
	ciVersion string,
) []*model.ImageSpec {
	_ = pool.Do(ctx, 5, specs, func(_ context.Context, sp *model.ImageSpec) error {
		templateVars := s.getTemplateVariables(sp, ciVersion)
		source := sp.Source
		if sp.ListURLTemplate != nil && *sp.ListURLTemplate != "" {
			// Dynamic image: resolve source template first
			var err error
			source, err = s.resolveSourceTemplate(ctx, sp, templateVars)
			if err != nil {
				return nil // Python catches Exception → return without setting size
			}
		} else if strings.Contains(sp.Source, "{") {
			// Static image with template: resolve variables
			if r, err := infra.RenderTemplate(sp.Source, templateVars); err == nil {
				source = r
			}
		}

		// HEAD request with retry + cache — matching Python's HttpDownload.head_size()
		size, ok := s.dl.HeadSize(ctx, download.RequestOpts{
			URL: source, Timeout: 10,
			UseCache: true, CacheTTLSeconds: 300,
		})
		if ok && size >= 0 {
			sp.Size = &size
		}
		// Python's resolve_remote_sizes catches all exceptions (try/except Exception)
		// and silently skips — matching our behavior of just returning without setting size
		return nil
	})

	return specs
}

// compress compresses the image using in-process zstd library.
// Matches Python's Service.compress() exactly.
func (s *Service) compress(imagePath string, level int, keepSource bool) (string, error) {
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return "", NewImageCompressionError(fmt.Sprintf("Cannot compress: source file does not exist: %s", imagePath))
	}

	info, _ := os.Stat(imagePath)
	originalSize := info.Size()

	// Check for all-zero content
	f, err := os.Open(imagePath)
	if err != nil {
		return "", fmt.Errorf("open source for compress: %w", err)
	}
	firstMB := make([]byte, MiB)
	n, _ := f.Read(firstMB)
	f.Close()

	allZeros := true
	for _, b := range firstMB[:n] {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		return "", NewImageCorruptError(
			fmt.Sprintf("Source file appears to be all zeros: %s. File may be corrupted.", imagePath),
		)
	}

	// Python uses Path.with_suffix(".zst") which REPLACES the existing extension.
	// e.g. foo.img → foo.zst, not foo.img.zst
	ext := filepath.Ext(imagePath)
	compressedPath := imagePath[:len(imagePath)-len(ext)] + ".zst"

	src, err := os.Open(imagePath)
	if err != nil {
		return "", NewImageCompressionError(fmt.Sprintf("Failed to open source: %v", err))
	}
	defer src.Close()

	dst, err := os.Create(compressedPath)
	if err != nil {
		return "", NewImageCompressionError(fmt.Sprintf("Failed to create output: %v", err))
	}
	defer dst.Close()

	// Use in-process zstd (matching Python's zstandard library)
	compressedWriter, err := zstd.NewWriter(dst,
		zstd.WithEncoderLevel(zstd.EncoderLevel(level)),
		zstd.WithEncoderConcurrency(runtime.NumCPU()),
	)
	if err != nil {
		return "", NewImageCompressionError(fmt.Sprintf("Failed to create zstd compressor: %v", err))
	}

	// Copy source data through zstd encoder
	_, err = io.Copy(compressedWriter, src)
	if err != nil {
		compressedWriter.Close()
		dst.Close()
		os.Remove(compressedPath)
		return "", NewImageCompressionError(fmt.Sprintf("Failed to compress image: %v", err))
	}
	compressedWriter.Close()
	dst.Close()

	if _, err := os.Stat(compressedPath); os.IsNotExist(err) {
		return "", NewImageCompressionError(fmt.Sprintf("Compression failed: output not created: %s", compressedPath))
	}

	compInfo, _ := os.Stat(compressedPath)
	compressedSize := compInfo.Size()
	if compressedSize == 0 {
		os.Remove(compressedPath)
		return "", NewImageCompressionError(
			fmt.Sprintf("Compression failed: output is empty (source was %d bytes)", originalSize),
		)
	}

	ratio := float64(originalSize) / float64(compressedSize)

	if !keepSource {
		os.Remove(imagePath)
	}

	slog.Info("Compressed",
		"file", filepath.Base(imagePath),
		"original_mb", originalSize/MiB,
		"compressed_mb", compressedSize/MiB,
		"ratio", ratio)

	return compressedPath, nil
}

// decompress decompresses the image to the specified output path using in-process zstd.
// Matches Python's Service.decompress() exactly.
func (s *Service) decompress(compressedPath, outputPath, compressedFormat string) error {
	if compressedFormat == "" {
		return NewImageDecompressionError("compressedFormat must be specified; got empty string")
	}
	if compressedFormat != "zst" {
		return NewImageDecompressionError(
			fmt.Sprintf("Unsupported compression format: '%s'. Only 'zst' (zstd) is supported.", compressedFormat),
		)
	}

	// Python's decompress() calls self._validate_image_path() first, which raises
	// ImageError (not ImageDecompressionError) if the path does not exist or is empty.
	if _, err := os.Stat(compressedPath); os.IsNotExist(err) {
		return NewImageError(fmt.Sprintf("Image file not found: %s", compressedPath))
	}
	if info, _ := os.Stat(compressedPath); info != nil && info.Size() == 0 {
		return NewImageEmptyError(fmt.Sprintf("Image file is empty: %s", compressedPath))
	}

	// Use in-process zstd (matching Python's zstandard library)
	src, err := os.Open(compressedPath)
	if err != nil {
		return NewImageDecompressionError(fmt.Sprintf("Failed to open compressed file: %v", err))
	}
	defer src.Close()

	dst, err := os.Create(outputPath)
	if err != nil {
		return NewImageDecompressionError(fmt.Sprintf("Failed to create output file: %v", err))
	}
	defer dst.Close()

	decoder, err := zstd.NewReader(src)
	if err != nil {
		return NewImageDecompressionError(fmt.Sprintf("Failed to create zstd decompressor: %v", err))
	}
	defer decoder.Close()

	_, err = io.Copy(dst, decoder)
	if err != nil {
		return NewImageDecompressionError(fmt.Sprintf("Failed to decompress image: %v", err))
	}

	dst.Close()

	// Validate output — matching Python's _validate_image_path() result handling.
	// Python catches ImageEmptyError from _validate_image_path and re-raises as
	// ImageDecompressionError with output_path unlinked.
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return NewImageDecompressionError(
			fmt.Sprintf("Decompression failed: output could not be verified: %s", outputPath),
		)
	}
	outInfo, _ := os.Stat(outputPath)
	if outInfo.Size() == 0 {
		os.Remove(outputPath)
		return NewImageDecompressionError(
			fmt.Sprintf("Decompression failed: output could not be verified: %s", outputPath),
		)
	}

	slog.Info("Decompressed",
		"source", filepath.Base(compressedPath),
		"dest", filepath.Base(outputPath),
		"size_mb", outInfo.Size()/MiB)

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Format detection — magic bytes for 6 formats
// ─── Format validators — matching Python's _validate_* methods exactly ──

func (s *Service) validateDownloadedFile(ctx context.Context, downloadedPath, imageFormat string) error {
	if _, err := os.Stat(downloadedPath); os.IsNotExist(err) {
		return NewImageValidationError("Downloaded file not found")
	}

	fileInfo, err := os.Stat(downloadedPath)
	if err != nil {
		return fmt.Errorf("stat downloaded file: %w", err)
	}
	fileSize := fileInfo.Size()
	if fileSize == 0 {
		os.Remove(downloadedPath)
		return NewImageValidationError("Downloaded file is empty")
	}

	var validateErr error
	switch imageFormat {
	case "qcow2":
		validateErr = disk.ValidateQCOW2(downloadedPath)
	case "vhd":
		validateErr = disk.ValidateVHD(downloadedPath, fileSize)
	case "vhdx":
		validateErr = disk.ValidateVHDX(downloadedPath, fileSize)
	case "raw":
		validateErr = disk.ValidateRaw(downloadedPath, fileSize)
	case "squashfs":
		validateErr = disk.ValidateSquashFS(downloadedPath)
	case "tar-rootfs":
		validateErr = disk.ValidateTar(ctx, downloadedPath)
	default:
		os.Remove(downloadedPath)
		return NewImageValidationError(fmt.Sprintf("Unknown format for validation: %s", imageFormat))
	}
	if validateErr != nil {
		os.Remove(downloadedPath)
		return NewImageValidationError(validateErr.Error())
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// disk extraction
// ──────────────────────────────────────────────────────────────────────────────
// Extraction helpers
// ──────────────────────────────────────────────────────────────────────────────

// extractDiskImage extracts root partition from a disk image (qcow2, vhd, vhdx, raw).
// Tries the selected backend first, falls back to loop-mount on ImageError/RuntimeError.
// partition is optional (nil = auto-detect), matching Python's partition: int | None = None.
// Matches Python's _extract_disk_image() EXACTLY.
func (s *Service) extractDiskImage(ctx context.Context,
	inputPath, outputPath, format string,
	partition int, disabledDetectors []string,
	provisionerType provisioner.ProvisionerType,
) (string, error) {
	// Enforce .img suffix — matching Python's output_path.with_suffix(".img") EXACTLY.
	imgPath := outputPath
	if ext := filepath.Ext(imgPath); ext != "" {
		imgPath = imgPath[:len(imgPath)-len(ext)] + ".img"
	} else {
		imgPath = imgPath + ".img"
	}

	if fmtFlag := infra.QemuImgFormat[format]; fmtFlag != "" {
		tmpDir, err := os.MkdirTemp(infra.GetTempDir(), "extract-*")
		if err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		rawPath := filepath.Join(tmpDir, "intermediate.raw")
		if err := s.convertToRaw(ctx, inputPath, rawPath, fmtFlag); err != nil {
			return "", err
		}
		return ExtractViaBackend(ctx, rawPath, imgPath, partition, disabledDetectors, provisionerType)
	} else if format == "raw" {
		return ExtractViaBackend(ctx, inputPath, imgPath, partition, disabledDetectors, provisionerType)
	} else {
		return "", NewImageError(fmt.Sprintf("Unsupported disk image format: %s", format))
	}
}

func (s *Service) convertToRaw(ctx context.Context, inputPath, outputPath, fmtFlag string) error {
	slog.Info("Converting to raw...", "file", filepath.Base(inputPath))
	result := system.RunCmdCompat(ctx, []string{"qemu-img", "convert", "-m", "16", "-f", fmtFlag, "-O", "raw",
		"-t", "none", "-T", "none", "-W", inputPath, outputPath}, system.DefaultRunCmdOpts())
	combined := string(result.StdoutBytes) + string(result.StderrBytes)
	if result.Err != nil {
		return NewImageError(fmt.Sprintf("qemu-img conversion failed: %s", combined))
	}
	slog.Info("Converted to raw", "output", filepath.Base(outputPath))
	return nil
}

func (s *Service) handleSquashfs(ctx context.Context, inputPath, finalPath, minimumRootfsSize string) (string, error) {
	tmpDir, err := os.MkdirTemp(infra.GetTempDir(), "squashfs-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	extractDir := filepath.Join(tmpDir, "squashfs-root")

	result := system.RunCmdCompat(
		ctx,
		[]string{"unsquashfs", "-d", extractDir, inputPath},
		system.DefaultRunCmdOpts(),
	)
	if result.Err != nil {
		return "", NewImageError("unsquashfs failed")
	}

	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return "", NewImageError("mkfs.ext4 not found. Install e2fsprogs package.")
	}

	duResult := system.RunCmdCompat(ctx, []string{"du", "-sb", extractDir}, system.DefaultRunCmdOpts())
	contentBytes := int64(0)
	if duResult.Err == nil {
		fields := strings.Fields(duResult.Stdout)
		if len(fields) > 0 {
			contentBytes, _ = strconv.ParseInt(fields[0], 10, 64)
		}
	}

	var imageSizeMB int
	if minimumRootfsSize == "dynamic" {
		imageSizeMB = calculateMinimumImageSizeMB(contentBytes)
	} else {
		imageSizeMB, _ = strconv.Atoi(minimumRootfsSize)
	}

	truncResult := system.RunCmdCompat(
		ctx,
		[]string{"truncate", "-s", fmt.Sprintf("%dM", imageSizeMB), finalPath},
		system.DefaultRunCmdOpts(),
	)
	truncCombined := string(truncResult.StdoutBytes) + string(truncResult.StderrBytes)
	if truncResult.Err != nil {
		return "", NewImageError(fmt.Sprintf("Failed to allocate ext4 image file: %s", truncCombined))
	}

	mkfsResult := system.RunCmdCompat(
		ctx,
		[]string{"mkfs.ext4", "-d", extractDir, "-L", "", finalPath},
		system.DefaultRunCmdOpts(),
	)
	mkfsCombined := string(mkfsResult.StdoutBytes) + string(mkfsResult.StderrBytes)
	if mkfsResult.Err != nil {
		return "", NewImageError(fmt.Sprintf("Failed to create ext4 from squashfs: %s", mkfsCombined))
	}

	slog.Info("Created ext4 from squashfs", "path", finalPath)
	return finalPath, nil
}

func (s *Service) createExt4FromTar(ctx context.Context, tarPath, outputPath, minimumRootfsMib string) error {
	t0 := time.Now()

	tmpDir, err := os.MkdirTemp(infra.GetTempDir(), "tar-extract-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	t1 := time.Now()
	slog.Info("Extracting tar...", "tmp_dir", tmpDir)

	tarResult := system.RunCmdCompat(ctx, []string{"tar", "-xf", tarPath, "-C", tmpDir,
		"--exclude=dev/*", "--no-same-owner", "--no-same-permissions"}, system.DefaultRunCmdOpts())
	tarCombined := string(tarResult.StdoutBytes) + string(tarResult.StderrBytes)
	if tarResult.Err != nil {
		return fmt.Errorf("tar extract failed: %s: %w", tarCombined, tarResult.Err)
	}
	t2 := time.Now()
	slog.Debug("tar extract", "elapsed_seconds", t2.Sub(t1).Seconds())

	system.RunCmdCompat(
		ctx,
		[]string{"chmod", "-R", "u+rwx", tmpDir},
		system.RunCmdOpts{Capture: false, Check: false},
	)
	t3 := time.Now()
	slog.Debug("chmod", "elapsed_seconds", t3.Sub(t2).Seconds())

	duResult := system.RunCmdCompat(
		ctx,
		[]string{"du", "-sb", tmpDir},
		system.RunCmdOpts{Capture: true, Check: false},
	)
	duExitCode := duResult.ExitCode
	duStdout := strings.TrimSpace(duResult.Stdout)
	duStderr := strings.TrimSpace(duResult.Stderr)

	if duExitCode != 0 && duExitCode != 1 {
		if duStderr != "" {
			return NewImageError(fmt.Sprintf("Failed to get directory size: %s", duStderr))
		}
		return NewImageError(fmt.Sprintf("Failed to get directory size (exit %d)", duExitCode))
	}
	fields := strings.Fields(duStdout)
	if len(fields) == 0 {
		return NewImageError("Failed to get directory size")
	}
	actualBytes, _ := strconv.ParseInt(fields[0], 10, 64)
	t4 := time.Now()
	slog.Debug("du", "elapsed_seconds", t4.Sub(t3).Seconds(), "size_bytes", actualBytes)

	var rawSizeMB int
	if minimumRootfsMib == "dynamic" {
		rawSizeMB = calculateMinimumImageSizeMB(actualBytes)
	} else {
		rawSizeMB, _ = strconv.Atoi(minimumRootfsMib)
	}

	slog.Info("Creating ext4 image", "size_mib", rawSizeMB, "path", outputPath)

	// Check available space
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(outputPath), &statfs); err == nil {
		freeBytes := int64(statfs.Bavail) * int64(statfs.Bsize)
		neededBytes := int64(rawSizeMB) * MiB
		if freeBytes < neededBytes {
			return NewImageError(
				fmt.Sprintf(
					"Not enough free space on %s to create the ext4 image: need %d MiB, only %d MiB available. Free up space or set MVM_CACHE_DIR to a larger filesystem.",
					filepath.Dir(outputPath),
					rawSizeMB,
					freeBytes/MiB,
				),
			)
		}
	}

	truncResult := system.RunCmdCompat(
		ctx,
		[]string{"truncate", "-s", fmt.Sprintf("%dM", rawSizeMB), outputPath},
		system.DefaultRunCmdOpts(),
	)
	truncCombined := string(truncResult.StdoutBytes) + string(truncResult.StderrBytes)
	if truncResult.Err != nil {
		return fmt.Errorf("truncate failed: %s: %w", truncCombined, truncResult.Err)
	}
	t5 := time.Now()
	slog.Debug("truncate", "elapsed_seconds", t5.Sub(t4).Seconds())

	mkfsResult := system.RunCmdCompat(
		ctx,
		[]string{"mkfs.ext4", "-d", tmpDir, "-F", outputPath},
		system.DefaultRunCmdOpts(),
	)
	mkfsCombined := string(mkfsResult.StdoutBytes) + string(mkfsResult.StderrBytes)
	if mkfsResult.Err != nil {
		msg := mkfsCombined
		if strings.Contains(msg, "No space left on device") || strings.Contains(msg, "ENOSPC") {
			slog.Warn("Disk full while creating ext4 image",
				"output_dir", filepath.Dir(outputPath),
				"needed_mib", rawSizeMB,
				"hint", "Set MVM_CACHE_DIR to a larger filesystem or free up space.")
		} else {
			slog.Warn("Failed to create ext4 image", "error", msg)
		}
		return NewImageError(fmt.Sprintf("Failed to create ext4 image: %s", msg))
	}
	t6 := time.Now()
	slog.Debug("mkfs.ext4", "elapsed_seconds", t6.Sub(t5).Seconds())

	slog.Info("Created ext4 image", "output", filepath.Base(outputPath), "total_seconds", t6.Sub(t0).Seconds())
	return nil
}

func calculateMinimumImageSizeMB(contentBytes int64) int {
	contentMiB := float64(contentBytes) / float64(MiB)
	calculatedMiB := int(contentMiB * RootfsHeadroom)
	if calculatedMiB < MinRootfsSizeMiB {
		return MinRootfsSizeMiB
	}
	return calculatedMiB
}

// ──────────────────────────────────────────────────────────────────────────────
// SHA256 verification
// ──────────────────────────────────────────────────────────────────────────────

func (s *Service) fetchSHA256FromURL(ctx context.Context, sha256URL, sourceFilename string) (string, error) {
	// Use shared HttpDownload.GetContent instead of raw http.Client.Get — provides
	// retry logic, HTTP caching, and mirror support matching Python's behavior.
	// Python passes timeout=HTTP_TIMEOUT_SHA256_FETCH_S (30s).
	// Python catches HttpDownloadError → returns None (no error).
	content, err := s.dl.GetContent(ctx, download.RequestOpts{
		URL: sha256URL, Timeout: infra.HTTPTimeoutSha256FetchS,
	})
	if err != nil {
		return "", nil // Python catches HttpDownloadError → returns None
	}

	content = strings.TrimSpace(content)
	if sourceFilename == "" {
		parts := strings.Fields(content)
		if len(parts) == 0 {
			return "", nil // Python returns None when parts is empty
		}
		return strings.ToLower(parts[0]), nil
	}

	sourceBasename := filepath.Base(sourceFilename)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineParts := strings.Fields(line)
		if len(lineParts) < 2 {
			continue
		}
		filenameInLine := strings.TrimLeft(lineParts[len(lineParts)-1], "*")
		filenameInLineBasename := filepath.Base(filenameInLine)

		if filenameInLine == sourceFilename ||
			filenameInLine == sourceBasename ||
			filenameInLineBasename == sourceFilename ||
			filenameInLineBasename == sourceBasename {
			return strings.ToLower(lineParts[0]), nil
		}
	}
	return "", nil // Python returns None when not found (not an error)
}

func (s *Service) downloadFile(
	ctx context.Context,
	url, destPath, expectedSHA256 string,
	progress func(int64, int64),
) error {
	allowMissing := expectedSHA256 == ""
	if progress == nil {
		return s.dl.DownloadFile(ctx, url, destPath, expectedSHA256, allowMissing, allowMissing, nil)
	}
	return s.dl.DownloadFile(ctx, url, destPath, expectedSHA256, allowMissing, allowMissing, progress)
}

func (s *Service) resolveFSType(ctx context.Context, imagePath string) (string, error) {
	fsType := system.DetectFilesystemType(ctx, imagePath)
	if fsType != "" {
		return fsType, nil
	}
	ext := filepath.Ext(imagePath)
	if mapped, ok := infra.ExtToFSType[ext]; ok {
		return mapped, nil
	}
	return "", NewImageError(fmt.Sprintf(
		"Could not detect filesystem type for %s. Ensure the image has a valid filesystem.",
		imagePath,
	))
}

// ──────────────────────────────────────────────────────────────────────────────
// Other helpers
// ──────────────────────────────────────────────────────────────────────────────

func (s *Service) getTemplateVariables(spec *model.ImageSpec, ciVersion string) map[string]string {
	return map[string]string{
		"ci_version":     ciVersion,
		"arch":           spec.Arch,
		"image_type":     spec.Type,
		"version":        spec.Version,
		"image_version":  spec.Version,
		"ubuntu_version": spec.Version,
	}
}

// resolveSourceTemplate resolves source URL by fetching and parsing CI image list.
// Uses the shared HttpDownload.GetContent to benefit from retry + caching.
// Matches Python's _resolve_source_template() exactly — sorts keys alphabetically
// before picking the last (highest) one (C05).
func (s *Service) resolveSourceTemplate(
	ctx context.Context,
	spec *model.ImageSpec,
	templateVars map[string]string,
) (string, error) {
	listURLTmpl := ""
	if spec.ListURLTemplate != nil {
		listURLTmpl = *spec.ListURLTemplate
	}
	if listURLTmpl == "" {
		return "", NewImageError(
			fmt.Sprintf("Missing 'list_url_template' in images.yaml for %s:%s", spec.Type, spec.Version),
		)
	}

	listURL, _ := infra.RenderTemplate(listURLTmpl, templateVars)

	// Use shared HttpDownload.GetContent instead of raw http.Client.Get — provides
	// retry logic, HTTP caching, and mirror support matching Python's behavior.
	xmlContent, err := s.dl.GetContent(ctx, download.RequestOpts{
		URL: listURL, Timeout: 30,
	})
	if err != nil {
		return "", NewImageError("Failed to list Firecracker CI ubuntu images")
	}

	ciVersion := templateVars["ci_version"]
	arch := templateVars["arch"]
	pattern := fmt.Sprintf(`<Key>(firecracker-ci/%s/%s/ubuntu-[0-9.]+\.squashfs)</Key>`,
		regexp.QuoteMeta(ciVersion), regexp.QuoteMeta(arch))
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(xmlContent, -1)

	if len(matches) == 0 {
		return "", NewImageError(fmt.Sprintf("No ubuntu squashfs found for CI version %s / arch %s", ciVersion, arch))
	}

	keys := make([]string, len(matches))
	for i, m := range matches {
		keys[i] = m[1]
	}

	// C05: Sort keys alphabetically before picking the last (highest) one
	sort.Strings(keys)
	chosenKey := keys[len(keys)-1]

	sourceResolved, _ := infra.RenderTemplate(spec.Source, templateVars)
	parsedURL, urlErr := url.Parse(sourceResolved)
	if urlErr != nil {
		return "", fmt.Errorf("parse URL: %w", urlErr)
	}
	pathParts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	bucket := ""
	if len(pathParts) > 0 {
		bucket = pathParts[0]
	}
	base := fmt.Sprintf("%s://%s/%s", parsedURL.Scheme, parsedURL.Host, bucket)
	return fmt.Sprintf("%s/%s", base, chosenKey), nil
}

// ResolveVersion resolves a version spec (latest, partial, or exact) to a concrete version.
func (s *Service) ResolveVersion(ctx context.Context, imageType string, versionSpec string, arch string, ciVersion string, configs []download.ResolverConfig) (string, error) {
	spec, err := version.ParseSpec(versionSpec)
	if err != nil {
		return "", NewImageError(fmt.Sprintf("Invalid version spec %q: %s", versionSpec, err))
	}

	// Exact version — no resolution needed
	if !spec.IsPartial() {
		return strings.TrimPrefix(versionSpec, "v"), nil
	}

	// Filter configs to matching type
	var matched []download.ResolverConfig
	for _, cfg := range configs {
		if imageType == "" || cfg.Type == imageType {
			matched = append(matched, cfg)
		}
	}
	if len(matched) == 0 {
		return "", NewImageError(fmt.Sprintf("No image types matched %q", imageType))
	}

	versionMap := ResolveVersions(ctx, matched, arch, 0, ciVersion)
	var allVersions []string
	for _, versions := range versionMap {
		for _, v := range versions {
			allVersions = append(allVersions, v.Version)
		}
	}
	if len(allVersions) == 0 {
		return "", NewImageError(fmt.Sprintf("No versions available for image type %q", imageType))
	}

	resolved, err := version.Resolve(allVersions, spec)
	if err != nil {
		return "", NewImageError(fmt.Sprintf("Cannot resolve version %q: %s", versionSpec, err))
	}
	return resolved, nil
}
