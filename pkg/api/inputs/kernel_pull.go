package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// KernelPullInput specifies kernel pull input.
type KernelPullInput struct {
	KernelType   string `json:"type"                    yaml:"type"`
	Version      string `json:"version,omitempty"       yaml:"version,omitempty"`
	OutputDir    string `json:"output_dir,omitempty"`
	OutputName   string `json:"name,omitempty"          yaml:"name,omitempty"`
	OutputPath   string `json:"output_path,omitempty"`
	Jobs         int    `json:"jobs,omitempty"          yaml:"jobs,omitempty"`
	KeepBuildDir bool   `json:"keep_build_dir"          yaml:"keep_build_dir"`
	CleanBuild   bool   `json:"clean_build"             yaml:"clean_build"`
	KernelConfig string `json:"kernel_config,omitempty" yaml:"kernel_config,omitempty"`
	SetDefault   bool   `json:"default"                 yaml:"default"`
	Features     string `json:"features"                yaml:"features"`
	SkipChecksum bool   `json:"skip_checksum,omitempty"`
}

// ResolvedKernelPullRequest specifies resolved kernel pull request.
type ResolvedKernelPullRequest struct {
	KernelType   string
	Arch         string
	OutputDir    string
	Jobs         int
	KeepBuildDir bool
	CleanBuild   bool
	SetDefault   bool
	KernelConfig *string
	Version      string
	Features     []string
	SkipChecksum bool
}

// Validate checks that the kernel pull input has valid fields.
func (i *KernelPullInput) Validate() error {
	// Validate kernel type
	if !kernel.KernelValidTypes[i.KernelType] {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Unsupported kernel type: %s. Valid types: firecracker, official", i.KernelType),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Validate version (semver-like: 5.10, 6.1.0, v6.1).
	if i.Version != "" {
		stripped := strings.TrimPrefix(i.Version, "v")
		if !version.IsValidVersion(stripped) {
			return errs.New(
				errs.CodeKernelBuildFailed,
				fmt.Sprintf("Invalid kernel version: '%s'. Expected format like '5.10', '6.1.0', or 'v6.1'", i.Version),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	return nil
}

// Resolve resolves and validates pull inputs, returning a ResolvedKernelPullRequest.
func (i *KernelPullInput) Resolve(ctx context.Context, cfg *config.Service) (*ResolvedKernelPullRequest, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Resolve version — use explicit input or fallback to default.
	version := i.Version
	if version == "" {
		version, _ = cfg.GetString(ctx, "defaults.kernel", "version")
	}
	// Strip "v" prefix from explicit version strings.
	if i.KernelType == "firecracker" {
		version = ""
	} else if version != "" {
		version = strings.TrimPrefix(version, "v")
	}
	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()
	// Resolve jobs — use explicit input, setting, or CPU count.
	var jobs int
	if i.Jobs != 0 {
		jobs = i.Jobs
	} else {
		// Default to all cores when not configured
		jobs, _ = cfg.GetInt(ctx, "defaults.kernel", "build_jobs")
	}
	// Fallback for constrained environments where NumCPU() returns 0
	if jobs == 0 {
		jobs = runtime.NumCPU()
	}
	if jobs == 0 {
		jobs = 1
	}
	// Resolve features from comma-separated input string.
	featuresRaw := strings.TrimSpace(i.Features)
	var featuresList []string
	if featuresRaw != "" {
		for f := range strings.SplitSeq(featuresRaw, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				featuresList = append(featuresList, f)
			}
		}
	}
	// Auto-include "kvm" feature when nested virtualization is enabled
	// (configured via "defaults.vm" setting).
	nestedVirtBool, _ := cfg.GetBool(ctx, "defaults.vm", "nested_virt")
	if nestedVirtBool {
		if !slices.Contains(featuresList, "kvm") {
			featuresList = append([]string{"kvm"}, featuresList...)
		}
	}
	// Resolve output directory — use explicit input or the default kernels dir.
	outputDir := infra.GetKernelsDir()
	if i.OutputDir != "" {
		outputDir = i.OutputDir
	}
	var kernelConfig *string
	if i.KernelConfig != "" {
		kernelConfig = &i.KernelConfig
	}
	result := &ResolvedKernelPullRequest{
		KernelType:   i.KernelType,
		Version:      version,
		Arch:         arch,
		OutputDir:    outputDir,
		Jobs:         jobs,
		KeepBuildDir: i.KeepBuildDir,
		CleanBuild:   i.CleanBuild,
		KernelConfig: kernelConfig,
		SetDefault:   i.SetDefault,
		Features:     featuresList,
		SkipChecksum: i.SkipChecksum,
	}
	// Validate architecture
	if !firecracker.SupportsArch(result.Arch) {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf(
				"Unsupported architecture: %s. Valid architectures: %s",
				result.Arch,
				strings.Join(firecracker.SupportedArches, ", "),
			),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Validate output directory (must exist or be creatable) —
	// if output_dir.exists() and not output_dir.is_dir():
	if result.OutputDir != "" {
		if fi, err := os.Stat(result.OutputDir); err == nil && !fi.IsDir() {
			return nil, errs.New(
				errs.CodeKernelBuildFailed,
				fmt.Sprintf("Output path exists but is not a directory: %s", result.OutputDir),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	// Validate build jobs (positive integer) —
	// if jobs <= 0:
	if result.Jobs <= 0 {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Invalid build jobs: %d. Must be a positive integer.", result.Jobs),
			errs.WithClass(errs.ClassValidation),
		)
	}
	return result, nil
}

// ResolveFeatures expands wildcards and validates feature names against the spec.
// If "all" or "*" is in the requested list, it's replaced with all sorted keys from specFeatures.
// Each feature name is validated against specFeatures; unknown names return an error.
// The result is deduplicated while preserving the original order.
func ResolveFeatures(requested []string, specFeatures map[string]model.KernelFeature) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}

	var result []string
	seen := make(map[string]bool)

	for _, f := range requested {
		if f == "all" || f == "*" {
			// Expand wildcard: add all spec feature names, sorted for determinism
			names := make([]string, 0, len(specFeatures))
			for name := range specFeatures {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !seen[name] {
					result = append(result, name)
					seen[name] = true
				}
			}
		} else {
			if seen[f] {
				continue
			}
			if _, ok := specFeatures[f]; !ok {
				valid := make([]string, 0, len(specFeatures))
				for name := range specFeatures {
					valid = append(valid, name)
				}
				sort.Strings(valid)
				return nil, errs.New(
					errs.CodeKernelBuildFailed,
					fmt.Sprintf("unknown kernel feature: %q. Valid features: %s", f, strings.Join(valid, ", ")),
					errs.WithClass(errs.ClassValidation),
				)
			}
			result = append(result, f)
			seen[f] = true
		}
	}

	return result, nil
}
