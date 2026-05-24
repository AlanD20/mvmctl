package inputs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra/errs"
)

// KernelImportInput matches Python's KernelImportInput dataclass.
//
//	@dataclass
//	class KernelImportInput:
//	    name: str
//	    path: Path
//	    version: str | None = None
//	    arch: str | None = None
//	    set_default: bool = False
type KernelImportInput struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Version    *string `json:"version,omitempty"`
	Arch       *string `json:"arch,omitempty"`
	SetDefault bool    `json:"set_default"`
}

// ResolvedKernelImportInput matches Python's ResolvedKernelImportInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedKernelImportInput:
//	    name: str
//	    path: Path
//	    version: str
//	    arch: str
//	    set_default: bool = False
type ResolvedKernelImportInput struct {
	Name       string
	Path       string
	Version    string
	Arch       string
	SetDefault bool
}

// KernelImportRequest matches Python's KernelImportRequest.
//
// Resolve and validate kernel import inputs.
type KernelImportRequest struct {
	db      *sql.DB
	_input  KernelImportInput
	_result *ResolvedKernelImportInput
}

// NewKernelImportRequest creates a new KernelImportRequest.
func NewKernelImportRequest(inputs KernelImportInput, db *sql.DB) *KernelImportRequest {
	return &KernelImportRequest{
		db:     db,
		_input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *KernelImportRequest) Result() *ResolvedKernelImportInput {
	return r._result
}

// Resolve resolves all input fields to concrete values and validates.
// Matches Python's KernelImportRequest.resolve().
func (r *KernelImportRequest) Resolve(ctx context.Context) (*ResolvedKernelImportInput, error) {
	// Expand and resolve path — Python: source_path = self._inputs.path.expanduser().resolve()
	// Python's Path.resolve() follows symlinks and resolves to an absolute path.
	sourcePath := r._input.Path
	if strings.HasPrefix(sourcePath, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			sourcePath = filepath.Join(home, sourcePath[1:])
		}
	}
	absPath, err := filepath.Abs(sourcePath)
	if err == nil {
		sourcePath = absPath
	}
	// Python: Path.resolve() follows symlinks
	resolvedPath, err := filepath.EvalSymlinks(sourcePath)
	if err == nil {
		sourcePath = resolvedPath
	}

	// Python: parsed = KernelService.parse_filename(source_path.name)
	parsedVersion, parsedArch := parseKernelFilename(filepath.Base(sourcePath))

	// Resolve arch — Python logic:
	//   if self._inputs.arch is not None → arch = self._inputs.arch
	//   elif parsed.arch != "-" → arch = parsed.arch
	//   else → arch = str(SettingsService.resolve(...))
	//          if not arch → arch = platform.machine()
	var arch string
	if r._input.Arch != nil && *r._input.Arch != "" {
		arch = *r._input.Arch
	} else if parsedArch != "" && parsedArch != "-" {
		arch = parsedArch
	} else {
		v, err := config.Resolve(ctx, r.db, "defaults.kernel", "arch")
		if err == nil && v != nil {
			arch = toString(v)
		}
		if arch == "" {
			// Python: platform.machine() returns machine hardware name
			// e.g. "x86_64", "aarch64"
			arch = platformMachine()
		}
	}

	// Resolve version — Python logic:
	//   if self._inputs.version is not None → version = self._inputs.version
	//   else → version = parsed.version if parsed.version != "-" else "unknown"
	var version string
	if r._input.Version != nil && *r._input.Version != "" {
		version = *r._input.Version
	} else if parsedVersion != "" && parsedVersion != "-" {
		version = parsedVersion
	} else {
		version = "unknown"
	}

	r._result = &ResolvedKernelImportInput{
		Name:       r._input.Name,
		Path:       sourcePath,
		Version:    version,
		Arch:       arch,
		SetDefault: r._input.SetDefault,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r._result, nil
}

// ensureValidate matches Python's KernelImportRequest.ensure_validate() exactly.
func (r *KernelImportRequest) ensureValidate() error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	// 1. Path exists — Python: if not self._result.path.exists()
	if _, err := os.Stat(r._result.Path); os.IsNotExist(err) {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Kernel file not found: %s", r._result.Path),
			Class:   errs.ClassValidation,
		}
	}

	// 2. Path is non-empty — Python: if self._result.path.stat().st_size == 0
	fi, err := os.Stat(r._result.Path)
	if err == nil && fi.Size() == 0 {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Kernel file is empty: %s", r._result.Path),
			Class:   errs.ClassValidation,
		}
	}

	// 3. Arch is supported — Python: if self._result.arch not in FIRECRACKER_SUPPORTED_ARCH
	// Python: FIRECRACKER_SUPPORTED_ARCH = ["x86_64", "amd64", "aarch64", "arm64"]
	validArchs := map[string]bool{"x86_64": true, "amd64": true, "aarch64": true, "arm64": true}
	if !validArchs[r._result.Arch] {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Unknown arch: %s. Valid: x86_64, amd64, aarch64, arm64", r._result.Arch),
			Class:   errs.ClassValidation,
		}
	}

	// 4. Name is non-empty — Python: if not self._result.name
	if r._result.Name == "" {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: "Kernel name cannot be empty",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}

// platformMachine returns the machine hardware name, matching Python's platform.machine().
// On Linux: "x86_64" for amd64, "aarch64" for arm64, etc.
func platformMachine() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}

// parseKernelFilename extracts version and arch from a kernel filename.
// Python: KernelService.parse_filename()
// Examples:
//
//	vmlinux-6.1.0-x86_64 -> version="6.1.0", arch="x86_64"
//	vmlinux-5.10-arm64 -> version="5.10", arch="arm64"
//	vmlinux -> version="-", arch="-"
func parseKernelFilename(filename string) (version, arch string) {
	name := filename
	arches := []string{"x86_64", "amd64", "arm64", "aarch64"}
	version = "-"
	arch = "-"

	// Step 1: Strip arch suffix from end (Python: for a in arches: if name.endswith(f"-{a}"))
	for _, a := range arches {
		if strings.HasSuffix(name, "-"+a) {
			arch = a
			name = name[:len(name)-len(a)-1]
			break
		}
	}

	// Step 2: Strip version from end using regex (Python: re.search(r"-v?(\d+(?:\.\d+)*)$", name))
	versionRe := regexp.MustCompile(`-v?(\d+(?:\.\d+)*)$`)
	if m := versionRe.FindStringSubmatch(name); len(m) >= 2 {
		versionNum := m[1]
		fullMatch := m[0]
		// Python: version = f"v{version_num}" if full_match.startswith("-v") else version_num
		if strings.HasPrefix(fullMatch, "-v") {
			version = "v" + versionNum
		} else {
			version = versionNum
		}
		// name = name[:match.start()] -- not needed, we only need version and arch
	}

	return
}
