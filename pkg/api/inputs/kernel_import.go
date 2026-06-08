package inputs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"

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
	// Expand and resolve path — Python: Path(self._inputs.path).expanduser().resolve()
	sourcePath, err := system.ExpandAndResolve(r.input.Path)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to resolve kernel path: %v", err),
			err,
			errs.WithClass(errs.ClassValidation),
		)
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
		return errs.New(
			errs.CodeKernelBuildFailed,
			"Failed to resolve necessary dependencies to validate",
			errs.WithClass(errs.ClassValidation),
		)
	}

	// 1. Path exists — Python: if not self.result.path.exists()
	if _, err := os.Stat(r.result.Path); os.IsNotExist(err) {
		return errs.NotFound(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file not found: %s", r.result.Path))
	}

	// 2. Path is non-empty — Python: if self.result.path.stat().st_size == 0
	fi, err := os.Stat(r.result.Path)
	if err == nil && fi.Size() == 0 {
		return errs.New(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file is empty: %s", r.result.Path))
	}

	// 3. Arch is supported — Python: if self.result.arch not in FIRECRACKER_SUPPORTED_ARCH
	if !slices.Contains(infra.FirecrackerSupportedArches, r.result.Arch) {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Unknown arch: %s. Valid: x86_64, amd64, aarch64, arm64", r.result.Arch),
			errs.WithClass(errs.ClassValidation),
		)
	}

	// 4. Name is non-empty — Python: if not self.result.name
	if r.result.Name == "" {
		return errs.New(errs.CodeKernelBuildFailed, "Kernel name cannot be empty", errs.WithClass(errs.ClassValidation))
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
func parseKernelFilename(filename string) (ver, arch string) {
	name := filename
	ver = "-"
	arch = "-"

	// Step 1: Strip arch suffix from end (Python: for a in arches: if name.endswith(f"-{a}"))
	for _, a := range infra.FirecrackerSupportedArches {
		if strings.HasSuffix(name, "-"+a) {
			arch = a
			name = name[:len(name)-len(a)-1]
			break
		}
	}

	// Step 2: Strip version from end (Python: re.search(r"-v?(\d+(?:\.\d+)*)$", name))
	if v, ok := version.ExtractVersionFromFilename(name); ok {
		ver = v
	}

	return
}
