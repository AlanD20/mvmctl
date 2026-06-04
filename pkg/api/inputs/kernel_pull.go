package inputs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/system"
)

// KernelPullInput matches Python's KernelPullInput dataclass.
//
//	@dataclass
//	class KernelPullInput:
//	    kernel_type: str
//	    version: str | None = None
//	    output_dir: Path | None = None
//	    output_name: str | None = None
//	    output_path: Path | None = None
//	    jobs: int | None = None
//	    keep_build_dir: bool = False
//	    clean_build: bool = False
//	    kernel_config: Path | None = None
//	    set_default: bool = False
//	    features: str = ""
type KernelPullInput struct {
	KernelType   string `json:"kernel_type"`
	Version      string `json:"version,omitempty"`
	OutputDir    string `json:"output_dir,omitempty"`
	OutputName   string `json:"output_name,omitempty"`
	OutputPath   string `json:"output_path,omitempty"`
	Jobs         int    `json:"jobs,omitempty"`
	KeepBuildDir bool   `json:"keep_build_dir"`
	CleanBuild   bool   `json:"clean_build"`
	KernelConfig string `json:"kernel_config,omitempty"`
	SetDefault   bool   `json:"set_default"`
	Features     string `json:"features"`
}

// ResolvedKernelPullRequest matches Python's ResolvedKernelPullRequest (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedKernelPullRequest:
//	    kernel_type: str
//	    arch: str
//	    output_dir: Path
//	    jobs: int
//	    keep_build_dir: bool
//	    clean_build: bool
//	    set_default: bool
//	    kernel_config: Path | None
//	    version: str | None = None
//	    features: list[str] = field(default_factory=list)
type ResolvedKernelPullRequest struct {
	KernelType   string
	Arch         string
	OutputDir    string
	Jobs         int
	KeepBuildDir bool
	CleanBuild   bool
	SetDefault   bool
	KernelConfig *string
	Version      *string
	Features     []string
}

// KernelPullRequest matches Python's KernelPullRequest.
//
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
// Matches Python's KernelPullRequest.resolve() exactly.
func (r *KernelPullRequest) Resolve(ctx context.Context) (*ResolvedKernelPullRequest, error) {
	// Resolve version — Python:
	//   if self._inputs.version is not None → version = self._inputs.version
	//   else → version = SettingsService.resolve(self._db, "defaults.kernel", "version")
	var version *string
	if r.input.Version != "" {
		v := r.input.Version
		version = &v
	} else {
		s := r.cfg.GetString(ctx, "defaults.kernel", "version", "")
		if s != "" {
			version = &s
		}
	}

	// Python: if self._inputs.kernel_type == "firecracker": version = None
	//         elif version is not None: version = version.removeprefix("v")
	if r.input.KernelType == "firecracker" {
		version = nil
	} else if version != nil {
		v := strings.TrimPrefix(*version, "v")
		version = &v
	}

	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()

	// Resolve jobs — Python:
	//   if self._inputs.jobs is not None → jobs = self._inputs.jobs
	//   else → jobs = SettingsService.resolve(self._db, "defaults.kernel", "build_jobs")
	//   if jobs is None → jobs = os.cpu_count() or SettingsService.resolve(...)
	var jobs int
	if r.input.Jobs != 0 {
		jobs = r.input.Jobs
	} else {
		// Default to all cores when not configured
		jobs = r.cfg.GetInt(ctx, "defaults.kernel", "build_jobs", runtime.NumCPU())
	}
	// Fallback for constrained environments where NumCPU() returns 0
	if jobs == 0 {
		jobs = runtime.NumCPU()
	}
	if jobs == 0 {
		jobs = 1
	}

	// Resolve features from comma-separated input string — Python:
	//   features_raw = (self._inputs.features or "").strip()
	//   features_list = ([f.strip() for f in features_raw.split(",") if f.strip()] if features_raw else [])
	featuresRaw := strings.TrimSpace(r.input.Features)
	var featuresList []string
	if featuresRaw != "" {
		for _, f := range strings.Split(featuresRaw, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				featuresList = append(featuresList, f)
			}
		}
	}

	// Auto-include "kvm" when defaults.vm.nested_virt is enabled — Python:
	//   nested_virt = bool(SettingsService.resolve(self._db, "defaults.vm", "nested_virt"))
	//   if nested_virt and "kvm" not in features_list: features_list.insert(0, "kvm")
	// Python's bool() is truthy for many types: non-empty strings, non-zero numbers,
	// non-None objects. In Go, we just check for a truthy config value.
	nestedVirtBool := r.cfg.GetBool(ctx, "defaults.vm", "nested_virt", false)
	if nestedVirtBool {
		hasKVM := false
		for _, f := range featuresList {
			if f == "kvm" {
				hasKVM = true
				break
			}
		}
		if !hasKVM {
			featuresList = append([]string{"kvm"}, featuresList...)
		}
	}

	// Resolve output_dir — Python: self._inputs.output_dir or CacheUtils.get_kernels_dir()
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
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_pull",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	// 1. Validate kernel type
	validTypes := kernel.KernelValidTypes
	if !validTypes[r.result.KernelType] {
		return &errs.DomainError{
			Code: errs.CodeKernelBuildFailed,
			Op:   "kernel_pull",
			Message: fmt.Sprintf(
				"Unsupported kernel type: %s. Valid types: firecracker, official",
				r.result.KernelType,
			),
			Class: errs.ClassValidation,
		}
	}

	// 2. Validate version (semver-like: 5.10, 6.1.0, v6.1) — Python:
	//    version = self.result.version
	//    if version:
	//        stripped = version.removeprefix("v")
	//        if not re.fullmatch(r"\d+(\.\d+)*", stripped):
	if r.result.Version != nil {
		stripped := strings.TrimPrefix(*r.result.Version, "v")
		if !isValidVersion(stripped) {
			return &errs.DomainError{
				Code: errs.CodeKernelBuildFailed,
				Op:   "kernel_pull",
				Message: fmt.Sprintf(
					"Invalid kernel version: '%s'. Expected format like '5.10', '6.1.0', or 'v6.1'",
					*r.result.Version,
				),
				Class: errs.ClassValidation,
			}
		}
	}

	// 3. Validate architecture
	archOk := false
	for _, a := range infra.FirecrackerSupportedArches {
		if r.result.Arch == a {
			archOk = true
			break
		}
	}
	if !archOk {
		return &errs.DomainError{
			Code: errs.CodeKernelBuildFailed,
			Op:   "kernel_pull",
			Message: fmt.Sprintf(
				"Unsupported architecture: %s. Valid architectures: %s",
				r.result.Arch, strings.Join(infra.FirecrackerSupportedArches, ", "),
			),
			Class: errs.ClassValidation,
		}
	}

	// 4. Validate output directory (must exist or be creatable) — Python:
	//    if output_dir.exists() and not output_dir.is_dir():
	if r.result.OutputDir != "" {
		if fi, err := os.Stat(r.result.OutputDir); err == nil && !fi.IsDir() {
			return &errs.DomainError{
				Code:    errs.CodeKernelBuildFailed,
				Op:      "kernel_pull",
				Message: fmt.Sprintf("Output path exists but is not a directory: %s", r.result.OutputDir),
				Class:   errs.ClassValidation,
			}
		}
	}

	// 5. Validate build jobs (positive integer) — Python:
	//    if jobs <= 0:
	if r.result.Jobs <= 0 {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_pull",
			Message: fmt.Sprintf("Invalid build jobs: %d. Must be a positive integer.", r.result.Jobs),
			Class:   errs.ClassValidation,
		}
	}

	return nil
}

// versionRegex matches valid semver-like version strings (e.g., "5.10", "6.1.0").
var versionRegex = regexp.MustCompile(`^\d+(\.\d+)*$`)

func isValidVersion(v string) bool {
	return v != "" && versionRegex.MatchString(v)
}
