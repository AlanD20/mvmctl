package inputs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// CLI_TO_INTERNAL_DETECTOR maps CLI detector names to internal detector codes.
var CLI_TO_INTERNAL_DETECTOR = map[string]string{
	"type":       "type_code",
	"label":      "label",
	"size":       "size",
	"filesystem": "filesystem",
}

// ImagePullInput holds options for pulling a remote image.
type ImagePullInput struct {
	Type              string   `json:"type"                         yaml:"type"`
	Name              *string  `json:"name,omitempty"               yaml:"name,omitempty"`
	Force             bool     `json:"force"                        yaml:"force"`
	SetDefault        bool     `json:"default"                      yaml:"default"`
	Version           string   `json:"version,omitempty"            yaml:"version,omitempty"`
	NoCache           bool     `json:"no_cache"                     yaml:"no_cache"`
	Partition         int      `json:"partition,omitempty"          yaml:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"            yaml:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty" yaml:"disabled_detectors,omitempty"`
	OutputDir         string   `json:"output_dir,omitempty"`
}

// Validate checks that the image pull input is valid.
func (i *ImagePullInput) Validate() error {
	if i.Type == "" {
		return fmt.Errorf("image type is required")
	}
	if i.Partition < 0 {
		return fmt.Errorf("partition cannot be negative")
	}
	return nil
}

// Resolve resolves the pull input to a ResolvedImageAcquireInput.
// Sets arch, resolves disabled detectors, applies output directory default,
// then validates the resolved result.
func (i *ImagePullInput) Resolve(
	ctx context.Context,
	cfg *config.Service,
	repo image.Repository,
) (*ResolvedImageAcquireInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()
	// Resolve disabled detectors from input
	disabled, err := resolveDisabledDetectors(i.DisabledDetectors)
	if err != nil {
		return nil, err
	}
	outputDir := i.OutputDir
	if outputDir == "" {
		outputDir = infra.GetImagesDir()
	}
	result := &ResolvedImageAcquireInput{
		Type:              i.Type,
		Name:              i.Name,
		Force:             i.Force,
		SetDefault:        i.SetDefault,
		Arch:              arch,
		Version:           i.Version,
		NoCache:           i.NoCache,
		Partition:         i.Partition,
		OutputDir:         outputDir,
		SkipOptimization:  i.SkipOptimization,
		DisabledDetectors: disabled,
	}
	if result.Arch == "" {
		return nil, errs.New(errs.CodeImageImportFailed, "arch is required", errs.WithClass(errs.ClassValidation))
	}
	if !firecracker.SupportsArch(result.Arch) {
		return nil, errs.New(errs.CodeImageImportFailed,
			fmt.Sprintf("unsupported arch: %s", result.Arch),
			errs.WithClass(errs.ClassValidation),
		)
	}
	return result, nil
}

// ImageImportInput holds options for importing a local image file.
type ImageImportInput struct {
	Name              string   `json:"name"`
	SourcePath        string   `json:"source_path"`
	Force             bool     `json:"force"`
	Format            string   `json:"format,omitempty"`
	SetDefault        bool     `json:"set_default"`
	Version           string   `json:"version,omitempty"`
	Partition         int      `json:"partition,omitempty"`
	SkipOptimization  bool     `json:"skip_optimization"`
	DisabledDetectors []string `json:"disabled_detectors,omitempty"`
}

// Validate checks that the image import input is valid.
func (i *ImageImportInput) Validate() error {
	if i.Name == "" {
		return fmt.Errorf("image name is required")
	}
	if i.SourcePath == "" {
		return fmt.Errorf("source path is required")
	}
	if i.Partition < 0 {
		return fmt.Errorf("partition cannot be negative")
	}
	return nil
}

// Resolve resolves the import input to a ResolvedImageAcquireInput.
// Sets arch, resolves format (config → auto-detect), resolves disabled
// detectors, then validates the resolved result.
func (i *ImageImportInput) Resolve(
	ctx context.Context,
	cfg *config.Service,
	repo image.Repository,
	vmRepo vm.Repository,
) (*ResolvedImageAcquireInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}

	// --- 1. Try VM resolution first — VM name supersedes local path ---
	sourcePath := i.SourcePath
	if vmRepo != nil {
		vmResolver := vm.NewResolver(vmRepo)
		vmItem, vmErr := vmResolver.Resolve(ctx, sourcePath)
		if vmErr == nil && vmItem != nil {
			sourcePath = vmItem.RootfsPath
			i.Format = "raw"           // force raw for VM rootfs
			i.SkipOptimization = true  // skip deblob for VM imports
			slog.Debug("Importing from VM", "vm", vmItem.Name, "rootfs", sourcePath)
		}
	}

	// --- 2. If NOT a VM, validate source file exists ---
	if sourcePath == i.SourcePath { // still original = not resolved as VM
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			return nil, errs.New(errs.CodeImageImportFailed,
				fmt.Sprintf("source file not found: %s", sourcePath))
		}
	}

	// Arch always matches the host machine — not user-configurable
	arch := system.RuntimeArch()
	// Resolve disabled detectors
	disabled, err := resolveDisabledDetectors(i.DisabledDetectors)
	if err != nil {
		return nil, err
	}
	// Resolve format from config, fall back to auto-detection
	format := i.Format
	if format == "" {
		format, _ = cfg.GetString(ctx, "defaults.image", "import_format")
	}
	// Extension-based auto-detect
	if format == "" {
		fname := strings.ToLower(filepath.Base(sourcePath))
		for _, ext := range infra.ImageImportExtensionOrder {
			if strings.HasSuffix(fname, ext) {
				if fmtVal, ok := infra.ImageImportFormatMap[ext]; ok {
					format = fmtVal
					break
				}
			}
		}
	}
	// Magic-byte fallback detection
	detected := disk.DetectImageFormat(sourcePath)
	if detected != "" && format == "" {
		format = detected
	}
	result := &ResolvedImageAcquireInput{
		Type:              i.Name,
		Name:              &i.Name,
		Version:           i.Version,
		Arch:              arch,
		SourcePath:        &sourcePath,
		Format:            format,
		OutputDir:         infra.GetImagesDir(),
		DisabledDetectors: disabled,
		Force:             i.Force,
		Partition:         i.Partition,
		SetDefault:        i.SetDefault,
		SkipOptimization:  i.SkipOptimization,
		FormatDetected:    detected,
		IsImported:        true,
	}
	if result.Arch == "" {
		return nil, errs.New(errs.CodeImageImportFailed, "arch is required", errs.WithClass(errs.ClassValidation))
	}
	if !firecracker.SupportsArch(result.Arch) {
		return nil, errs.New(errs.CodeImageImportFailed,
			fmt.Sprintf("unsupported arch: %s", result.Arch),
			errs.WithClass(errs.ClassValidation),
		)
	}
	return result, nil
}

// ResolvedImageAcquireInput specifies resolved image acquire input.
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
	IsImported        bool
	DisabledDetectors []string
}

// resolveDisabledDetectors resolves disabled detector names to internal codes.
func resolveDisabledDetectors(detectors []string) ([]string, error) {
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
			return nil, errs.New(
				errs.CodeImageImportFailed,
				"Unknown detector: "+name+". Valid: type,label,size,filesystem,all",
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	return disabled, nil
}
