// Package kernel provides kernel binary download and management.
// Layer: Core domain — never imports other core/* packages.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/archive"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"

	"mvmctl/internal/assets"
)

// --- Service-layer types ---

type KernelPipelineResult struct {
	ConfigResult *KernelConfigResult
	BuildResult  *KernelBuildResult
	Success      bool
}

type KernelConfigResult struct {
	Success      bool
	Warnings     []string
	InfoMessages []string
}

type KernelBuildResult struct {
	Success      bool
	Warnings     []string
	InfoMessages []string
}

// BuildConfig groups the parameters for buildFromSource.
type BuildConfig struct {
	Spec           *model.KernelSpec
	Version        string
	SourceURL      string
	OutputPath     string
	Jobs           int
	Arch           string
	SHA256         string
	KeepBuildDir   bool
	UserConfigPath *string
	UseCache       bool
	OnDownload     event.OnDownloadCallback
	OnProgress     event.OnProgressCallback
	// Configs from selected features to enforce on top of spec.DefaultConfigs.
	// Keys are kernel config options (CONFIG_FOO); values are "y", "n", or a number.
	FeatureEnforces map[string]string
}

// applyConfigOption runs scripts/config for a single kernel option.
//
//	Value "y" / "yes" / "true"  → --enable   (set boolean = y)
//	Value "n" / "no" / "false"  → --disable  (set boolean = n)
//	Anything else               → --set-val  (set numeric/string value)
func applyConfigOption(ctx context.Context, configScriptPath, kernelDir, option, value string) {
	switch strings.ToLower(value) {
	case "y", "yes", "true":
		runConfigScript(ctx, configScriptPath, kernelDir, "--enable", option)
	case "n", "no", "false":
		runConfigScript(ctx, configScriptPath, kernelDir, "--disable", option)
	default:
		runConfigScript(ctx, configScriptPath, kernelDir, "--set-val", option, value)
	}
}

// Service provides stateless kernel operations.
type Service struct {
	repo     Repository
	cacheDir string
	dl       *download.Downloader
	resolver *download.HttpDirVersionResolver
	specs    map[string]*model.KernelSpec
}

func NewService(repo Repository, cacheDir string) *Service {
	return &Service{
		repo:     repo,
		cacheDir: cacheDir,
		dl:       download.New(),
		resolver: download.NewHttpDirVersionResolver(),
	}
}

// --- Firecracker Kernel Download ---

// FetchFirecrackerKernel downloads a pre-built Firecracker CI vmlinux.
func (s *Service) FetchFirecrackerKernel(
	ctx context.Context,
	spec *model.KernelSpec,
	ciVersion, arch, outputDir string,
	onProgress event.OnDownloadCallback,
) (*model.KernelPullResult, error) {
	if spec.ListURLTemplate == nil || *spec.ListURLTemplate == "" {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Missing 'list_url_template' in kernels.yaml for %s", spec.Name))
	}

	templateVars := map[string]string{
		"ci_version": ciVersion,
		"arch":       arch,
		"version":    spec.Version,
	}
	listURL, err := infra.RenderTemplate(*spec.ListURLTemplate, templateVars)
	if err != nil {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to render list URL template: %s", err))
	}

	xmlContent, err := s.dl.GetBody(ctx, listURL)
	if err != nil {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to list CI kernels: %s", err))
	}

	pattern := fmt.Sprintf(KernelS3XMLPattern,
		regexp.QuoteMeta(ciVersion), regexp.QuoteMeta(arch))
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(string(xmlContent), -1)
	if len(matches) == 0 {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"No vmlinux found for Firecracker CI version %s / arch %s", ciVersion, arch))
	}

	var versions []string
	for _, m := range matches {
		versions = append(versions, extractVersionFromKey(m[1]))
	}
	version.SortVersions(versions)

	kernelVersion := versions[0]
	chosenKey := fmt.Sprintf(KernelS3KeyPattern, ciVersion, arch, kernelVersion)
	outputPath := filepath.Join(outputDir, fmt.Sprintf(KernelOutputPattern, spec.OutputName, kernelVersion, arch))

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
			slog.Debug("Skipping checksum")
		}
	}
	if expectedSHA256 == "" && !intentionalNoChecksum {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Checksum required for Firecracker CI kernel download: %s", downloadURL))
	}

	slog.Info("Downloading Firecracker CI kernel", "url", downloadURL)
	if err := s.dl.DownloadFile(ctx, downloadURL, outputPath, expectedSHA256, true, true, onProgress); err != nil {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to download Firecracker CI kernel: %s", err),
		)
	}
	os.Chmod(outputPath, infra.ExecutablePerm)

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

// --- Official Kernel Build Pipeline ---

// BuildOfficialKernel builds an official kernel from source.
func (s *Service) BuildOfficialKernel(
	ctx context.Context,
	spec *model.KernelSpec,
	arch, outputDir string,
	jobs int,
	keepBuildDir bool,
	useCache bool,
	userConfigPath *string,
	featureEnforces map[string]string,
	onDownload event.OnDownloadCallback,
	onProgress event.OnProgressCallback,
) (*model.KernelPullResult, error) {
	if err := checkBuildDependencies(ctx); err != nil {
		return nil, err
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf(KernelOutputPattern, spec.OutputName, spec.Version, arch))

	buildResult, err := s.buildFromSource(ctx, BuildConfig{
		Spec:            spec,
		Version:         spec.Version,
		SourceURL:       spec.Source,
		OutputPath:      outputPath,
		Jobs:            jobs,
		Arch:            arch,
		SHA256:          spec.SHA256,
		KeepBuildDir:    keepBuildDir,
		UserConfigPath:  userConfigPath,
		UseCache:        useCache,
		OnDownload:      onDownload,
		OnProgress:      onProgress,
		FeatureEnforces: featureEnforces,
	})
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
func (s *Service) buildFromSource(ctx context.Context, cfg BuildConfig) (*KernelPipelineResult, error) {
	buildDir := cfg.Spec.BuildDir
	if buildDir == "" {
		buildDir = "."
	}

	configHash := s.computeConfigHash(cfg.Spec, cfg.Version, cfg.UserConfigPath, cfg.FeatureEnforces)
	cacheKey := fmt.Sprintf("%s-%s", cfg.Version, configHash)
	cacheMarker := filepath.Join(filepath.Dir(buildDir), fmt.Sprintf(KernelCacheMarker, cacheKey))
	cachedKernelPath := filepath.Join(filepath.Dir(buildDir), fmt.Sprintf(KernelCachePath, cacheKey))

	if tryCacheHit(cfg.OutputPath, cacheMarker, cachedKernelPath, cfg.UseCache) {
		return &KernelPipelineResult{ConfigResult: nil, BuildResult: nil, Success: true}, nil
	}

	// Resolve source URL and checksum
	resolvedSourceURL, resolvedSHA256, err := s.resolveSourceURL(
		ctx,
		cfg.Spec,
		cfg.Version,
		cfg.Arch,
		cfg.SHA256,
		cfg.OnProgress,
	)
	if err != nil {
		return nil, err
	}

	tarball := filepath.Join(buildDir, fmt.Sprintf(KernelTarballPattern, cfg.Version))
	kernelSrcDir := filepath.Join(buildDir, fmt.Sprintf(KernelSrcDirPattern, cfg.Version, cfg.Arch))

	// Download tarball if not cached
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
			cfg.OnDownload,
		); err != nil {
			return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Download failed: %s", err))
		}
	} else {
		slog.Debug("Using cached tarball", "path", tarball)
	}

	// Extract tarball if not already extracted
	if _, err := os.Stat(kernelSrcDir); os.IsNotExist(err) {
		extracted, err := s.ExtractKernelTarball(ctx, tarball, buildDir)
		if err != nil {
			return nil, err
		}
		if extracted != kernelSrcDir {
			if err := os.Rename(extracted, kernelSrcDir); err != nil {
				return nil, errs.New(
					errs.CodeKernelBuildFailed,
					fmt.Sprintf("Failed to rename kernel source directory: %s", err),
				)
			}
		}
	} else {
		slog.Debug("Using existing source", "path", kernelSrcDir)
	}

	configResult, err := s.PrepareKernelConfig(
		ctx,
		kernelSrcDir,
		cfg.Spec,
		cfg.Arch,
		cfg.Jobs,
		cfg.UserConfigPath,
		cfg.FeatureEnforces,
		cfg.OnProgress,
	)
	if err != nil {
		return nil, err
	}

	buildResult, err := s.RunMakeVmlinux(ctx, kernelSrcDir, cfg.OutputPath, cfg.Jobs)
	if err != nil {
		return &KernelPipelineResult{
			ConfigResult: configResult,
			BuildResult:  nil,
			Success:      false,
		}, err
	}

	// Cache output
	if cfg.UseCache {
		os.MkdirAll(filepath.Dir(cachedKernelPath), infra.DirPerm)
		if err := infra.CopyPreservingMetadata(cfg.OutputPath, cachedKernelPath); err != nil {
			slog.Debug("Failed to cache kernel build", "error", err)
		}
		os.WriteFile(cacheMarker, []byte(cacheKey), 0644)
	}

	// Cleanup build directory
	if !cfg.KeepBuildDir {
		if err := os.RemoveAll(buildDir); err != nil {
			slog.Debug("Failed to clean up build directory", "dir", buildDir, "error", err)
		} else {
			slog.Debug("Build directory cleaned up", "dir", buildDir)
		}
	} else {
		slog.Debug("Build directory kept at", "dir", buildDir)
	}

	return &KernelPipelineResult{
		ConfigResult: configResult,
		BuildResult:  buildResult,
		Success:      true,
	}, nil
}

// resolveSourceURL resolves source URL template vars and fetches SHA256 if needed.
func (s *Service) resolveSourceURL(
	ctx context.Context,
	spec *model.KernelSpec,
	version, arch, sha256 string,
	onProgress event.OnProgressCallback,
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
	resolvedSourceURL, err := infra.RenderTemplate(spec.Source, templateVars)
	if err != nil {
		return "", "", errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to render source URL template: %s", err),
		)
	}

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
			filename := fmt.Sprintf(KernelTarballPattern, version)
			if sha, err := s.fetchSHA256(ctx, resolvedSHA256URL, filename); err == nil && sha != "" {
				resolvedSHA256 = sha
			}
		}
	}

	if resolvedSHA256 == "" && !intentionalNoChecksum {
		return resolvedSourceURL, "", errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Checksum required for kernel source download: %s", resolvedSourceURL))
	}

	return resolvedSourceURL, resolvedSHA256, nil
}

// fetchSHA256 fetches a SHA256 checksum from a URL, optionally matching a filename.
func (s *Service) fetchSHA256(ctx context.Context, sha256URL, filename string) (string, error) {
	content, err := s.dl.GetBody(ctx, sha256URL)
	if err != nil {
		return "", fmt.Errorf("fetch sha256: %w", err)
	}

	text := strings.TrimSpace(string(content))
	if filename == "" {
		parts := strings.Fields(text)
		if len(parts) > 0 {
			return strings.ToLower(parts[0]), nil
		}
		return "", nil
	}

	for line := range strings.SplitSeq(text, "\n") {
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

// --- Kernel Listing ---

func (s *Service) ListAll(ctx context.Context, verify bool) ([]*model.KernelItem, error) {
	kernels, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return kernels, nil
	}

	var missingIDs []string
	for _, kernel := range kernels {
		if _, err := os.Stat(kernel.Path); os.IsNotExist(err) {
			missingIDs = append(missingIDs, kernel.ID)
		}
	}
	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		return s.repo.ListAll(ctx)
	}
	return kernels, nil
}

func (s *Service) List(ctx context.Context) ([]*model.KernelItem, error) {
	return s.ListAll(ctx, true)
}

// --- Kernel Remove ---

func (s *Service) Remove(ctx context.Context, kernel *model.KernelItem, force bool) (*model.KernelItem, error) {
	vms := kernel.VMs
	hasVMs := len(vms) > 0
	hasSnapshots := len(kernel.Snapshots) > 0

	if (hasVMs || hasSnapshots) && !force {
		var refs []string
		for _, vm := range vms {
			refs = append(refs, vm.Name)
		}
		if hasSnapshots {
			names := make([]string, len(kernel.Snapshots))
			for i, s := range kernel.Snapshots {
				names[i] = s.Name
			}
			refs = append(refs, fmt.Sprintf("%d snapshot(s): %s", len(kernel.Snapshots), strings.Join(names, ", ")))
		}
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Kernel referenced by: %s", strings.Join(refs, ", ")),
		)
	}

	if _, statErr := os.Stat(kernel.Path); statErr == nil {
		if err := os.Remove(kernel.Path); err != nil {
			slog.Warn("Failed to remove kernel file", "error", err)
		}
	}

	if hasVMs || hasSnapshots {
		return kernel, s.repo.SoftDelete(ctx, kernel.ID)
	}
	return kernel, s.repo.Delete(ctx, kernel.ID)
}

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

// --- Spec Loading ---

func (s *Service) LoadSpecs() (map[string]*model.KernelSpec, error) {
	if s.specs != nil {
		return s.specs, nil
	}

	data, err := assets.ReadFile("kernels.yaml")
	if err != nil {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to load kernels.yaml: %s", err))
	}

	// Unmarshal into raw map to get spec names (the YAML keys).
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to parse kernels.yaml: %s", err))
	}

	// Intermediate YAML struct matching the file format for clean unmarshal.
	// NOTE: KernelSpec's yaml tag for KernelType is "kernel_type", but the
	// kernels.yaml file uses "type". The specYAML struct matches the file.
	type specYAML struct {
		KernelType            string                         `yaml:"type"`
		Version               string                         `yaml:"version"`
		Source                string                         `yaml:"source"`
		OutputName            string                         `yaml:"output_name"`
		BuildDir              string                         `yaml:"build_dir"`
		ListURLTemplate       *string                        `yaml:"list_url_template,omitempty"`
		BaseConfigURLTemplate *string                        `yaml:"base_config_url_template,omitempty"`
		SHA256                string                         `yaml:"sha256,omitempty"`
		SHA256URL             string                         `yaml:"sha256_url,omitempty"`
		ConfigFragments       []string                       `yaml:"config_fragments"`
		ParallelJobs          *int                           `yaml:"parallel_jobs,omitempty"`
		DefaultConfigs        map[string]string              `yaml:"default_configs"`
		Resolver              *string                        `yaml:"resolver,omitempty"`
		VersionsURL           *string                        `yaml:"versions_url,omitempty"`
		FilePattern           *string                        `yaml:"file_pattern,omitempty"`
		FileSuffix            *string                        `yaml:"file_suffix,omitempty"`
		Options               map[string]any                 `yaml:"options,omitempty"`
		Features              map[string]model.KernelFeature `yaml:"features,omitempty"`
	}

	specs := make(map[string]*model.KernelSpec, len(raw))
	for name, rawAny := range raw {
		entry, err := yaml.Marshal(rawAny)
		if err != nil {
			return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to encode spec %s: %s", name, err))
		}
		var sy specYAML
		if err := yaml.Unmarshal(entry, &sy); err != nil {
			return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to decode spec %s: %s", name, err))
		}

		specs[name] = &model.KernelSpec{
			Name:                  name,
			KernelType:            sy.KernelType,
			Version:               sy.Version,
			Source:                sy.Source,
			OutputName:            sy.OutputName,
			BuildDir:              sy.BuildDir,
			ListURLTemplate:       sy.ListURLTemplate,
			BaseConfigURLTemplate: sy.BaseConfigURLTemplate,
			SHA256:                sy.SHA256,
			SHA256URL:             sy.SHA256URL,
			ConfigFragments:       sy.ConfigFragments,
			ParallelJobs:          sy.ParallelJobs,
			DefaultConfigs:        sy.DefaultConfigs,
			Resolver:              sy.Resolver,
			VersionsURL:           sy.VersionsURL,
			FilePattern:           sy.FilePattern,
			FileSuffix:            sy.FileSuffix,
			Options:               sy.Options,
			Features:              sy.Features,
		}
	}

	s.specs = specs
	return specs, nil
}

// GetSpecsFor returns kernel specs filtered by criteria.
func (s *Service) GetSpecsFor(names []string, kernelType, version string) ([]*model.KernelSpec, error) {
	allSpecs, err := s.LoadSpecs()
	if err != nil {
		return nil, err
	}

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
			return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
				"Kernel spec(s) not found: %s. Available: %s",
				strings.Join(missing, ", "), strings.Join(avail, ", ")))
		}
		return results, nil
	}

	var filtered []*model.KernelSpec
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for _, spec := range allSpecs {
		if kernelType != "" && spec.KernelType != kernelType {
			continue
		}
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

// --- Build Pipeline ---

func (s *Service) buildTemplateVars(spec *model.KernelSpec, arch string) map[string]string {
	majorMinor := majorMinorFromVersion(spec.Version)
	return map[string]string{
		"major_minor":    majorMinor,
		"version":        majorMinor,
		"kernel_version": spec.Version,
		"ci_version":     spec.Version,
		"arch":           arch,
	}
}

func (s *Service) PrepareKernelConfig(
	ctx context.Context,
	kernelDir string,
	spec *model.KernelSpec,
	arch string,
	jobs int,
	userConfigPath *string,
	featureEnforces map[string]string,
	onProgress event.OnProgressCallback,
) (*KernelConfigResult, error) {
	var warnings []string
	var infoMessages []string
	templateVars := s.buildTemplateVars(spec, arch)

	// Download Firecracker config and apply fragments
	if err := s.downloadFCConfig(ctx, kernelDir, spec, templateVars); err != nil {
		var de *errs.DomainError
		if errors.As(err, &de) &&
			(de.Code == errs.CodeKernelBuildFailed || de.Code == errs.CodeKernelConfigFailed) {
			if onProgress != nil {
				onProgress(event.Progress{Phase: "build", Status: "running", Message: "Using defconfig instead..."})
			}
			slog.Info("Using defconfig instead")
			if rc, _, _ := runMake(ctx, kernelDir, KernelDefconfigTarget, jobs); rc != 0 {
				return nil, errs.New(errs.CodeKernelBuildFailed, "defconfig failed")
			}
		} else {
			return nil, err
		}
	} else if len(spec.ConfigFragments) > 0 {
		if onProgress != nil {
			onProgress(
				event.Progress{Phase: "build", Status: "running", Message: "Applying kernel config fragments..."},
			)
		}
		if err := s.applyConfigFragments(ctx, kernelDir, spec.ConfigFragments, templateVars, onProgress); err != nil {
			return nil, err
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{Phase: "build", Status: "running", Message: "Synchronizing kernel config..."})
	}
	slog.Debug("Synchronizing config")
	if rc, _, _ := runMake(ctx, kernelDir, KernelOlddefconfigTarget, jobs); rc != 0 {
		return nil, errs.New(errs.CodeKernelBuildFailed, "olddefconfig failed")
	}

	// Merge default configs with feature enforces (features override defaults).
	configScriptPath := filepath.Join(kernelDir, "scripts", "config")
	mergedConfigs := make(map[string]string, len(spec.DefaultConfigs)+len(featureEnforces))
	maps.Copy(mergedConfigs, spec.DefaultConfigs)
	maps.Copy(mergedConfigs, featureEnforces)

	if len(mergedConfigs) > 0 {
		if onProgress != nil {
			onProgress(
				event.Progress{
					Phase:   "build",
					Status:  "running",
					Message: fmt.Sprintf("Applying %d kernel config options...", len(mergedConfigs)),
				},
			)
		}
		slog.Debug("Applying kernel config options", "count", len(mergedConfigs))
		for option, value := range mergedConfigs {
			applyConfigOption(ctx, configScriptPath, kernelDir, option, value)
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{Phase: "build", Status: "running", Message: "Resolving config dependencies..."})
	}
	slog.Debug("Resolving dependencies")
	if rc, _, _ := runMake(ctx, kernelDir, KernelOlddefconfigTarget, jobs); rc != 0 {
		return nil, errs.New(errs.CodeKernelBuildFailed, "olddefconfig failed after enabling options")
	}

	if userConfigPath != nil && *userConfigPath != "" {
		if _, statErr := os.Stat(*userConfigPath); statErr == nil {
			if onProgress != nil {
				onProgress(
					event.Progress{
						Phase:   "build",
						Status:  "running",
						Message: fmt.Sprintf("Applying user config fragment: %s", *userConfigPath),
					},
				)
			}
			slog.Info("Applying user config fragment", "path", *userConfigPath)
			configPath := filepath.Join(kernelDir, ".config")
			userData, err := os.ReadFile(*userConfigPath)
			if err != nil {
				return nil, errs.New(
					errs.CodeKernelBuildFailed,
					fmt.Sprintf("Failed to read user config fragment %s: %s", *userConfigPath, err),
				)
			}
			mergeConfigLines(string(userData), configPath)
			if onProgress != nil {
				onProgress(
					event.Progress{
						Phase:   "build",
						Status:  "running",
						Message: "Resolving dependencies after user config...",
					},
				)
			}
			slog.Debug("Resolving dependencies after user config")
			if rc, _, _ := runMake(ctx, kernelDir, "olddefconfig", jobs); rc != 0 {
				return nil, errs.New(errs.CodeKernelBuildFailed, "olddefconfig failed after user config")
			}
		}
	}

	if onProgress != nil {
		onProgress(event.Progress{Phase: "build", Status: "running", Message: "Verifying kernel configuration..."})
	}
	slog.Debug("Verifying configuration")
	configSettings, err := parseKernelConfig(kernelDir)
	if err != nil {
		return nil, err
	}
	var missingSettings []string
	for option := range mergedConfigs {
		if !configSettings[option] {
			missingSettings = append(missingSettings, option)
		} else {
			slog.Debug("Verified setting", "setting", option)
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

var buildLogPattern = regexp.MustCompile(`(?i)(warning|error|cannot find|undefined reference|fatal|note:)`)

func (s *Service) RunMakeVmlinux(
	ctx context.Context,
	kernelDir, outputPath string,
	jobs int,
) (*KernelBuildResult, error) {
	warnings := []string{"Building kernel... (this may take 10-30 minutes)"}
	var infoMessages []string
	slog.Info("Building vmlinux", "jobs", jobs)
	slog.Info("This may take 10-30 minutes")

	buildLogPath := outputPath + KernelBuildLogSuffix
	os.MkdirAll(filepath.Dir(buildLogPath), infra.DirPerm)

	result, err := system.DefaultRunner.Run(
		ctx,
		[]string{KernelMakeCmd, KernelMakeTarget, fmt.Sprintf("-j%d", jobs)},
		system.RunCmdOpts{
			Cwd:     kernelDir,
			Capture: true,
			Check:   true,
		},
	)

	logData := result.Stdout
	if result.Stderr != "" {
		logData += "\n" + result.Stderr
	}
	os.WriteFile(buildLogPath, []byte(logData), 0644)

	if err != nil {
		if strings.Contains(err.Error(), "Command not found") {
			return nil, errs.New(errs.CodeKernelBuildFailed, "Kernel build failed: unable to execute make")
		}
		warnings = append(warnings, parseBuildWarnings(logData)...)
		return nil, errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Kernel build failed (exit %d). Log: %s", result.ExitCode, buildLogPath))
	}

	warnings = append(warnings, parseBuildWarnings(logData)...)

	vmlinuxPath := filepath.Join(kernelDir, "vmlinux")
	if _, err := os.Stat(vmlinuxPath); os.IsNotExist(err) {
		return nil, errs.New(errs.CodeKernelBuildFailed, "Build succeeded but vmlinux not found")
	}
	if err := infra.CopyPreservingMetadata(vmlinuxPath, outputPath); err != nil {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Kernel build failed: unable to copy vmlinux: %s", err),
		)
	}
	os.Chmod(outputPath, 0755)

	size := int64(0)
	if fi, err := os.Stat(outputPath); err == nil {
		size = fi.Size()
	}
	sizeMB := float64(size) / float64(1048576)
	slog.Info("Kernel built", "name", filepath.Base(outputPath), "size_mib", sizeMB)

	return &KernelBuildResult{
		Success:      true,
		Warnings:     warnings,
		InfoMessages: infoMessages,
	}, nil
}

// parseBuildWarnings extracts build warnings from kernel build output.
func parseBuildWarnings(logData string) []string {
	var warnings []string
	for line := range strings.SplitSeq(logData, "\n") {
		line = strings.TrimRight(line, "\r")
		slog.Debug("Build output", "line", line)
		if buildLogPattern.MatchString(line) {
			warnings = append(warnings, line)
		}
	}
	return warnings
}

// --- Download Pipeline ---

func (s *Service) DownloadKernelSource(ctx context.Context, url, dest string, sha256 string) error {
	if sha256 != "" {
		return s.dl.DownloadFile(ctx, url, dest, sha256, false, false, nil)
	}
	return s.dl.DownloadFile(ctx, url, dest, "", true, true, nil)
}

func (s *Service) ExtractKernelTarball(ctx context.Context, tarball, extractDir string) (string, error) {
	if err := archive.Extract(ctx, tarball, extractDir); err != nil {
		return "", errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Extraction failed: %s", err))
	}
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}
	// Find the most recently created linux-* directory (the one just extracted).
	// Use the newest dir to avoid picking up stale directories from prior builds.
	var newest string
	var newestMod time.Time
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "linux-") {
			fi, err := entry.Info()
			if err != nil {
				continue
			}
			mod := fi.ModTime()
			if newest == "" || mod.After(newestMod) {
				newest = filepath.Join(extractDir, entry.Name())
				newestMod = mod
			}
		}
	}
	if newest != "" {
		return newest, nil
	}
	return "", errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("No linux-* directory found in kernel tarball"))
}

// --- Remote Version Listing ---

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
		parts := strings.SplitN(key, "-", 2)
		name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
		if len(parts) > 1 {
			name += " " + parts[1]
		}
		if strings.HasPrefix(key, "official") {
			name += " (build required)"
		}

		converted := make([]model.VersionInfo, len(versions))
		for i, v := range versions {
			converted[i] = model.VersionInfo{
				Version:     v.Version,
				DownloadURL: v.DownloadURL,
				SHA256URL:   v.SHA256URL,
				DisplayName: "",
				Type:        key,
				Format:      v.Format,
				Name:        name,
			}
		}
		result[key] = converted
	}
	return result
}

// --- Internal helpers ---

func (s *Service) downloadFCConfig(
	ctx context.Context,
	kernelDir string,
	spec *model.KernelSpec,
	vars map[string]string,
) error {
	if spec.BaseConfigURLTemplate == nil || *spec.BaseConfigURLTemplate == "" {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Missing 'base_config_url_template' in kernels.yaml for %s", spec.Name),
		)
	}
	url, err := infra.RenderTemplate(*spec.BaseConfigURLTemplate, vars)
	if err != nil {
		return errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to render config URL template: %s", err))
	}
	data, err := s.dl.GetBody(ctx, url)
	if err != nil {
		return errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to download config: %s", err))
	}
	return os.WriteFile(filepath.Join(kernelDir, ".config"), data, 0644)
}

func (s *Service) applyConfigFragments(
	ctx context.Context,
	kernelDir string,
	fragments []string,
	vars map[string]string,
	onProgress event.OnProgressCallback,
) error {
	configPath := filepath.Join(kernelDir, ".config")
	for idx, fragment := range fragments {
		rendered, err := infra.RenderTemplate(fragment, vars)
		if err != nil {
			return errs.New(
				errs.CodeKernelBuildFailed,
				fmt.Sprintf("Failed to render config fragment %d: %s", idx, err),
			)
		}
		var content []byte

		if strings.HasPrefix(rendered, "http://") || strings.HasPrefix(rendered, "https://") {
			if onProgress != nil {
				onProgress(
					event.Progress{
						Phase:   "build",
						Status:  "running",
						Message: fmt.Sprintf("Fetching config fragment: %s", rendered),
					},
				)
			}
			content, err = s.dl.GetBody(ctx, rendered)
			if err != nil {
				return errs.New(
					errs.CodeKernelBuildFailed,
					fmt.Sprintf("Failed to fetch config fragment %s: %s", rendered, err),
				)
			}
		} else {
			rel := strings.TrimPrefix(rendered, "assets/")
			if onProgress != nil {
				onProgress(
					event.Progress{
						Phase:   "build",
						Status:  "running",
						Message: fmt.Sprintf("Applying config fragment: %s", rel),
					},
				)
			}
			content, err = assets.ReadFile(rel)
			if err != nil {
				return errs.New(
					errs.CodeKernelBuildFailed,
					fmt.Sprintf("Config fragment not found: %s (from '%s')", rel, fragment),
				)
			}
		}

		if idx == 0 {
			if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
				baseContent := string(content)
				if !strings.HasSuffix(baseContent, "\n") {
					baseContent += "\n"
				}
				if err := os.WriteFile(configPath, []byte(baseContent), 0644); err != nil {
					return errs.New(
						errs.CodeKernelBuildFailed,
						fmt.Sprintf("Failed to write base config fragment: %s", err),
					)
				}
				continue
			}
		}
		if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
			if err := os.WriteFile(configPath, []byte{}, 0644); err != nil {
				return errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Failed to create empty .config: %s", err))
			}
		}
		mergeConfigLines(string(content), configPath)
	}
	return nil
}

// --- Caching helpers ---

func (s *Service) computeConfigHash(
	spec *model.KernelSpec,
	version string,
	userConfigPath *string,
	featureEnforces map[string]string,
) string {
	// Sort keys for deterministic hashing
	defaultKeys := make([]string, 0, len(spec.DefaultConfigs))
	for k := range spec.DefaultConfigs {
		defaultKeys = append(defaultKeys, k)
	}
	sort.Strings(defaultKeys)
	var defaultParts []string
	for _, k := range defaultKeys {
		defaultParts = append(defaultParts, fmt.Sprintf("%s=%s", k, spec.DefaultConfigs[k]))
	}

	featureKeys := make([]string, 0, len(featureEnforces))
	for k := range featureEnforces {
		featureKeys = append(featureKeys, k)
	}
	sort.Strings(featureKeys)
	var featureParts []string
	for _, k := range featureKeys {
		featureParts = append(featureParts, fmt.Sprintf("%s=%s", k, featureEnforces[k]))
	}

	hash := crypto.ContentHash(
		version,
		fmt.Sprintf("%v", spec.ConfigFragments),
		strings.Join(defaultParts, ","),
		strings.Join(featureParts, ","),
	)
	if userConfigPath != nil {
		data, err := os.ReadFile(*userConfigPath)
		if err == nil {
			hash = crypto.ContentHash(hash, string(data))
		}
	}
	return crypto.Truncate(hash, 16)
}

func tryCacheHit(outputPath, cacheMarker, cachedKernelPath string, useCache bool) bool {
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
		slog.Info("Kernel exists but config changed, rebuilding", "path", outputPath)
		os.Remove(outputPath)
	}
	return false
}

// --- Import Kernel ---

func (s *Service) ImportKernel(
	ctx context.Context,
	name string,
	sourcePath string,
	version string,
	arch string,
	setDefault bool,
) (*model.KernelItem, error) {
	resolvedPath, err := system.ExpandAndResolve(sourcePath)
	if err != nil {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to resolve source path '%s': %s", sourcePath, err),
		)
	}

	kernelsDir := filepath.Join(s.cacheDir, "kernels")
	if err := os.MkdirAll(kernelsDir, infra.DirPerm); err != nil {
		return nil, err
	}

	destFilename := fmt.Sprintf(KernelOutputPattern, name, version, arch)
	destPath := filepath.Join(kernelsDir, destFilename)

	if err := infra.CopyPreservingMetadata(resolvedPath, destPath); err != nil {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to copy kernel file to %s: %s", destPath, err),
		)
	}
	os.Chmod(destPath, 0755)

	// Compute deterministic ID from the copied file (no timestamp)
	kernelID, err := crypto.KernelID(destPath, version, arch)
	if err != nil {
		return nil, fmt.Errorf("compute kernel ID: %w", err)
	}

	now := time.Now().Format(time.RFC3339)

	kernelItem := &model.KernelItem{
		ID:        kernelID,
		Name:      fmt.Sprintf("%s %s", name, version),
		BaseName:  name,
		Version:   version,
		Arch:      arch,
		Type:      "custom",
		Path:      destPath,
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

// --- Version Resolution ---

func (s *Service) ResolveVersion(
	ctx context.Context,
	kernelType string,
	versionSpec string,
	arch string,
	ciVersion string,
) (string, error) {
	specs, err := s.LoadSpecs()
	if err != nil {
		return "", err
	}

	var matching []*model.KernelSpec
	for _, spec := range specs {
		if spec.KernelType == kernelType {
			matching = append(matching, spec)
		}
	}
	if len(matching) == 0 {
		return "", errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Cannot resolve version for unknown type: %s", kernelType),
		)
	}

	// Parse the version spec
	spec, err := version.ParseSpec(versionSpec)
	if err != nil {
		return "", errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf("Invalid version spec %q: %s", versionSpec, err))
	}

	// Exact version — no resolution needed
	if !spec.IsPartial() {
		return strings.TrimPrefix(versionSpec, "v"), nil
	}

	// Partial or latest — fetch available versions and resolve
	configs := kernelSpecsToResolverConfigs(matching)
	versionMap := s.resolver.Resolve(ctx, configs, arch, ciVersion, 0, 10)

	var allVersions []string
	for _, versions := range versionMap {
		for _, v := range versions {
			allVersions = append(allVersions, v.Version)
		}
	}
	if len(allVersions) == 0 {
		return "", errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Cannot resolve version %q for %s: no versions available from upstream", versionSpec, kernelType))
	}

	resolved, err := version.Resolve(allVersions, spec)
	if err != nil {
		return "", errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Cannot resolve version %q for %s: %s", versionSpec, kernelType, err),
		)
	}
	return resolved, nil
}
