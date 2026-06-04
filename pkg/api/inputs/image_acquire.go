package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/image"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/disk"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/system"
)

// CLI_TO_INTERNAL_DETECTOR maps CLI detector names to internal detector codes.
// Matches Python's CLI_TO_INTERNAL_DETECTOR dict.
var CLI_TO_INTERNAL_DETECTOR = map[string]string{
	"type":       "type_code",
	"label":      "label",
	"size":       "size",
	"filesystem": "filesystem",
}

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
	Version           string   `json:"version,omitempty"`
	NoCache           bool     `json:"no_cache"`
	Partition         int      `json:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty"`
	OutputDir         string   `json:"output_dir,omitempty"`
}

// ImageImportInput is the raw input for importing a local image file.
// Matches Python's ImageImportInput dataclass.
type ImageImportInput struct {
	Name              string   `json:"name"`
	SourcePath        string   `json:"source_path"`
	Force             bool     `json:"force"`
	Format            string   `json:"format,omitempty"`
	SetDefault        bool     `json:"set_default"`
	Partition         int      `json:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty"`
}

// ResolvedImageAcquireInput matches Python's ResolvedImageAcquireInput (frozen dataclass).
type ResolvedImageAcquireInput struct {
	Type              string
	Arch              string
	OutputDir         string
	Name              *string
	SourcePath        *string
	Version           string // resolved version; "" means latest
	NoCache           bool
	Force             bool
	Format            string // resolved format; "" if unknown
	FormatDetected    string // auto-detected format ("" if none, for warning display)
	SetDefault        bool
	Partition         int // 0 = auto-detect
	SkipOptimization  bool
	DisabledDetectors []string
}

// ImageAcquireRequest matches Python's ImageAcquireRequest.
//
// input uses any because it is either ImagePullInput or ImageImportInput —
// Go has no sum types.
type ImageAcquireRequest struct {
	cfg      *config.Service
	input    any // ImagePullInput or ImageImportInput
	result   *ResolvedImageAcquireInput
	resolver *image.Resolver
}

// NewImageAcquireRequest creates a new ImageAcquireRequest.
func NewImageAcquireRequest(inputs any, cfg *config.Service, imageRepo image.Repository) *ImageAcquireRequest {
	return &ImageAcquireRequest{
		cfg:      cfg,
		input:    inputs,
		resolver: image.NewResolver(imageRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// ResolvePull resolves pull inputs.
// Matches Python's ImageAcquireRequest.resolve_pull().
func (r *ImageAcquireRequest) ResolvePull(ctx context.Context) (*ResolvedImageAcquireInput, error) {
	in, ok := r.input.(*ImagePullInput)
	if !ok {
		return nil, &errs.DomainError{
			Code:    errs.CodeImagePullFailed,
			Op:      "image_acquire",
			Message: "Expected ImagePullInput",
			Class:   errs.ClassValidation,
		}
	}

	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()

	// Resolve disabled detectors — Python: self._resolve_disabled_detectors(self._inputs.disabled_detectors)
	disabled, err := r.resolveDisabledDetectors(in.DisabledDetectors)
	if err != nil {
		return nil, err
	}

	outputDir := in.OutputDir
	if outputDir == "" {
		outputDir = infra.GetImagesDir()
	}

	r.result = &ResolvedImageAcquireInput{
		Type:              in.Type,
		Name:              in.Name,
		Force:             in.Force,
		SetDefault:        in.SetDefault,
		Arch:              arch,
		Version:           in.Version,
		NoCache:           in.NoCache,
		Partition:         in.Partition,
		OutputDir:         outputDir,
		SkipOptimization:  in.SkipOptimization,
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
	in, ok := r.input.(*ImageImportInput)
	if !ok {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "Expected ImageImportInput",
			Class:   errs.ClassValidation,
		}
	}

	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()

	// Resolve disabled detectors
	disabled, err := r.resolveDisabledDetectors(in.DisabledDetectors)
	if err != nil {
		return nil, err
	}

	// Resolve format from config, fall back to auto-detection
	format := in.Format
	if format == "" {
		format, _ = r.cfg.GetString(ctx, "defaults.image", "import_format")
	}

	// Auto-detect format from file if format is not known
	detected := disk.DetectImageFormat(in.SourcePath)
	if detected != "" && format == "" {
		format = detected
	}

	sourcePath := in.SourcePath

	r.result = &ResolvedImageAcquireInput{
		Type:              in.Name,
		Name:              &in.Name,
		Arch:              arch,
		SourcePath:        &sourcePath,
		Format:            format,
		OutputDir:         infra.GetImagesDir(),
		DisabledDetectors: disabled,
		Force:             in.Force,
		Partition:         in.Partition,
		SetDefault:        in.SetDefault,
		SkipOptimization:  in.SkipOptimization,
		FormatDetected:    detected,
	}

	if err := r.ensureValidate(); err != nil {
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
	if arch == "" {
		return &errs.DomainError{
			Code:    errs.CodeImageImportFailed,
			Op:      "image_acquire",
			Message: "arch is required",
			Class:   errs.ClassValidation,
		}
	}
	parts := infra.FirecrackerSupportedArches
	archOk := false
	for _, p := range parts {
		if p == arch {
			archOk = true
			break
		}
	}
	if !archOk {
		return &errs.DomainError{
			Code: errs.CodeImageImportFailed,
			Op:   "image_acquire",
			Message: fmt.Sprintf(
				"Unknown arch: %s. Valid: %s",
				arch,
				strings.Join(infra.FirecrackerSupportedArches, ", "),
			),
			Class: errs.ClassValidation,
		}
	}

	if r.result.Partition < 0 {
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
