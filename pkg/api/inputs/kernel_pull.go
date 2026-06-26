package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
	"os"
	"runtime"
	"slices"
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
	// Validate feature names (only for official builds) —
	if result.KernelType == "official" && len(result.Features) > 0 {
		valid := kernel.KernelValidFeatures
		var invalid []string
		for _, f := range result.Features {
			if !valid[f] {
				invalid = append(invalid, f)
			}
		}
		if len(invalid) > 0 {
			msg := fmt.Sprintf(
				"Unknown kernel features: %s. Valid features: kvm, nftables, tuntap, btrfs",
				strings.Join(invalid, ", "),
			)
			return nil, errs.New(errs.CodeKernelBuildFailed, msg, errs.WithClass(errs.ClassValidation))
		}
	}
	return result, nil
}
