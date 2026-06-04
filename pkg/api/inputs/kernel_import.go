package inputs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/system"

	"github.com/jmoiron/sqlx"
)

// KernelImportInput matches Python's KernelImportInput dataclass.
//
//	@dataclass
//	class KernelImportInput:
//	    name: str
//	    path: Path
//	    version: str | None = None
//	    set_default: bool = False
type KernelImportInput struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Version    *string `json:"version,omitempty"`
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
	db     *sqlx.DB
	input  KernelImportInput
	result *ResolvedKernelImportInput
}

// NewKernelImportRequest creates a new KernelImportRequest.
func NewKernelImportRequest(inputs KernelImportInput, db *sqlx.DB) *KernelImportRequest {
	return &KernelImportRequest{
		db:    db,
		input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves all input fields to concrete values and validates.
// Matches Python's KernelImportRequest.resolve().
func (r *KernelImportRequest) Resolve(ctx context.Context) (*ResolvedKernelImportInput, error) {
	// Expand and resolve path — Python: source_path = self._inputs.path.expanduser().resolve()
	// Python's Path.resolve() follows symlinks and resolves to an absolute path.
	sourcePath := r.input.Path
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

	// Resolve arch — arch always matches the host machine, but can be
	// extracted from the filename if present (e.g. "vmlinux-6.1-x86_64").
	var arch string
	if parsedArch != "" && parsedArch != "-" {
		arch = parsedArch
	} else {
		arch = system.RuntimeArch()
	}

	// Resolve version — Python logic:
	//   if self._inputs.version is not None → version = self._inputs.version
	//   else → version = parsed.version if parsed.version != "-" else "unknown"
	var version string
	if r.input.Version != nil && *r.input.Version != "" {
		version = *r.input.Version
	} else if parsedVersion != "" && parsedVersion != "-" {
		version = parsedVersion
	} else {
		version = "unknown"
	}

	r.result = &ResolvedKernelImportInput{
		Name:       r.input.Name,
		Path:       sourcePath,
		Version:    version,
		Arch:       arch,
		SetDefault: r.input.SetDefault,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

// ensureValidate matches Python's KernelImportRequest.ensure_validate() exactly.
func (r *KernelImportRequest) ensureValidate() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	// 1. Path exists — Python: if not self.result.path.exists()
	if _, err := os.Stat(r.result.Path); os.IsNotExist(err) {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Kernel file not found: %s", r.result.Path),
			Class:   errs.ClassValidation,
		}
	}

	// 2. Path is non-empty — Python: if self.result.path.stat().st_size == 0
	fi, err := os.Stat(r.result.Path)
	if err == nil && fi.Size() == 0 {
		return &errs.DomainError{
			Code:    errs.CodeKernelNotFound,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Kernel file is empty: %s", r.result.Path),
			Class:   errs.ClassValidation,
		}
	}

	// 3. Arch is supported — Python: if self.result.arch not in FIRECRACKER_SUPPORTED_ARCH
	// Python: FIRECRACKER_SUPPORTED_ARCH = ["x86_64", "amd64", "aarch64", "arm64"]
	validArchs := map[string]bool{"x86_64": true, "amd64": true, "aarch64": true, "arm64": true}
	if !validArchs[r.result.Arch] {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: fmt.Sprintf("Unknown arch: %s. Valid: x86_64, amd64, aarch64, arm64", r.result.Arch),
			Class:   errs.ClassValidation,
		}
	}

	// 4. Name is non-empty — Python: if not self.result.name
	if r.result.Name == "" {
		return &errs.DomainError{
			Code:    errs.CodeKernelBuildFailed,
			Op:      "kernel_import",
			Message: "Kernel name cannot be empty",
			Class:   errs.ClassValidation,
		}
	}

	return nil
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
