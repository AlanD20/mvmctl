package inputs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"

	"github.com/jmoiron/sqlx"
)

// KernelPullInput matches Python's KernelPullInput dataclass.
//
//	@dataclass
//	class KernelPullInput:
//	    kernel_type: str
//	    version: str | None = None
//	    arch: str | None = None
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
	KernelType   string  `json:"kernel_type"`
	Version      *string `json:"version,omitempty"`
	Arch         *string `json:"arch,omitempty"`
	OutputDir    *string `json:"output_dir,omitempty"`
	OutputName   *string `json:"output_name,omitempty"`
	OutputPath   *string `json:"output_path,omitempty"`
	Jobs         *int    `json:"jobs,omitempty"`
	KeepBuildDir bool    `json:"keep_build_dir"`
	CleanBuild   bool    `json:"clean_build"`
	KernelConfig *string `json:"kernel_config,omitempty"`
	SetDefault   bool    `json:"set_default"`
	Features     string  `json:"features"`
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
	db     *sqlx.DB
	input  KernelPullInput
	result *ResolvedKernelPullRequest
}

// NewKernelPullRequest creates a new KernelPullRequest.
func NewKernelPullRequest(inputs KernelPullInput, db *sqlx.DB) *KernelPullRequest {
	return &KernelPullRequest{
		db:    db,
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
	if r.input.Version != nil {
		v := *r.input.Version
		version = &v
	} else {
		v, err := config.Resolve(ctx, r.db, "defaults.kernel", "version")
		if err == nil && v != nil {
			s := toString(v)
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

	// Resolve arch — Python:
	//   if self._inputs.arch is not None → arch = self._inputs.arch
	//   else → arch = SettingsService.resolve(self._db, "defaults.kernel", "arch")
	var arch string
	if r.input.Arch != nil {
		arch = *r.input.Arch
	} else {
		v, err := config.Resolve(ctx, r.db, "defaults.kernel", "arch")
		if err == nil && v != nil {
			arch = toString(v)
		}
	}

	// Python falls through to whatever arch is (no fallback for pull that's different from import)

	// Resolve jobs — Python:
	//   if self._inputs.jobs is not None → jobs = self._inputs.jobs
	//   else → jobs = SettingsService.resolve(self._db, "defaults.kernel", "build_jobs")
	//   if jobs is None → jobs = os.cpu_count() or SettingsService.resolve(...)
	var jobs int
	if r.input.Jobs != nil {
		jobs = *r.input.Jobs
	} else {
		v, err := config.Resolve(ctx, r.db, "defaults.kernel", "build_jobs")
		if err == nil && v != nil {
			if i, ok := v.(int64); ok {
				jobs = int(i)
			}
		}
	}
	if jobs == 0 {
		// Python: os.cpu_count() — can return None in constrained environments
		jobs = runtime.NumCPU()
	}
	if jobs == 0 {
		// Python: or SettingsService.resolve(...)
		v, err := config.Resolve(ctx, r.db, "defaults.kernel", "build_jobs")
		if err == nil && v != nil {
			if i, ok := v.(int64); ok {
				jobs = int(i)
			}
		}
	}
	// Python's os.cpu_count() returns None in some constrained environments.
	// Go's runtime.NumCPU() returns the number of logical CPUs, always >= 1 on Linux,
	// but we keep the fallback for compatibility.

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
	//
	// Python's bool() is truthy for many types: non-empty strings, non-zero numbers,
	// non-None objects. We replicate that behavior with pythonBool().
	nestedVirt, _ := config.Resolve(ctx, r.db, "defaults.vm", "nested_virt")
	nestedVirtBool := pythonBool(nestedVirt)
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
	if r.input.OutputDir != nil && *r.input.OutputDir != "" {
		outputDir = *r.input.OutputDir
	}

	r.result = &ResolvedKernelPullRequest{
		KernelType:   r.input.KernelType,
		Version:      version,
		Arch:         arch,
		OutputDir:    outputDir,
		Jobs:         jobs,
		KeepBuildDir: r.input.KeepBuildDir,
		CleanBuild:   r.input.CleanBuild,
		KernelConfig: r.input.KernelConfig,
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

	// 1. Validate kernel type — Python:
	//    valid_types = ("firecracker", "official")
	//    if self.result.kernel_type not in valid_types:
	validTypes := map[string]bool{"firecracker": true, "official": true}
	if !validTypes[r.result.KernelType] {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_pull",
			Message: fmt.Sprintf("Unsupported kernel type: %s. Valid types: firecracker, official", r.result.KernelType),
			Class:   errs.ClassValidation,
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
				Code:    errs.CodeKernelBuildFailed,
				Op:      "kernel_pull",
				Message: fmt.Sprintf("Invalid kernel version: '%s'. Expected format like '5.10', '6.1.0', or 'v6.1'", *r.result.Version),
				Class:   errs.ClassValidation,
			}
		}
	}

	// 3. Validate architecture — Python:
	//    valid_archs = ("x86_64", "amd64", "arm64", "aarch64")
	//    if self.result.arch not in valid_archs:
	validArchs := map[string]bool{"x86_64": true, "amd64": true, "arm64": true, "aarch64": true}
	if !validArchs[r.result.Arch] {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_pull",
			Message: fmt.Sprintf("Unsupported architecture: %s. Valid architectures: x86_64, amd64, arm64, aarch64", r.result.Arch),
			Class:   errs.ClassValidation,
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

// isValidVersion matches Python's re.fullmatch(r"\d+(\.\d+)*", stripped).
func isValidVersion(v string) bool {
	if v == "" {
		return false
	}
	return regexp.MustCompile(`^\d+(\.\d+)*$`).MatchString(v)
}

// resolveSettingIntFallback resolves an integer setting, returning 0 if not found.
func resolveSettingIntFallback(ctx context.Context, db *sqlx.DB, category, key string) int {
	v, err := config.Resolve(ctx, db, category, key)
	if err == nil && v != nil {
		if i, ok := v.(int64); ok {
			return int(i)
		}
	}
	return 0
}

// pythonBool replicates Python's bool() behavior which is truthy for many types:
//   - None/false -> false
//   - non-empty strings -> true
//   - non-zero numbers -> true
//   - non-nil non-bool objects -> true
//
// Matches Python: bool(value)
func pythonBool(v interface{}) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val != ""
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	default:
		return true
	}
}
