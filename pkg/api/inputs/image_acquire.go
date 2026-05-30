package inputs

import (
	"context"
	"database/sql"
	"fmt"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/image"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
)

// CLI_TO_INTERNAL_DETECTOR maps CLI detector names to internal detector codes.
// Matches Python's CLI_TO_INTERNAL_DETECTOR dict.
var CLI_TO_INTERNAL_DETECTOR = map[string]string{
	"type":       "type_code",
	"label":      "label",
	"size":       "size",
	"filesystem": "filesystem",
}

// FIRECRACKER_SUPPORTED_ARCH lists architectures supported by Firecracker.
const FIRECRACKER_SUPPORTED_ARCH_STR = "x86_64,amd64,aarch64,arm64"

// ImagePullInput is the raw input for pulling a remote image.
// Matches Python's ImagePullInput dataclass:
//
//	@dataclass
//	class ImagePullInput:
//	    type: str
//	    name: str | None = None
//	    force: bool = False
//	    set_default: bool = False
//	    arch: str | None = None
//	    version: str | None = None
//	    no_cache: bool = False
//	    partition: int | None = None
//	    skip_optimization: bool = False
//	    disabled_detectors: list[str] = field(default_factory=list)
type ImagePullInput struct {
	Type              string   `json:"type"`
	Name              *string  `json:"name,omitempty"`
	Force             bool     `json:"force"`
	SetDefault        bool     `json:"set_default"`
	Arch              *string  `json:"arch,omitempty"`
	Version           *string  `json:"version,omitempty"`
	NoCache           bool     `json:"no_cache"`
	Partition         *int     `json:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty"`
	OutputDir         string   `json:"output_dir,omitempty"` // custom output directory; empty means use default images dir
}

// ImageImportInput is the raw input for importing a local image file.
// Matches Python's ImageImportInput dataclass.
type ImageImportInput struct {
	Name              string   `json:"name"`
	SourcePath        string   `json:"source_path"`
	Force             bool     `json:"force"`
	Format            *string  `json:"format,omitempty"`
	Arch              *string  `json:"arch,omitempty"`
	SetDefault        bool     `json:"set_default"`
	Partition         *int     `json:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty"`
}

// ResolvedImageAcquireInput matches Python's ResolvedImageAcquireInput (frozen dataclass).
//
//	@dataclass
//	class ResolvedImageAcquireInput:
//	    type: str
//	    arch: str
//	    output_dir: Path
//	    name: str | None = None
//	    source_path: Path | None = None
//	    version: str | None = None
//	    no_cache: bool = False
//	    force: bool = False
//	    format: str | None = None
//	    set_default: bool = False
//	    partition: int | None = None
//	    skip_optimization: bool = False
//	    disabled_detectors: list[str] = field(default_factory=list)
type ResolvedImageAcquireInput struct {
	Type              string
	Arch              string
	OutputDir         string
	Name              *string
	SourcePath        *string
	Version           *string
	NoCache           bool
	Force             bool
	Format            *string
	SetDefault        bool
	Partition         *int
	SkipOptimization  bool
	DisabledDetectors []string
}

// ImageAcquireRequest matches Python's ImageAcquireRequest.
//
// input uses any because it is either ImagePullInput or ImageImportInput —
// Go has no sum types.
type ImageAcquireRequest struct {
	db       *sql.DB
	input    any // ImagePullInput or ImageImportInput
	result   *ResolvedImageAcquireInput
	resolver *image.Resolver
}

// NewImageAcquireRequest creates a new ImageAcquireRequest.
func NewImageAcquireRequest(inputs any, db *sql.DB, imageRepo image.Repository) *ImageAcquireRequest {
	return &ImageAcquireRequest{
		db:       db,
		input:    inputs,
		resolver: image.NewResolver(imageRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// ResolvePull resolves pull inputs.
// Matches Python's ImageAcquireRequest.resolve_pull().
func (r *ImageAcquireRequest) ResolvePull(ctx context.Context) (*ResolvedImageAcquireInput, error) {
	pullInput, ok := r.input.(ImagePullInput)
	if !ok {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Op:      "image_acquire",
			Message: "Expected ImagePullInput",
			Class:   errs.ClassValidation,
		}
	}

	// Default arch — Python: SettingsService.resolve(..., "arch"), no fallback
	var arch string
	if pullInput.Arch != nil {
		arch = *pullInput.Arch
	} else {
		archVal, err := config.Resolve(ctx, r.db, "defaults.image", "arch")
		if err == nil && archVal != nil {
			arch = toString(archVal)
		}
	}

	// Resolve disabled detectors — Python: self._resolve_disabled_detectors(self._inputs.disabled_detectors)
	disabled, err := r.resolveDisabledDetectors(pullInput.DisabledDetectors)
	if err != nil {
		return nil, err
	}

	r.result = &ResolvedImageAcquireInput{
		Type:              pullInput.Type,
		Name:              pullInput.Name,
		Force:             pullInput.Force,
		SetDefault:        pullInput.SetDefault,
		Arch:              arch,
		Version:           pullInput.Version,
		NoCache:           pullInput.NoCache,
		Partition:         pullInput.Partition,
		OutputDir:         infra.GetImagesDir(),
		SkipOptimization:  pullInput.SkipOptimization,
		DisabledDetectors: disabled,
	}

	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

// ResolveImport resolves import inputs.
// Matches Python's ImageAcquireRequest.resolve_import().
func (r *ImageAcquireRequest) ResolveImport(ctx context.Context) (*ResolvedImageAcquireInput, error) {
	importInput, ok := r.input.(ImageImportInput)
	if !ok {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Expected ImageImportInput",
			Class:   errs.ClassValidation,
		}
	}

	// Default arch — Python: SettingsService.resolve(..., "arch"), no fallback
	var arch string
	if importInput.Arch != nil {
		arch = *importInput.Arch
	} else {
		archVal, err := config.Resolve(ctx, r.db, "defaults.image", "arch")
		if err == nil && archVal != nil {
			arch = toString(archVal)
		}
	}

	// Resolve disabled detectors
	disabled, err := r.resolveDisabledDetectors(importInput.DisabledDetectors)
	if err != nil {
		return nil, err
	}

	// Default format — Python: str(SettingsService.resolve(...))
	// Python's str(None) returns "None", so we match that behavior exactly
	var format string
	if importInput.Format != nil {
		format = *importInput.Format
	} else {
		formatVal, err := config.Resolve(ctx, r.db, "defaults.image", "import_format")
		if err == nil {
			format = stringify(formatVal) // str(None) → "None" in Python
		} else {
			format = "None"
		}
	}

	sourcePath := importInput.SourcePath

	r.result = &ResolvedImageAcquireInput{
		Type:              importInput.Name,
		Name:              &importInput.Name,
		Arch:              arch,
		SourcePath:        &sourcePath,
		Format:            &format,
		OutputDir:         infra.GetImagesDir(),
		DisabledDetectors: disabled,
		Force:             importInput.Force,
		Partition:         importInput.Partition,
		SetDefault:        importInput.SetDefault,
		SkipOptimization:  importInput.SkipOptimization,
	}

	if err := r.ensureValidate(); err != nil {
		return nil, err
	}
	if err := r.ensureValidateImport(); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *ImageAcquireRequest) ensureValidate() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	arch := r.result.Arch
	validArchs := []string{"x86_64", "amd64", "aarch64", "arm64"}
	archValid := false
	for _, va := range validArchs {
		if arch == va {
			archValid = true
			break
		}
	}
	if !archValid {
		return &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Unknown arch: " + arch + ". Valid: x86_64, amd64, aarch64, arm64",
			Class:   errs.ClassValidation,
		}
	}

	if r.result.Partition != nil && *r.result.Partition < 1 {
		return &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Partition cannot be less than 1",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}

func (r *ImageAcquireRequest) ensureValidateImport() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}
	return nil
}

func (r *ImageAcquireRequest) resolveDisabledDetectors(detectors []string) ([]string, error) {
	var disabled []string
	for _, name := range detectors {
		if name == "all" {
			var all []string
			for _, v := range CLI_TO_INTERNAL_DETECTOR {
				all = append(all, v)
			}
			return all, nil
		}
		if internalName, ok := CLI_TO_INTERNAL_DETECTOR[name]; ok {
			disabled = append(disabled, internalName)
		} else {
			return nil, &errs.DomainError{
				Code:    errs.CodeImageImportFailed,
				Op:      "image_acquire",
				Message: "Unknown detector: " + name + ". Valid: type,label,size,filesystem,all",
				Class:   errs.ClassValidation,
			}
		}
	}
	return disabled, nil
}

// toString converts an any to string.
func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// stringify matches Python's str() builtin: str(None) → "None", str(5) → "5", etc.
func stringify(v any) string {
	if v == nil {
		return "None"
	}
	return fmt.Sprintf("%v", v)
}
