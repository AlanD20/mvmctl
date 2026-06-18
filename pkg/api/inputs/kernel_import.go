package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
)

// KernelImportInput specifies kernel import input.
type KernelImportInput struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Version    *string `json:"version,omitempty"`
	SetDefault bool    `json:"set_default"`
}

// ResolvedKernelImportInput specifies resolved kernel import input.
type ResolvedKernelImportInput struct {
	Name       string
	Path       string
	Version    string
	Arch       string
	SetDefault bool
}

// KernelImportRequest specifies kernel import request.
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
func (r *KernelImportRequest) Resolve(ctx context.Context) (*ResolvedKernelImportInput, error) {
	// Expand and resolve the kernel source path.
	sourcePath, err := system.ExpandAndResolve(r.input.Path)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Failed to resolve kernel path: %v", err),
			err,
			errs.WithClass(errs.ClassValidation),
		)
	}
	parsedVersion, parsedArch := parseKernelFilename(filepath.Base(sourcePath))
	// Resolve arch — arch always matches the host machine, but can be
	// extracted from the filename if present (e.g. "vmlinux-6.1-x86_64").
	var arch string
	if parsedArch != "" && parsedArch != "-" {
		arch = parsedArch
	} else {
		arch = system.RuntimeArch()
	}
	// Resolve version:
	// Use user-specified version if provided; otherwise use parsed value,
	// falling back to "unknown".
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

// ensureValidate validates the resolved import request.
func (r *KernelImportRequest) ensureValidate() error {
	if r.result == nil {
		return errs.New(
			errs.CodeKernelBuildFailed,
			"Failed to resolve necessary dependencies to validate",
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 1. Path exists.
	if _, err := os.Stat(r.result.Path); os.IsNotExist(err) {
		return errs.NotFound(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file not found: %s", r.result.Path))
	}
	// 2. Path is non-empty.
	fi, err := os.Stat(r.result.Path)
	if err == nil && fi.Size() == 0 {
		return errs.New(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file is empty: %s", r.result.Path))
	}
	// 3. Arch is supported —
	if !slices.Contains(infra.FirecrackerSupportedArches, r.result.Arch) {
		return errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Unknown arch: %s. Valid: x86_64, amd64, aarch64, arm64", r.result.Arch),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 4. Name is non-empty —
	if r.result.Name == "" {
		return errs.New(errs.CodeKernelBuildFailed, "Kernel name cannot be empty", errs.WithClass(errs.ClassValidation))
	}
	return nil
}

// parseKernelFilename extracts version and arch from a kernel filename.
// Examples:
//
//	vmlinux-6.1.0-x86_64 -> version="6.1.0", arch="x86_64"
//	vmlinux-5.10-arm64 -> version="5.10", arch="arm64"
//	vmlinux -> version="-", arch="-"
func parseKernelFilename(filename string) (ver, arch string) {
	name := filename
	ver = "-"
	arch = "-"
	// Step 1: Strip arch suffix from the end.
	for _, a := range infra.FirecrackerSupportedArches {
		if strings.HasSuffix(name, "-"+a) {
			arch = a
			name = name[:len(name)-len(a)-1]
			break
		}
	}
	// Step 2: Extract version from filename.
	if v, ok := version.ExtractVersionFromFilename(name); ok {
		ver = v
	}
	return
}
