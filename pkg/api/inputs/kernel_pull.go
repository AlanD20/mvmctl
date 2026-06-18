package inputs
import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
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
// KernelPullRequest specifies kernel pull request.
// Resolve and validate kernel pull/build inputs.
type KernelPullRequest struct {
	cfg    *config.Service
	input  KernelPullInput
	result *ResolvedKernelPullRequest
}
// NewKernelPullRequest creates a new KernelPullRequest.
func NewKernelPullRequest(inputs KernelPullInput, cfg *config.Service) *KernelPullRequest {
	return &KernelPullRequest{
		cfg:   cfg,
		input: inputs,
	}
}
// Result returns the resolved request, or nil if resolve() has not been called.
// Resolve resolves all inputs to explicit values.
func (r *KernelPullRequest) Resolve(ctx context.Context) (*ResolvedKernelPullRequest, error) {
	// Resolve version — use explicit input or fallback to default.
	version := r.input.Version
	if version == "" {
		version, _ = r.cfg.GetString(ctx, "defaults.kernel", "version")
	}
	// Strip "v" prefix from explicit version strings.
	if r.input.KernelType == "firecracker" {
		version = ""
	} else if version != "" {
		version = strings.TrimPrefix(version, "v")
	}
	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()
	// Resolve jobs — use explicit input, setting, or CPU count.
	var jobs int
	if r.input.Jobs != 0 {
		jobs = r.input.Jobs
	} else {
		// Default to all cores when not configured
		jobs, _ = r.cfg.GetInt(ctx, "defaults.kernel", "build_jobs")
	}
	// Fallback for constrained environments where NumCPU() returns 0
	if jobs == 0 {
		jobs = runtime.NumCPU()
	}
	if jobs == 0 {
		jobs = 1
	}
	// Resolve features from comma-separated input string.
	featuresRaw := strings.TrimSpace(r.input.Features)
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
	nestedVirtBool, _ := r.cfg.GetBool(ctx, "defaults.vm", "nested_virt")
	if nestedVirtBool {
		if !slices.Contains(featuresList, "kvm") {
			featuresList = append([]string{"kvm"}, featuresList...)
		}
	}
	// Resolve output directory — use explicit input or the default kernels dir.
	outputDir := infra.GetKernelsDir()
	if r.input.OutputDir != "" {
		outputDir = r.input.OutputDir
	}
	var kernelConfig *string
	if r.input.KernelConfig != "" {
		kernelConfig = &r.input.KernelConfig
	}
	r.result = &ResolvedKernelPullRequest{
		KernelType:   r.input.KernelType,
		Version:      version,
		Arch:         arch,
		OutputDir:    outputDir,
		Jobs:         jobs,
		KeepBuildDir: r.input.KeepBuildDir,
		CleanBuild:   r.input.CleanBuild,
		KernelConfig: kernelConfig,
		SetDefault:   r.input.SetDefault,
		Features:     featuresList,
	}
	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}
	return r.result, nil
}
func (r *KernelPullRequest) ensureValidate() error {
	if r.result == nil {
		return errs.New(
			errs.CodeKernelBuildFailed,
			"Failed to resolve necessary dependencies to validate",
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 1. Validate kernel type
	validTypes := kernel.KernelValidTypes
	if !validTypes[r.result.KernelType] {
		return errs.New(errs.CodeKernelBuildFailed,
			fmt.Sprintf("Unsupported kernel type: %s. Valid types: firecracker, official", r.result.KernelType),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 2. Validate version (semver-like: 5.10, 6.1.0, v6.1).
	if r.result.Version != "" {
		stripped := strings.TrimPrefix(r.result.Version, "v")
		if !version.IsValidVersion(stripped) {
			return errs.New(
				errs.CodeKernelBuildFailed,
				fmt.Sprintf(
					"Invalid kernel version: '%s'. Expected format like '5.10', '6.1.0', or 'v6.1'",
					r.result.Version,
				),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	// 3. Validate architecture
	if !slices.Contains(infra.FirecrackerSupportedArches, r.result.Arch) {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf(
				"Unsupported architecture: %s. Valid architectures: %s",
				r.result.Arch,
				strings.Join(infra.FirecrackerSupportedArches, ", "),
			),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 4. Validate output directory (must exist or be creatable) —
	// if output_dir.exists() and not output_dir.is_dir():
	if r.result.OutputDir != "" {
		if fi, err := os.Stat(r.result.OutputDir); err == nil && !fi.IsDir() {
			return errs.New(
				errs.CodeKernelBuildFailed,
				fmt.Sprintf("Output path exists but is not a directory: %s", r.result.OutputDir),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	// 5. Validate build jobs (positive integer) —
	// if jobs <= 0:
	if r.result.Jobs <= 0 {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Invalid build jobs: %d. Must be a positive integer.", r.result.Jobs),
			errs.WithClass(errs.ClassValidation),
		)
	}
	return nil
}
