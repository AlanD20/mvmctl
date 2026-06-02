package kernel

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mvmctl/internal/assets"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
)

// ── Service-layer types (moved from model.go per Go porting spec) ──

// ParsedKernelFilename corresponds to Python's ParsedKernelFilename.
type ParsedKernelFilename struct {
	BaseName string
	Version  string
	Arch     string
}

// KernelPipelineResult corresponds to Python's KernelPipelineResult.
type KernelPipelineResult struct {
	ConfigResult *KernelConfigResult
	BuildResult  *KernelBuildResult
	Success      bool
}

// KernelConfigResult corresponds to Python's KernelConfigResult.
type KernelConfigResult struct {
	Success      bool
	Warnings     []string
	InfoMessages []string
}

// KernelBuildResult corresponds to Python's KernelBuildResult.
type KernelBuildResult struct {
	Success      bool
	Warnings     []string
	InfoMessages []string
}

// resolvedKernelPath returns the absolute filesystem path for a kernel item.
func resolvedKernelPath(k *model.KernelItem) string {
	if filepath.IsAbs(k.Path) {
		return k.Path
	}
	return filepath.Join(infra.GetKernelsDir(), k.Path)
}

// Service provides stateless kernel operations: loading specs, downloading,
// building from source, and managing kernel configuration.
// Matches Python's KernelService (1512 lines).
type Service struct {
	repo     Repository
	cacheDir string
	dl       *download.Downloader
	resolver *download.HttpDirVersionResolver
	specs    map[string]*model.KernelSpec // cached loaded specs
}

func NewService(repo Repository, cacheDir string) *Service {
	return &Service{
		repo:     repo,
		cacheDir: cacheDir,
		dl:       download.New(),
		resolver: download.NewHttpDirVersionResolver(),
	}
}

// Repo returns the underlying repository for use by the API layer.
func (s *Service) Repo() Repository {
	return s.repo
}

// ── Firecracker Kernel Download ──────────────────────────────────────────

// FetchFirecrackerKernel downloads a pre-built Firecracker CI vmlinux.
// Matches Python's KernelService.fetch_firecracker_kernel().
func (s *Service) FetchFirecrackerKernel(
	ctx context.Context,
	spec *model.KernelSpec,
	ciVersion, arch, outputDir string,
	progressCallback func(currentBytes, totalBytes int64),
) (*model.KernelPullResult, error) {
	if spec.ListURLTemplate == nil || *spec.ListURLTemplate == "" {
		return nil, NewKernelErrorf(
			"Missing 'list_url_template' in kernels.yaml for %s", spec.Name)
	}

	templateVars := map[string]string{
		"ci_version": ciVersion,
		"arch":       arch,
		"version":    spec.Version,
	}
	listURL := renderTemplate(*spec.ListURLTemplate, templateVars)

	// Fetch S3 XML listing
	xmlContent, err := s.dl.GetBody(ctx, listURL)
	if err != nil {
		return nil, NewKernelErrorf("Failed to list CI kernels: %s", err)
	}

	// Parse S3 keys for vmlinux files matching ci_version/arch
	pattern := fmt.Sprintf(`<Key>(firecracker-ci/%s/%s/vmlinux-[\d.]+)</Key>`,
		regexp.QuoteMeta(ciVersion), regexp.QuoteMeta(arch))
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(string(xmlContent), -1)

	if len(matches) == 0 {
		return nil, NewKernelErrorf(
			"No vmlinux found for Firecracker CI version %s / arch %s", ciVersion, arch)
	}

	// Extract keys and sort by version descending
	var keys []string
	for _, m := range matches {
		keys = append(keys, m[1])
	}
	sort.Slice(keys, func(i, j int) bool {
		vi := extractVersionFromKey(keys[i])
		vj := extractVersionFromKey(keys[j])
		return version.SemverGreater(vi, vj)
	})

	chosenKey := keys[0]
	kernelVersion := extractVersionFromKey(chosenKey)

	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s-%s", spec.OutputName, kernelVersion, arch))

	// Check if already cached
	if _, err := os.Stat(outputPath); err == nil {
		slog.Info("Firecracker CI kernel already cached", "path", outputPath)
		return &model.KernelPullResult{
			Path:         outputPath,
			Version:      kernelVersion,
			Arch:         arch,
			KernelType:   infra.KernelTypeFirecracker,
			Warnings:     []string{},
			InfoMessages: []string{fmt.Sprintf("Firecracker kernel ready: %s", outputPath)},
		}, nil
	}

	// Compute intentional_no_checksum before sha256_url rendering (matching Python)
	intentionalNoChecksum := spec.SHA256 == "" && spec.SHA256URL == ""

	templateVars["kernel_version"] = kernelVersion
	downloadURL := fmt.Sprintf("%s/%s", strings.TrimRight(spec.Source, "/"), chosenKey)
	sha256URL := ""
	if spec.SHA256URL != "" {
		if r, err := infra.RenderTemplate(spec.SHA256URL, templateVars); err == nil {
			sha256URL = r
		}
	}
	if sha256URL == "" && !intentionalNoChecksum {
		sha256URL = downloadURL + ".sha256"
	}
	expectedSHA256 := ""
	if sha256URL != "" {
		if content, err := s.dl.GetBody(ctx, sha256URL); err == nil {
			parts := strings.Fields(strings.TrimSpace(string(content)))
			if len(parts) > 0 {
				expectedSHA256 = strings.ToLower(parts[0])
				slog.Debug("Fetched CI kernel checksum", "sha256", expectedSHA256)
			}
		} else {
			// Python: logger.debug level — silently skip
		}
	}
	if expectedSHA256 == "" && !intentionalNoChecksum {
		return nil, NewKernelErrorf("Checksum required for Firecracker CI kernel download: %s", downloadURL)
	}

	// Download kernel
	slog.Info("Downloading Firecracker CI kernel", "url", downloadURL)
	if err := s.dl.DownloadFile(
		ctx,
		downloadURL,
		outputPath,
		expectedSHA256,
		true,
		true,
		progressCallback,
	); err != nil {
		return nil, NewKernelErrorf("Failed to download Firecracker CI kernel: %s", err)
	}
	os.Chmod(outputPath, 0755)

	slog.Info("Firecracker CI kernel saved", "path", outputPath)
	return &model.KernelPullResult{
		Path:         outputPath,
		Version:      kernelVersion,
		Arch:         arch,
		KernelType:   infra.KernelTypeFirecracker,
		Warnings:     []string{},
		InfoMessages: []string{fmt.Sprintf("Firecracker kernel ready: %s", outputPath)},
	}, nil
}

// ── Official Kernel Build Pipeline ──────────────────────────────────────

// BuildOfficialKernel builds an official kernel from source.
// Matches Python's KernelService.build_official_kernel() with all parameters:
// keep_build_dir, clean_build, kernel_config, progress_callback, on_status.
func (s *Service) BuildOfficialKernel(
	ctx context.Context,
	spec *model.KernelSpec,
	arch, outputDir string,
	jobs int,
	keepBuildDir bool,
	useCache bool,
	userConfigPath *string,
	progressCallback func(currentBytes, totalBytes int64),
	onStatus func(string),
) (*model.KernelPullResult, error) {
	if err := checkBuildDependencies(ctx); err != nil {
		return nil, err
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s-%s", spec.OutputName, spec.Version, arch))

	// Use spec properties directly (matching Python's build_official_kernel which
	// reads spec.version, spec.source, spec.sha256 internally).
	sourceURL := spec.Source
	sha256 := spec.SHA256

	buildResult, err := s.buildFromSource(
		ctx,
		spec,
		spec.Version,
		sourceURL,
		outputPath,
		jobs,
		arch,
		sha256,
		keepBuildDir,
		userConfigPath,
		useCache,
		progressCallback,
		onStatus,
	)
	if err != nil {
		return nil, err
	}

	var warnings []string
	var infoMessages []string
	if buildResult.ConfigResult != nil {
		warnings = append(warnings, buildResult.ConfigResult.Warnings...)
		infoMessages = append(infoMessages, buildResult.ConfigResult.InfoMessages...)
	}
	if buildResult.BuildResult != nil {
		warnings = append(warnings, buildResult.BuildResult.Warnings...)
		infoMessages = append(infoMessages, buildResult.BuildResult.InfoMessages...)
	}
	infoMessages = append(infoMessages, fmt.Sprintf("Kernel built: %s", outputPath))

	return &model.KernelPullResult{
		Path:         outputPath,
		Version:      spec.Version,
		Arch:         arch,
		KernelType:   infra.KernelTypeOfficial,
		Warnings:     warnings,
		InfoMessages: infoMessages,
	}, nil
}

// buildFromSource orchestrates download → extract → configure → build.
// Matches Python's KernelService.build_from_source().
func (s *Service) buildFromSource(
	ctx context.Context,
	spec *model.KernelSpec,
	version string,
	sourceURL string,
	outputPath string,
	jobs int,
	arch string,
	sha256 string,
	keepBuildDir bool,
	userConfigPath *string,
	useCache bool,
	progressCallback func(currentBytes, totalBytes int64),
	onStatus func(string),
) (*KernelPipelineResult, error) {
	// Python: build_dir = Path(spec.build_dir)
	// If spec.build_dir is empty string, Path("") resolves to current directory.
	buildDir := spec.BuildDir
	if buildDir == "" {
		buildDir = "."
	}
	configHash := s.computeConfigHash(spec, version, userConfigPath)
	cacheKey := fmt.Sprintf("%s-%s", version, configHash)
	cacheMarker := filepath.Join(filepath.Dir(buildDir), fmt.Sprintf("kernel-cache-%s.marker", cacheKey))
	cachedKernelPath := filepath.Join(filepath.Dir(buildDir), fmt.Sprintf("kernel-cache-%s.vmlinux", cacheKey))

	// 1. Cache hit?
	if s.tryCacheHit(ctx, outputPath, cacheMarker, cachedKernelPath, useCache) {
		return &KernelPipelineResult{
			ConfigResult: nil,
			BuildResult:  nil,
			Success:      true,
		}, nil
	}

	// 2. Resolve source URL and checksum
	resolvedSourceURL, resolvedSHA256, err := s.resolveSourceAndChecksum(ctx, spec, version, arch, sha256, onStatus)
	if err != nil {
		return nil, err
	}

	tarball := filepath.Join(buildDir, fmt.Sprintf("linux-%s.tar.xz", version))
	kernelSrcDir := filepath.Join(buildDir, fmt.Sprintf("linux-%s-%s", version, arch))
	var configResult *KernelConfigResult
	var buildResult *KernelBuildResult
	var pipelineErr error

	// Use closure so cleanup runs after completion (matching Python's try/except/else pattern)
	func() {
		// 3. Download + extract
		if _, err := os.Stat(tarball); os.IsNotExist(err) {
			os.MkdirAll(filepath.Dir(tarball), infra.DirPerm)
			slog.Info("Downloading kernel", "url", resolvedSourceURL)
			if err := s.dl.DownloadFile(
				ctx,
				resolvedSourceURL,
				tarball,
				resolvedSHA256,
				true,
				true,
				progressCallback,
			); err != nil {
				pipelineErr = NewKernelErrorf("Download failed: %s", err)
				return
			}
		} else {
			slog.Debug("Using cached tarball", "path", tarball)
		}
		if _, err := os.Stat(kernelSrcDir); os.IsNotExist(err) {
			extracted, err := s.ExtractKernelTarball(ctx, tarball, buildDir)
			if err != nil {
				pipelineErr = err
				return
			}
			if extracted != kernelSrcDir {
				if err := os.Rename(extracted, kernelSrcDir); err != nil {
					pipelineErr = NewKernelErrorf("Failed to rename kernel source directory: %s", err)
					return
				}
			}
		} else {
			slog.Debug("Using existing source", "path", kernelSrcDir)
		}
		// 4. Prepare kernel config
		var configErr error
		configResult, configErr = s.PrepareKernelConfig(ctx, kernelSrcDir, spec, arch, jobs, userConfigPath, onStatus)
		if configErr != nil {
			pipelineErr = configErr
			return
		}
		// 5. Build vmlinux
		var buildErr error
		buildResult, buildErr = s.RunMakeVmlinux(ctx, kernelSrcDir, outputPath, jobs)
		if buildErr != nil {
			pipelineErr = buildErr
			return
		}
		// 6. Cache output — using shutil.copy2 equivalent (preserving metadata)
		if useCache {
			os.MkdirAll(filepath.Dir(cachedKernelPath), infra.DirPerm)
			if err := infra.CopyPreservingMetadata(outputPath, cachedKernelPath); err != nil {
				slog.Warn("Failed to cache kernel build", "error", err)
			}
			os.WriteFile(cacheMarker, []byte(cacheKey), 0644)
		}
	}()

	// Python: except block → exception propagates (pipelineErr is non-nil).
	// Python: else block → cleanup runs only on success.
	if pipelineErr == nil && !keepBuildDir {
		if err := os.RemoveAll(buildDir); err != nil {
			slog.Warn("Failed to clean up build directory", "dir", buildDir, "error", err)
		} else {
			slog.Debug("Build directory cleaned up", "dir", buildDir)
		}
	} else if pipelineErr == nil && keepBuildDir {
		slog.Debug("Build directory kept at", "dir", buildDir)
	}

	if pipelineErr != nil {
		return &KernelPipelineResult{
			ConfigResult: configResult,
			BuildResult:  buildResult,
			Success:      false,
		}, pipelineErr
	}

	return &KernelPipelineResult{
		ConfigResult: configResult,
		BuildResult:  buildResult,
		Success:      true,
	}, nil
}

// resolveSourceAndChecksum resolves source URL template vars and fetches SHA256 if needed.
// Matches Python's KernelService._resolve_source_and_checksum().
// Returns an error if a checksum is required but cannot be resolved.
func (s *Service) resolveSourceAndChecksum(
	ctx context.Context,
	spec *model.KernelSpec,
	version, arch string,
	sha256 string,
	onStatus func(string),
) (string, string, error) {
	major := ""
	if m, _, found := strings.Cut(version, "."); found {
		major = m
	}
	templateVars := map[string]string{
		"version":        version,
		"series":         major,
		"kernel_version": version,
		"ci_version":     version,
		"arch":           arch,
	}
	resolvedSourceURL := renderTemplate(spec.Source, templateVars)

	intentionalNoChecksum := spec.SHA256 == "" && spec.SHA256URL == ""

	resolvedSHA256 := sha256

	if resolvedSHA256 == "" && !intentionalNoChecksum {
		resolvedSHA256URL := ""
		if spec.SHA256URL != "" {
			if r, err := infra.RenderTemplate(spec.SHA256URL, templateVars); err == nil {
				resolvedSHA256URL = r
			}
		}
		if resolvedSHA256URL != "" {
			filename := fmt.Sprintf("linux-%s.tar.xz", version)
			if sha, err := s.fetchSHA256FromURL(ctx, resolvedSHA256URL, filename); err == nil && sha != "" {
				resolvedSHA256 = sha
			}
		}
	}

	if resolvedSHA256 == "" && !intentionalNoChecksum {
		return resolvedSourceURL, "", NewKernelErrorf(
			"Checksum required for kernel source download: %s",
			resolvedSourceURL,
		)
	}

	return resolvedSourceURL, resolvedSHA256, nil
}

// fetchSHA256FromURL fetches a SHA256 checksum from a URL, optionally matching a filename.
// Matches Python's KernelService.fetch_kernel_sha256_from_url().
func (s *Service) fetchSHA256FromURL(ctx context.Context, sha256URL, filename string) (string, error) {
	content, err := s.dl.GetBody(ctx, sha256URL)
	if err != nil {
		return "", fmt.Errorf("fetch sha256: %w", err)
	}

	text := strings.TrimSpace(string(content))
	if filename == "" {
		// Per-file sidecar format: "<hash>  <filename>" — return first token
		parts := strings.Fields(text)
		if len(parts) > 0 {
			return strings.ToLower(parts[0]), nil
		}
		return "", nil
	}

	// Aggregated SHA256SUMS format
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") || strings.HasPrefix(line, "Hash:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == filename {
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", nil
}

// extractVersionFromKey extracts the version from a Firecracker S3 key.
// e.g. "firecracker-ci/v1.15/x86_64/vmlinux-6.1.155" → "6.1.155"
// TODO(verdict#33): belongs in infra/version or similar shared utility
func extractVersionFromKey(key string) string {
	idx := strings.LastIndex(key, "/vmlinux-")
	if idx < 0 {
		return key
	}
	return key[idx+len("/vmlinux-"):]
}

// ── Kernel Listing ──────────────────────────────────────────────────────

// ListAll returns all kernels, optionally verifying is_present against the filesystem.
// Matches Python's KernelService.list_all(verify: bool = True).
func (s *Service) ListAll(ctx context.Context, verify bool) ([]*model.KernelItem, error) {
	kernels, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return kernels, nil
	}
	// Verify filesystem presence (matching Python's list_all verification)
	var missingIDs []string
	for _, kernel := range kernels {
		rp := resolvedKernelPath(kernel)
		if _, err := os.Stat(rp); os.IsNotExist(err) {
			missingIDs = append(missingIDs, kernel.ID)
		}
	}
	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		// Re-fetch after update
		return s.repo.ListAll(ctx)
	}
	return kernels, nil
}

// List returns all kernels with filesystem verification (verify=True).
// Matches Python's KernelService.list_all() (default verify=True).
func (s *Service) List(ctx context.Context) ([]*model.KernelItem, error) {
	return s.ListAll(ctx, true)
}

// ── Kernel Remove ───────────────────────────────────────────────────────

// Remove removes a kernel, deleting its file and DB record.
// Matches Python's KernelService.remove(self, kernel, *, force) -> KernelItem.
func (s *Service) Remove(ctx context.Context, kernel *model.KernelItem, force bool) (*model.KernelItem, error) {
	vms := kernel.VMs
	hasVMs := len(vms) > 0

	if hasVMs && !force {
		var names []string
		for _, vm := range vms {
			names = append(names, vm.Name)
		}
		return nil, NewKernelErrorf("Kernel referenced by VMs: %s", strings.Join(names, ", "))
	}

	// Delete file from disk (using ResolvedPath to match Python's kernel.resolved_path)
	rp := resolvedKernelPath(kernel)
	if _, statErr := os.Stat(rp); statErr == nil {
		if err := os.Remove(rp); err != nil {
			slog.Warn("Failed to remove kernel file", "error", err)
		}
	}

	// Hard delete if no VMs, soft delete if VMs exist (with force)
	if hasVMs {
		return kernel, s.repo.SoftDelete(ctx, kernel.ID)
	}
	return kernel, s.repo.Delete(ctx, kernel.ID)
}

// RemoveMany removes multiple kernels in batch.
// Matches Python's KernelService.remove_many().
func (s *Service) RemoveMany(
	ctx context.Context,
	kernels []*model.KernelItem,
	force bool,
) ([]*model.KernelItem, error) {
	var deleted []*model.KernelItem
	for _, kernel := range kernels {
		k, err := s.Remove(ctx, kernel, force)
		if err != nil {
			return nil, err
		}
		deleted = append(deleted, k)
	}
	return deleted, nil
}

// ── Spec Loading ────────────────────────────────────────────────────────

// LoadSpecs loads and parses all kernel specs from the embedded kernels.yaml.
// Matches Python's KernelService._load_specs().
func (s *Service) LoadSpecs() (map[string]*model.KernelSpec, error) {
	if s.specs != nil {
		return s.specs, nil
	}

	var raw map[string]any
	if err := assets.ReadYAML("kernels.yaml", &raw); err != nil {
		return nil, NewKernelErrorf("Failed to load kernels.yaml: %s", err)
	}

	specs := make(map[string]*model.KernelSpec)
	for name, rawAny := range raw {
		rawMap, ok := rawAny.(map[string]any)
		if !ok {
			return nil, NewKernelError("Invalid kernels.yaml entry format")
		}

		features := make(map[string]model.KernelFeature)
		if featsRaw, ok := rawMap["features"].(map[string]any); ok {
			for fname, fr := range featsRaw {
				frMap, ok := fr.(map[string]any)
				if !ok {
					continue
				}
				features[fname] = model.KernelFeature{
					Desc:     requireStr(frMap, "desc"),
					Configs:  requireStrList(frMap, "configs"),
					Requires: requireStrList(frMap, "requires"),
				}
			}
		}

		optsRaw, _ := rawMap["options"].(map[string]any)

		spec := &model.KernelSpec{
			Name:              name,
			KernelType:        requireStr(rawMap, "type"),
			Version:           requireStr(rawMap, "version"),
			Source:            requireStr(rawMap, "source"),
			OutputName:        requireStr(rawMap, "output_name"),
			BuildDir:          requireStr(rawMap, "build_dir"),
			ListURLTemplate:   optionalStrPtr(rawMap, "list_url_template"),
			ConfigURLTemplate: optionalStrPtr(rawMap, "config_url_template"),
			SHA256:            getStringOption(rawMap, "sha256"),
			SHA256URL:         getStringOption(rawMap, "sha256_url"),
			ParallelJobs:      optionalIntPtr(rawMap, "parallel_jobs"),
			ConfigFragments:   requireStrList(rawMap, "config_fragments"),
			EnabledConfigs:    requireStrList(rawMap, "enabled_configs"),
			DisabledConfigs:   requireStrList(rawMap, "disabled_configs"),
			RequiredSettings:  requireStrList(rawMap, "required_settings"),
			SetValConfigs:     parseSetValList(rawMap, "set_val_configs"),
			Resolver:          optionalStrPtr(rawMap, "resolver"),
			VersionsURL:       optionalStrPtr(rawMap, "versions_url"),
			FilePattern:       optionalStrFromPtr(rawMap, "options", "file_pattern"),
			FileSuffix:        optionalStrFromPtr(rawMap, "options", "file_suffix"),
			Options:           optsRaw,
			Features:          features,
		}
		specs[name] = spec
	}

	s.specs = specs
	return specs, nil
}

// LoadKernelTypesConfig loads the kernel types configuration from the embedded kernels.yaml
// and returns it as a list of structured config dicts.
// Matches Python's KernelService.load_kernel_types_config().
func (s *Service) LoadKernelTypesConfig() ([]map[string]any, error) {
	specs, err := s.LoadSpecs()
	if err != nil {
		return nil, err
	}
	var configs []map[string]any
	for _, spec := range specs {
		format := "tar.xz"
		if spec.KernelType != "official" {
			format = "vmlinux"
		}
		config := map[string]any{
			"type":         spec.KernelType,
			"resolver":     spec.Resolver,
			"version":      spec.Version,
			"source":       spec.Source,
			"versions_url": spec.VersionsURL,
			"format":       format,
			"name":         spec.Name,
		}
		if spec.ListURLTemplate != nil {
			config["list_url_template"] = *spec.ListURLTemplate
		}
		if spec.SHA256URL != "" {
			config["sha256_url"] = spec.SHA256URL
		}
		if spec.Options != nil {
			config["options"] = spec.Options
		}
		if spec.Resolver != nil && *spec.Resolver == "http-dir" {
			opts, _ := config["options"].(map[string]any)
			if opts == nil {
				opts = make(map[string]any)
				config["options"] = opts
			}
			discoveries := []string{}
			if spec.Options != nil {
				if raw, ok := spec.Options["version_discoveries"].([]any); ok {
					for _, d := range raw {
						if s, ok := d.(string); ok {
							discoveries = append(discoveries, s)
						}
					}
				}
			}
			opts["version_discoveries"] = discoveries
			filePattern := "linux-"
			if spec.FilePattern != nil {
				filePattern = *spec.FilePattern
			}
			opts["file_pattern"] = filePattern
			fileSuffix := ".tar.xz"
			if spec.FileSuffix != nil {
				fileSuffix = *spec.FileSuffix
			}
			opts["file_suffix"] = fileSuffix
		} else if spec.Resolver != nil && *spec.Resolver == "firecracker-s3" {
			opts, _ := config["options"].(map[string]any)
			if opts == nil {
				opts = make(map[string]any)
				config["options"] = opts
			}
			s3Pattern := "vmlinux-([\\d.]+)"
			if spec.Options != nil {
				if p, ok := spec.Options["s3_version_pattern"].(string); ok && p != "" {
					s3Pattern = p
				}
			}
			opts["s3_version_pattern"] = s3Pattern
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// GetSpecsFor returns kernel specs filtered by criteria.
// Matches Python's KernelService.get_specs_for().
func (s *Service) GetSpecsFor(names []string, kernelType, version string) ([]*model.KernelSpec, error) {
	allSpecs, err := s.LoadSpecs()
	if err != nil {
		return nil, err
	}

	// Python: if names is not None and kernel_type is None and version is None:
	// Python treats empty names list as entering fast path (returns empty results).
	// Go matches this: names != nil enters fast path even if len==0.
	if names != nil && kernelType == "" && version == "" {
		var results []*model.KernelSpec
		var missing []string
		for _, n := range names {
			spec, ok := allSpecs[n]
			if !ok {
				missing = append(missing, n)
				continue
			}
			results = append(results, spec)
		}
		if len(missing) > 0 {
			avail := make([]string, 0, len(allSpecs))
			for k := range allSpecs {
				avail = append(avail, k)
			}
			return nil, NewKernelErrorf(
				"Kernel spec(s) not found: %s. Available: %s",
				strings.Join(missing, ", "), strings.Join(avail, ", "))
		}
		return results, nil
	}

	var filtered []*model.KernelSpec
	nameSet := makeSet(names)
	for _, spec := range allSpecs {
		if kernelType != "" && spec.KernelType != kernelType {
			continue
		}
		// Python: if version is not None and spec.version != version:
		// Empty string "" is treated as a valid filter in Python.
		// In Go, version != "" skips filter for nil/empty version.
		if version != "" && spec.Version != version {
			resolver := ""
			if spec.Resolver != nil {
				resolver = *spec.Resolver
			}
			if resolver == "http-dir" || resolver == "firecracker-s3" {
				specCopy := *spec
				specCopy.Version = version
				spec = &specCopy
			} else {
				continue
			}
		}
		if len(nameSet) > 0 && !nameSet[spec.Name] {
			continue
		}
		filtered = append(filtered, spec)
	}
	return filtered, nil
}

// TODO(verdict#33): belongs in infra/slices or similar shared utility
func makeSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// ── Build Pipeline ──────────────────────────────────────────────────────

// PrepareKernelConfig configures a kernel with Firecracker settings.
// Matches Python's KernelService.prepare_kernel_config() including user_config_path parameter.
func (s *Service) PrepareKernelConfig(
	ctx context.Context,
	kernelDir string,
	spec *model.KernelSpec,
	arch string,
	jobs int,
	userConfigPath *string,
	onStatus func(string),
) (*KernelConfigResult, error) {
	var warnings []string
	var infoMessages []string
	version := spec.Version
	majorMinor := majorMinorFromVersion(version)
	templateVars := map[string]string{
		"major_minor":    majorMinor,
		"version":        majorMinor,
		"kernel_version": version,
		"ci_version":     version,
		"arch":           arch,
	}

	// Download Firecracker config and apply fragments (combined try/except matching Python)
	// Python: except KernelError — only catches KernelError, not any other error.
	configErr := func() error {
		if err := s.downloadFCConfig(ctx, kernelDir, spec, arch, templateVars); err != nil {
			return err
		}
		if len(spec.ConfigFragments) > 0 {
			if onStatus != nil {
				onStatus("Applying kernel config fragments...")
			}
			if err := s.applyConfigFragments(ctx, kernelDir, spec.ConfigFragments, templateVars, onStatus); err != nil {
				return err
			}
		}
		return nil
	}()
	if configErr != nil {
		// Python: except KernelError — only catch kernel errors, let other errors propagate.
		// Check if this is a KernelError (our domain error type).
		var de *errs.DomainError
		if errors.As(configErr, &de) &&
			(de.Code == errs.CodeKernelBuildFailed || de.Code == errs.CodeKernelConfigFailed) {
			if onStatus != nil {
				onStatus("Using defconfig instead...")
			}
			slog.Info("Using defconfig instead")
			rc, _, _ := runMake(ctx, kernelDir, "defconfig", jobs)
			if rc != 0 {
				return nil, NewKernelError("defconfig failed")
			}
		} else {
			// Not a KernelError — let it propagate (matching Python behavior)
			return nil, configErr
		}
	}

	// First olddefconfig sync (matching Python order)
	if onStatus != nil {
		onStatus("Synchronizing kernel config...")
	}
	slog.Debug("Synchronizing config")
	if rc, _, _ := runMake(ctx, kernelDir, "olddefconfig", jobs); rc != 0 {
		return nil, NewKernelError("olddefconfig failed")
	}

	// Apply enabled/disabled/set-val options via scripts/config
	configScriptPath := filepath.Join(kernelDir, "scripts", "config")
	if spec.EnabledConfigs != nil && len(spec.EnabledConfigs) > 0 {
		if onStatus != nil {
			onStatus(fmt.Sprintf("Enabling %d kernel options...", len(spec.EnabledConfigs)))
		}
		slog.Debug("Applying kernel options from kernels.yaml")
		for _, opt := range spec.EnabledConfigs {
			runConfigScript(ctx, configScriptPath, kernelDir, "--enable", opt)
		}
	}
	if spec.DisabledConfigs != nil && len(spec.DisabledConfigs) > 0 {
		if onStatus != nil {
			onStatus(fmt.Sprintf("Disabling %d kernel options...", len(spec.DisabledConfigs)))
		}
		slog.Debug("Applying disabled kernel options")
		for _, opt := range spec.DisabledConfigs {
			runConfigScript(ctx, configScriptPath, kernelDir, "--disable", opt)
		}
	}
	if spec.SetValConfigs != nil && len(spec.SetValConfigs) > 0 {
		if onStatus != nil {
			onStatus(fmt.Sprintf("Setting %d kernel options...", len(spec.SetValConfigs)))
		}
		slog.Debug("Applying set-val kernel options")
		for _, kv := range spec.SetValConfigs {
			runConfigScript(ctx, configScriptPath, kernelDir, "--set-val", kv[0], kv[1])
		}
	}

	// Resolve dependencies after options
	if onStatus != nil {
		onStatus("Resolving config dependencies...")
	}
	slog.Debug("Resolving dependencies")
	if rc, _, _ := runMake(ctx, kernelDir, "olddefconfig", jobs); rc != 0 {
		return nil, NewKernelError("olddefconfig failed after enabling options")
	}

	// Apply user config fragment if provided.
	// Python: if user_config_path and user_config_path.exists():
	//         user_content = user_config_path.read_text(encoding="utf-8")
	//         cls._merge_config_lines(user_content, config_path)
	if userConfigPath != nil && *userConfigPath != "" {
		if _, statErr := os.Stat(*userConfigPath); statErr == nil {
			if onStatus != nil {
				onStatus(fmt.Sprintf("Applying user config fragment: %s", *userConfigPath))
			}
			slog.Info("Applying user config fragment", "path", *userConfigPath)
			configPath := filepath.Join(kernelDir, ".config")
			userData, err := os.ReadFile(*userConfigPath)
			if err != nil {
				return nil, NewKernelErrorf("Failed to read user config fragment %s: %s", *userConfigPath, err)
			}
			mergeConfigLines(string(userData), configPath)
			if onStatus != nil {
				onStatus("Resolving dependencies after user config...")
			}
			slog.Debug("Resolving dependencies after user config")
			if rc, _, _ := runMake(ctx, kernelDir, "olddefconfig", jobs); rc != 0 {
				return nil, NewKernelError("olddefconfig failed after user config")
			}
		}
	}

	// Verify required settings
	if onStatus != nil {
		onStatus("Verifying kernel configuration...")
	}
	slog.Debug("Verifying configuration")
	var missingSettings []string
	for _, setting := range spec.RequiredSettings {
		if err := verifyConfigSetting(kernelDir, setting); err != nil {
			missingSettings = append(missingSettings, setting)
		} else {
			slog.Debug("Required setting", "setting", setting)
		}
	}
	if len(missingSettings) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("Required kernel settings missing: %s", strings.Join(missingSettings, ", ")))
		return &KernelConfigResult{
			Success:      false,
			Warnings:     warnings,
			InfoMessages: infoMessages,
		}, nil
	}

	return &KernelConfigResult{
		Success:      true,
		Warnings:     warnings,
		InfoMessages: infoMessages,
	}, nil
}

// buildLogPattern matches build warnings in kernel build output.
// Matches Python's _BUILD_LOG_PATTERNS regex.
var buildLogPattern = regexp.MustCompile(`(?i)(warning|error|cannot find|undefined reference|fatal|note:)`)

// RunMakeVmlinux builds the kernel vmlinux binary using a build log file.
// Matches Python's KernelService.run_make_vmlinux().
func (s *Service) RunMakeVmlinux(
	ctx context.Context,
	kernelDir, outputPath string,
	jobs int,
) (*KernelBuildResult, error) {
	warnings := []string{"Building kernel... (this may take 10-30 minutes)"}
	var infoMessages []string
	slog.Info("Building vmlinux", "jobs", jobs)
	slog.Info("This may take 10-30 minutes")

	buildLogPath := outputPath + ".build.log"
	os.MkdirAll(filepath.Dir(buildLogPath), infra.DirPerm)

	result := system.RunCmdCompat(ctx, []string{"make", "vmlinux", fmt.Sprintf("-j%d", jobs)}, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   true,
	})

	// Write captured output to build log
	logData := result.Stdout
	if result.Stderr != "" {
		logData += "\n" + result.Stderr
	}
	os.WriteFile(buildLogPath, []byte(logData), 0644)

	if result.Err != nil {
		// Matching Python's OSError handler:
		//   except OSError as e:
		//       raise KernelError("Kernel build failed: unable to execute make") from e
		// RunCmdCompat wraps exec.ErrNotFound as "Command not found: make".
		if strings.Contains(result.Err.Error(), "Command not found") {
			return nil, NewKernelError("Kernel build failed: unable to execute make")
		}

		// Re-read build log for warnings (matching Python: read log before checking returncode)
		for _, line := range strings.Split(logData, "\n") {
			line = strings.TrimRight(line, "\r")
			slog.Debug("Build output", "line", line)
			if buildLogPattern.MatchString(line) {
				warnings = append(warnings, line)
			}
		}
		// Exit code from result
		exitCode := result.ExitCode
		// Python raises: KernelError(f"Kernel build failed (exit {returncode}). Log: {build_log_path}")
		return nil, NewKernelErrorf(
			"Kernel build failed (exit %d). Log: %s", exitCode, buildLogPath)
	}

	// Re-read build log for warnings even on success, with per-line debug logging
	for _, line := range strings.Split(logData, "\n") {
		line = strings.TrimRight(line, "\r")
		slog.Debug("Build output", "line", line)
		if buildLogPattern.MatchString(line) {
			warnings = append(warnings, line)
		}
	}

	// Copy vmlinux to output (matching Python's shutil.copy2 which preserves metadata)
	vmlinuxPath := filepath.Join(kernelDir, "vmlinux")
	if _, err := os.Stat(vmlinuxPath); os.IsNotExist(err) {
		return nil, NewKernelError("Build succeeded but vmlinux not found")
	}
	// Use cp -p to match Python's shutil.copy2 (preserves timestamps, permissions)
	if err := infra.CopyPreservingMetadata(vmlinuxPath, outputPath); err != nil {
		return nil, NewKernelErrorf("Kernel build failed: unable to copy vmlinux: %s", err)
	}
	os.Chmod(outputPath, 0755)

	// size = output_path.stat().st_size; size_mb = size / CONST_MEBIBYTE_BYTES
	size := int64(0)
	if fi, err := os.Stat(outputPath); err == nil {
		size = fi.Size()
	}
	sizeMB := float64(size) / float64(1048576) // CONST_MEBIBYTE_BYTES
	slog.Info("Kernel built", "name", filepath.Base(outputPath), "size_mib", sizeMB)

	return &KernelBuildResult{
		Success:      true,
		Warnings:     warnings,
		InfoMessages: infoMessages,
	}, nil
}

// ── Download Pipeline ───────────────────────────────────────────────────

// DownloadKernelSource downloads a kernel source tarball.
// Matches Python's KernelService.download_kernel_source().
func (s *Service) DownloadKernelSource(ctx context.Context, url, dest string, sha256 string) error {
	if sha256 != "" {
		return s.dl.DownloadFile(ctx, url, dest, sha256, false, false, nil)
	}
	return s.dl.DownloadFile(ctx, url, dest, "", true, true, nil)
}

// ExtractKernelTarball extracts a kernel tarball (tar.xz) and returns the extracted directory.
// Uses Go's archive/tar with path traversal protection, matching Python's
// tarfile.open(tarball, "r:xz") with filter="data" security behavior.
func (s *Service) ExtractKernelTarball(ctx context.Context, tarball, extractDir string) (string, error) {
	if err := os.MkdirAll(extractDir, infra.DirPerm); err != nil {
		return "", err
	}

	// Check xz availability via which command
	xzCheck := system.RunCmdCompat(ctx, []string{"which", "xz"}, system.RunCmdOpts{Capture: true, Check: false})
	if xzCheck.ExitCode != 0 {
		return "", NewKernelErrorf(
			"Extraction failed: 'xz' binary not found in PATH; xz is required " +
				"for kernel tarball extraction (native Go xz reader is not available)")
	}

	tarballData, err := os.ReadFile(tarball)
	if err != nil {
		return "", NewKernelErrorf("Extraction failed: %s", err)
	}

	xzResult := system.RunCmdCompat(ctx, []string{"xz", "-d", "--stdout"}, system.RunCmdOpts{
		Input:   string(tarballData),
		Capture: true,
		Check:   true,
	})
	if xzResult.Err != nil {
		return "", NewKernelErrorf("Extraction failed: xz decompression failed: %s", xzResult.Err)
	}

	tarReader := tar.NewReader(strings.NewReader(xzResult.Stdout))
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", NewKernelErrorf("Extraction failed: %s", err)
		}

		if strings.Contains(header.Name, "..") {
			return "", NewKernelErrorf(
				"Extraction failed: path traversal detected in tarball: %s", header.Name)
		}

		target := filepath.Join(extractDir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(extractDir)+string(os.PathSeparator)) {
			return "", NewKernelErrorf(
				"Extraction failed: path traversal detected in tarball: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, infra.DirPerm); err != nil {
				return "", NewKernelErrorf("Extraction failed: %s", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), infra.DirPerm); err != nil {
				return "", NewKernelErrorf("Extraction failed: %s", err)
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return "", NewKernelErrorf("Extraction failed: %s", err)
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return "", NewKernelErrorf("Extraction failed: %s", err)
			}
			outFile.Close()
		case tar.TypeSymlink:
			continue
		default:
			continue
		}
	}

	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "linux-") {
			return filepath.Join(extractDir, entry.Name()), nil
		}
	}
	return "", NewKernelError("Could not find extracted kernel directory")
}

// ── Remote Version Listing ──────────────────────────────────────────────

// ListRemoteVersions lists available remote kernel versions by delegating to
// the shared HttpDirVersionResolver.
func (s *Service) ListRemoteVersions(
	ctx context.Context,
	specs []*model.KernelSpec,
	arch string,
	ciVersion string,
	cacheTTLSeconds int,
	limit int,
) map[string][]model.VersionInfo {
	configs := kernelSpecsToResolverConfigs(specs)
	raw := s.resolver.Resolve(ctx, configs, arch, ciVersion, cacheTTLSeconds, limit)
	result := make(map[string][]model.VersionInfo, len(raw))
	for key, versions := range raw {
		converted := make([]model.VersionInfo, len(versions))
		for i, v := range versions {
			converted[i] = model.VersionInfo{
				Version:     v.Version,
				DownloadURL: v.DownloadURL,
				SHA256URL:   v.SHA256URL,
				DisplayName: v.DisplayName,
				Type:        v.Type,
				Format:      v.Format,
			}
		}
		result[key] = converted
	}
	return result
}

// ── Internal helpers ────────────────────────────────────────────────────

func (s *Service) downloadFCConfig(
	ctx context.Context,
	kernelDir string,
	spec *model.KernelSpec,
	arch string,
	vars map[string]string,
) error {
	if spec.ConfigURLTemplate == nil || *spec.ConfigURLTemplate == "" {
		return NewKernelErrorf("Missing 'config_url_template' in kernels.yaml for %s", spec.Name)
	}
	url := renderTemplate(*spec.ConfigURLTemplate, vars)
	data, err := s.dl.GetBody(ctx, url)
	if err != nil {
		return NewKernelErrorf("Failed to download config: %s", err)
	}
	configPath := filepath.Join(kernelDir, ".config")
	return os.WriteFile(configPath, data, 0644)
}

func (s *Service) applyConfigFragments(
	ctx context.Context,
	kernelDir string,
	fragments []string,
	vars map[string]string,
	onStatus func(string),
) error {
	configPath := filepath.Join(kernelDir, ".config")
	for idx, fragment := range fragments {
		rendered := renderTemplate(fragment, vars)
		var content []byte
		var err error

		if strings.HasPrefix(rendered, "http://") || strings.HasPrefix(rendered, "https://") {
			if onStatus != nil {
				onStatus(fmt.Sprintf("Fetching config fragment: %s", rendered))
			}
			content, err = s.dl.GetBody(ctx, rendered)
			if err != nil {
				return NewKernelErrorf("Failed to fetch config fragment %s: %s", rendered, err)
			}
		} else {
			rel := strings.TrimPrefix(rendered, "assets/")
			if onStatus != nil {
				onStatus(fmt.Sprintf("Applying config fragment: %s", rel))
			}
			content, err = assets.ReadFile(rel)
			if err != nil {
				return NewKernelErrorf("Config fragment not found: %s (from '%s')", rel, fragment)
			}
		}
		if idx == 0 {
			if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
				baseContent := string(content)
				if !strings.HasSuffix(baseContent, "\n") {
					baseContent += "\n"
				}
				if err := os.WriteFile(configPath, []byte(baseContent), 0644); err != nil {
					return NewKernelErrorf("Failed to write base config fragment: %s", err)
				}
				continue
			}
		}
		if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
			if err := os.WriteFile(configPath, []byte{}, 0644); err != nil {
				return NewKernelErrorf("Failed to create empty .config: %s", err)
			}
		}
		mergeConfigLines(string(content), configPath)
	}
	return nil
}

// runConfigScript runs scripts/config with the given args, logging a warning on failure.
// Matches Python's KernelService._run_config_script() which captures stderr separately.
func runConfigScript(ctx context.Context, configScript, kernelDir string, args ...string) {
	cmdArgs := []string{configScript}
	cmdArgs = append(cmdArgs, args...)
	result := system.RunCmdCompat(ctx, cmdArgs, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   false,
	})
	exitCode := result.ExitCode
	if result.Err != nil || exitCode != 0 {
		slog.Warn("scripts/config failed",
			"args", strings.Join(args, " "),
			"rc", exitCode,
			"stderr", strings.TrimSpace(result.Stderr))
	}
}

// TODO(verdict#33): belongs in infra/strings or similar shared utility
func majorMinorFromVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

func runMake(ctx context.Context, kernelDir, target string, jobs int) (int, string, string) {
	result := system.RunCmdCompat(ctx, []string{"make", target, fmt.Sprintf("-j%d", jobs)}, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   false,
	})
	stdoutStr := result.Stdout
	stderrStr := result.Stderr
	// Log config warnings from make output (matching Python)
	for _, line := range strings.Split(stderrStr, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.Contains(stripped, ".config:") || strings.Contains(strings.ToLower(stripped), "warning:") {
			slog.Debug("Config warning", "message", stripped)
		}
	}
	return result.ExitCode, stdoutStr, stderrStr
}

// TODO(verdict#33): belongs in infra/config or similar shared utility
func mergeConfigLines(content, configPath string) {
	existing := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existing = string(data)
	}

	existingLines := strings.Split(existing, "\n")
	keyToIdx := make(map[string]int)
	for i, line := range existingLines {
		if key := extractConfigKey(line); key != "" {
			keyToIdx[key] = i
		}
	}

	for _, fragLine := range strings.Split(content, "\n") {
		normalized := strings.TrimSpace(fragLine)
		if key := extractConfigKey(normalized); key != "" {
			if idx, ok := keyToIdx[key]; ok {
				existingLines[idx] = normalized
			} else {
				keyToIdx[key] = len(existingLines)
				existingLines = append(existingLines, normalized)
			}
		}
	}

	merged := strings.Join(existingLines, "\n") + "\n"
	os.WriteFile(configPath, []byte(merged), 0644)
}

// TODO(verdict#33): belongs in infra/config or similar shared utility
func extractConfigKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "# ") && strings.HasSuffix(line, " is not set") {
		key := line[2 : len(line)-11]
		if strings.HasPrefix(key, "CONFIG_") {
			return key
		}
		return ""
	}
	if strings.HasPrefix(line, "CONFIG_") && strings.Contains(line, "=") {
		return strings.SplitN(line, "=", 2)[0]
	}
	return ""
}

// TODO(verdict#33): belongs in infra/config or similar shared utility
func verifyConfigSetting(kernelDir, setting string) error {
	data, err := os.ReadFile(filepath.Join(kernelDir, ".config"))
	if err != nil {
		return err
	}
	// Python: config_lines = set(config_content.splitlines())
	// splitlines() splits on \n, \r\n, \r, etc. and removes trailing empty strings.
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	lines := makeSet(strings.Split(normalized, "\n"))
	if strings.Contains(setting, "=") {
		if !lines[setting] {
			return fmt.Errorf("missing: %s", setting)
		}
	} else {
		if !lines[setting+"=y"] && !lines[setting+"=m"] && !lines["# "+setting+" is not set"] {
			return fmt.Errorf("missing: %s", setting)
		}
	}
	return nil
}

// TODO(verdict#33): belongs in infra/strings or similar shared utility
func renderTemplate(tmpl string, vars map[string]string) string {
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", v)
	}
	return tmpl
}

// ── Caching helpers ────────────────────────────────────────────────────

// computeConfigHash computes a hash of kernel configuration parameters for caching.
func (s *Service) computeConfigHash(spec *model.KernelSpec, version string, userConfigPath *string) string {
	h := sha256.New()
	h.Write([]byte(version))
	h.Write([]byte(pythonStr(spec.ConfigFragments)))
	h.Write([]byte(pythonStr(spec.EnabledConfigs)))
	h.Write([]byte(pythonStr(spec.DisabledConfigs)))
	h.Write([]byte(pythonStrSetVal(spec.SetValConfigs)))
	h.Write([]byte(pythonStr(spec.RequiredSettings)))
	if userConfigPath != nil {
		data, err := os.ReadFile(*userConfigPath)
		if err == nil {
			h.Write(data)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// pythonStr formats a []string like Python's str() on a list:
// ["CONFIG_A", "CONFIG_B"] → "['CONFIG_A', 'CONFIG_B']"
func pythonStr(items []string) string {
	if items == nil {
		return "[]"
	}
	var parts []string
	for _, s := range items {
		parts = append(parts, fmt.Sprintf("'%s'", s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pythonStrSetVal formats a [][2]string like Python's str() on a list of tuples:
// [["a","b"]] → "[('a', 'b')]"
func pythonStrSetVal(items [][2]string) string {
	if items == nil {
		return "[]"
	}
	var parts []string
	for _, pair := range items {
		parts = append(parts, fmt.Sprintf("('%s', '%s')", pair[0], pair[1]))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// tryCacheHit attempts to satisfy a build from cache.
func (s *Service) tryCacheHit(
	ctx context.Context,
	outputPath, cacheMarker, cachedKernelPath string,
	useCache bool,
) bool {
	if !useCache {
		return false
	}
	if _, err := os.Stat(cacheMarker); err == nil {
		if _, err := os.Stat(cachedKernelPath); err == nil {
			infra.CopyPreservingMetadata(cachedKernelPath, outputPath)
			os.Chmod(outputPath, 0755)
			slog.Info("Using cached kernel build (config hash match)", "path", outputPath)
			return true
		}
	}
	if _, err := os.Stat(outputPath); err == nil {
		if _, err := os.Stat(cacheMarker); err == nil {
			slog.Debug("Using cached kernel (config hash match)", "path", outputPath)
			return true
		}
		if _, err := os.Stat(outputPath); err == nil {
			slog.Info("Kernel exists but config changed, rebuilding", "path", outputPath)
			os.Remove(outputPath)
		}
	}
	return false
}

// checkBuildDependencies checks for required kernel build dependencies.
func checkBuildDependencies(ctx context.Context) error {
	requiredCommands := []string{
		"git", "curl", "make", "gcc", "flex", "bison", "bc", "pahole", "ld",
	}
	var missing []string
	for _, cmd := range requiredCommands {
		result := system.RunCmdCompat(ctx, []string{"which", cmd}, system.RunCmdOpts{Capture: true, Check: false})
		if result.ExitCode != 0 {
			missing = append(missing, cmd)
		}
	}
	libraryChecks := []struct {
		pkg, display string
	}{
		{"libelf", "libelf"},
		{"openssl", "libssl-dev"},
	}
	for _, lc := range libraryChecks {
		result := system.RunCmdCompat(
			ctx,
			[]string{"pkg-config", "--exists", lc.pkg},
			system.RunCmdOpts{Check: true},
		)
		if result.Err != nil {
			missing = append(missing, lc.display)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		missingStr := strings.Join(missing, ", ")
		return NewKernelErrorf(
			"Missing kernel build dependencies: %s\n\n"+
				"Install on Ubuntu/Debian:\n"+
				"  sudo apt update\n"+
				"  sudo apt install -y build-essential libncurses-dev bison flex\n"+
				"  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves\n\n"+
				"Install on Arch Linux:\n"+
				"  sudo pacman -S base-devel ncurses bison flex\n"+
				"  sudo pacman -S openssl bc curl git pahole\n",
			missingStr)
	}
	return nil
}

// ── Utility helpers ─────────────────────────────────────────────────────

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func requireStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrPtr(m map[string]any, key string) *string {
	v, _ := m[key].(string)
	if v == "" {
		return nil
	}
	return &v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalIntPtr(m map[string]any, key string) *int {
	v, ok := m[key].(int)
	if !ok {
		if f, ok := m[key].(float64); ok {
			v = int(f)
		} else {
			return nil
		}
	}
	if v == 0 {
		return nil
	}
	return &v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrFromPtr(m map[string]any, parent, key string) *string {
	if p, ok := m[parent].(map[string]any); ok {
		v, _ := p[key].(string)
		if v != "" {
			return &v
		}
	}
	return nil
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrFrom(m map[string]any, parent, key string) string {
	if p, ok := m[parent].(map[string]any); ok {
		v, _ := p[key].(string)
		return v
	}
	return ""
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func requireStrList(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func parseSetValList(m map[string]any, key string) [][2]string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var result [][2]string
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			continue
		}
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			result = append(result, [2]string{parts[0], parts[1]})
		}
	}
	return result
}

// Helper to get a string list from options map
func getStringListOption(opts map[string]any, key string) []string {
	if opts == nil {
		return nil
	}
	raw, ok := opts[key]
	if !ok {
		return nil
	}
	rawList, ok := raw.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range rawList {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// Helper to get a string from options map
func getStringOption(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	v, _ := opts[key].(string)
	return v
}

// ParseFilename parses a kernel filename to extract base name, version, and arch.
// Matches Python's KernelService.parse_filename().
func ParseFilename(filename string) ParsedKernelFilename {
	name := filename
	arches := []string{"x86_64", "amd64", "arm64", "aarch64"}
	version := "-"
	arch := "-"

	for _, a := range arches {
		if strings.HasSuffix(name, "-"+a) {
			arch = a
			name = name[:len(name)-len(a)-1]
			break
		}
	}

	versionRe := regexp.MustCompile(`-v?(\d+(?:\.\d+)*)$`)
	if m := versionRe.FindStringSubmatch(name); len(m) >= 2 {
		versionNum := m[1]
		fullMatch := m[0]
		if strings.HasPrefix(fullMatch, "-v") {
			version = "v" + versionNum
		} else {
			version = versionNum
		}
		name = name[:len(name)-len(fullMatch)]
	}

	baseName := strings.Split(name, "-")[0]
	return ParsedKernelFilename{BaseName: baseName, Version: version, Arch: arch}
}

// ImportKernel copies a local vmlinux file to the kernels cache directory,
// generates a content-addressed SHA256 ID, creates a KernelItem with type "custom",
// and persists it via upsert.
// Matches Python's KernelService.import_kernel().
func (s *Service) ImportKernel(
	ctx context.Context,
	name string,
	sourcePath string,
	version string,
	arch string,
	setDefault bool,
) (*model.KernelItem, error) {
	// Expand ~ and resolve symlinks, matching Python's source_path.expanduser().resolve()
	resolvedPath, err := system.ExpandAndResolve(sourcePath)
	if err != nil {
		return nil, NewKernelErrorf("Failed to resolve source path '%s': %s", sourcePath, err)
	}

	kernelsDir := filepath.Join(s.cacheDir, "kernels")
	if err := os.MkdirAll(kernelsDir, infra.DirPerm); err != nil {
		return nil, err
	}

	destFilename := fmt.Sprintf("%s-%s-%s", name, version, arch)
	destPath := filepath.Join(kernelsDir, destFilename)

	// Copy file (matching Python's shutil.copy2 which preserves metadata)
	if err := infra.CopyPreservingMetadata(resolvedPath, destPath); err != nil {
		return nil, NewKernelErrorf("Failed to copy kernel file to %s: %s", destPath, err)
	}
	os.Chmod(destPath, 0755)

	// Generate content-addressed ID using HashGenerator.Kernel() (matching Python exactly)
	now := time.Now().Format(time.RFC3339)
	kernelID, err := crypto.KernelID(destPath, version, arch, now)
	if err != nil {
		return nil, fmt.Errorf("compute kernel ID: %w", err)
	}

	kernelItem := &model.KernelItem{
		ID:        kernelID,
		Name:      fmt.Sprintf("%s %s", name, version),
		BaseName:  name,
		Version:   version,
		Arch:      arch,
		Type:      "custom",
		Path:      destFilename,
		IsDefault: setDefault,
		IsPresent: true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Upsert(ctx, kernelItem); err != nil {
		return nil, err
	}
	if setDefault {
		if err := s.repo.SetDefault(ctx, kernelItem.ID); err != nil {
			return nil, err
		}
	}

	shortID, _ := crypto.ShortenID(kernelID)
	slog.Info("Imported kernel", "name", kernelItem.Name, "version", version, "arch", arch, "id", shortID)
	return kernelItem, nil
}

// ── Version resolution helpers ─────────────────────────────────────────────

// kernelSpecsToResolverConfigs converts a list of KernelSpec to ResolverConfig structs
// for delegation to the shared HttpDirVersionResolver.
func kernelSpecsToResolverConfigs(specs []*model.KernelSpec) []download.ResolverConfig {
	configs := make([]download.ResolverConfig, 0, len(specs))
	for _, spec := range specs {
		cfg := download.ResolverConfig{
			Type: spec.KernelType,
			Name: spec.Name,
		}

		if spec.Resolver != nil {
			cfg.Resolver = *spec.Resolver
		}
		if spec.VersionsURL != nil {
			cfg.VersionsURL = *spec.VersionsURL
		}
		if spec.Source != "" {
			cfg.Source = spec.Source
		}
		if spec.SHA256URL != "" {
			cfg.SHA256URL = spec.SHA256URL
		}
		if spec.Version != "" {
			cfg.Version = spec.Version
		}

		if spec.KernelType == infra.KernelTypeOfficial {
			cfg.Format = "tar.xz"
		} else {
			cfg.Format = "vmlinux"
		}

		resolver := ""
		if spec.Resolver != nil {
			resolver = *spec.Resolver
		}

		switch resolver {
		case "http-dir":
			if spec.VersionsURL != nil && *spec.VersionsURL != "" {
				cfg.DownloadURL = spec.Source
				if spec.SHA256URL != "" {
					cfg.SHA256URL = spec.SHA256URL
				}
				filePattern := "linux-"
				if spec.FilePattern != nil {
					filePattern = *spec.FilePattern
				}
				fileSuffix := ".tar.xz"
				if spec.FileSuffix != nil {
					fileSuffix = *spec.FileSuffix
				}
				discoveries := getStringSlice(spec.Options, "version_discoveries")
				cfg.Options = download.ResolverOptions{
					VersionDiscoveries: discoveries,
					FilePattern:        filePattern,
					FileSuffix:         fileSuffix,
				}
			}
		case "firecracker-s3":
			if spec.ListURLTemplate != nil && *spec.ListURLTemplate != "" {
				cfg.ListURLTemplate = *spec.ListURLTemplate
				// Strip {version} from list_url_template for listing purposes
				cfg.ListURLTemplate = strings.ReplaceAll(cfg.ListURLTemplate, "{version}", "")
				// Download URL template
				sourceBase := strings.TrimRight(spec.Source, "/")
				cfg.DownloadURL = fmt.Sprintf("%s/firecracker-ci/{ci_version}/{arch}/vmlinux-{version}", sourceBase)
				if spec.SHA256URL != "" {
					cfg.SHA256URL = spec.SHA256URL
				}
				s3Pattern := "vmlinux-([\\d.]+)"
				if spec.Options != nil {
					if p, ok := spec.Options["s3_version_pattern"].(string); ok && p != "" {
						s3Pattern = p
					}
				}
				cfg.Options = download.ResolverOptions{
					S3VersionPattern: s3Pattern,
				}
			}
		default:
		}

		configs = append(configs, cfg)
	}
	return configs
}

// resolverConfigsFromMaps converts []map[string]any to []download.ResolverConfig.
func resolverConfigsFromMaps(configs []map[string]any) []download.ResolverConfig {
	result := make([]download.ResolverConfig, 0, len(configs))
	for _, m := range configs {
		var cfg download.ResolverConfig
		if v, ok := m["type"].(string); ok {
			cfg.Type = v
		}
		if v, ok := m["resolver"].(string); ok {
			cfg.Resolver = v
		}
		if v, ok := m["versions_url"].(string); ok {
			cfg.VersionsURL = v
		}
		if v, ok := m["download_url"].(string); ok {
			cfg.DownloadURL = v
		}
		if v, ok := m["sha256_url"].(string); ok {
			cfg.SHA256URL = v
		}
		if v, ok := m["list_url_template"].(string); ok {
			cfg.ListURLTemplate = v
		}
		if v, ok := m["format"].(string); ok {
			cfg.Format = v
		}
		if v, ok := m["name"].(string); ok {
			cfg.Name = v
		}
		if v, ok := m["source"].(string); ok {
			cfg.Source = v
		}
		if v, ok := m["version"].(string); ok {
			cfg.Version = v
		}

		if optsRaw, ok := m["options"].(map[string]any); ok {
			if v, ok := optsRaw["version_discoveries"].([]any); ok {
				cfg.Options.VersionDiscoveries = make([]string, len(v))
				for i, item := range v {
					cfg.Options.VersionDiscoveries[i], _ = item.(string)
				}
			}
			if v, ok := optsRaw["file_pattern"].(string); ok {
				cfg.Options.FilePattern = v
			}
			if v, ok := optsRaw["file_suffix"].(string); ok {
				cfg.Options.FileSuffix = v
			}
			if v, ok := optsRaw["s3_version_pattern"].(string); ok {
				cfg.Options.S3VersionPattern = v
			}
		}

		result = append(result, cfg)
	}
	return result
}

// getStringSlice extracts a []string value from a map for a given key.
// TODO(verdict#33): belongs in infra/maps or similar shared utility
func getStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// extractVMName extracts the "name" from a VM object.
// VMs are now typed as *model.VM from the shared model package,
// so we access Name directly without reflection.
func extractVMName(vm *model.VM) string {
	return vm.Name
}

// ResolveLatestVersion resolves the latest available version for a given kernel type.
func (s *Service) ResolveLatestVersion(ctx context.Context, kernelType string, ciVersion string) (string, error) {
	configs, err := s.LoadKernelTypesConfig()
	if err != nil {
		return "", err
	}

	var typeConfigs []map[string]any
	for _, c := range configs {
		if t, ok := c["type"].(string); ok && t == kernelType {
			typeConfigs = append(typeConfigs, c)
		}
	}
	if len(typeConfigs) == 0 {
		return "", NewKernelErrorf("Cannot resolve 'latest' for unknown type: %s", kernelType)
	}

	resolverConfigs := resolverConfigsFromMaps(typeConfigs)

	versionMap := s.resolver.Resolve(ctx, resolverConfigs, "x86_64", ciVersion, 0, 1)

	var allVersions []string
	for _, versions := range versionMap {
		for _, v := range versions {
			allVersions = append(allVersions, v.Version)
		}
	}
	if len(allVersions) == 0 {
		return "", NewKernelErrorf(
			"Cannot resolve 'latest' for %s: no versions available from upstream", kernelType)
	}

	sort.Slice(allVersions, func(i, j int) bool {
		return version.SemverGreater(allVersions[i], allVersions[j])
	})
	return allVersions[0], nil
}
