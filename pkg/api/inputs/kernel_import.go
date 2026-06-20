package inputs

import (
	"fmt"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
	"os"
	"path/filepath"
	"strings"
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

// Validate checks that the kernel import input has required fields.
func (i *KernelImportInput) Validate() error {
	if i.Name == "" {
		return fmt.Errorf("kernel name is required")
	}
	if i.Path == "" {
		return fmt.Errorf("kernel path is required")
	}
	return nil
}

// Resolve resolves and validates import inputs, returning a ResolvedKernelImportInput.
func (i *KernelImportInput) Resolve() (*ResolvedKernelImportInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Expand and resolve the kernel source path.
	sourcePath, err := system.ExpandAndResolve(i.Path)
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
	if i.Version != nil && *i.Version != "" {
		version = *i.Version
	} else if parsedVersion != "" && parsedVersion != "-" {
		version = parsedVersion
	} else {
		version = "unknown"
	}
	result := &ResolvedKernelImportInput{
		Name:       i.Name,
		Path:       sourcePath,
		Version:    version,
		Arch:       arch,
		SetDefault: i.SetDefault,
	}
	// 1. Path exists.
	if _, err := os.Stat(result.Path); os.IsNotExist(err) {
		return nil, errs.NotFound(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file not found: %s", result.Path))
	}
	// 2. Path is non-empty.
	fi, err := os.Stat(result.Path)
	if err == nil && fi.Size() == 0 {
		return nil, errs.New(errs.CodeKernelNotFound, fmt.Sprintf("Kernel file is empty: %s", result.Path))
	}
	// 3. Arch is supported.
	if !firecracker.SupportsArch(result.Arch) {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			fmt.Sprintf("Unknown arch: %s. Valid: x86_64, amd64, aarch64, arm64", result.Arch),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 4. Name is non-empty.
	if result.Name == "" {
		return nil, errs.New(
			errs.CodeKernelBuildFailed,
			"Kernel name cannot be empty",
			errs.WithClass(errs.ClassValidation),
		)
	}
	return result, nil
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
	for _, a := range firecracker.SupportedArches {
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
