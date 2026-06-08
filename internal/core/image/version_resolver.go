package image

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/errs"

	"gopkg.in/yaml.v3"
)

// ResolveVersions fetches and parses version listings for all provided image type configs.
// Delegates to the shared infra resolver, then enriches results with the
// human-readable Name from the image type config.
func ResolveVersions(
	ctx context.Context,
	configs []download.ResolverConfig,
	arch string,
	cacheTTLSeconds int,
	ciVersion string,
) map[string][]model.VersionInfo {
	inner := download.NewHttpDirVersionResolver()
	results := inner.Resolve(ctx, configs, arch, ciVersion, cacheTTLSeconds, 0)

	// Enrich each version with the config-level Name (e.g. "Ubuntu LTS").
	output := make(map[string][]model.VersionInfo, len(results))
	for typeName, versions := range results {
		configName := resolveConfigName(configs, typeName)
		for i := range versions {
			versions[i].Name = configName
		}
		output[typeName] = versions
	}
	return output
}

// resolveConfigName finds the human-readable name for a type from the config list.
func resolveConfigName(configs []download.ResolverConfig, typeName string) string {
	for i := range configs {
		if configs[i].Type == typeName {
			return configs[i].Name
		}
	}
	return ""
}

// TypeConfigRaw holds the raw YAML structure from images.yaml.
type TypeConfigRaw struct {
	ImageTypes []download.ResolverConfig `yaml:"image_types"`
}

// LoadImageTypesConfig loads the image_types catalog from the given YAML bytes.
func LoadImageTypesConfig(yamlContent []byte) ([]download.ResolverConfig, error) {
	var raw TypeConfigRaw
	if err := yaml.Unmarshal(yamlContent, &raw); err != nil {
		return nil, fmt.Errorf("parse images.yaml: %w", err)
	}
	return raw.ImageTypes, nil
}

// ConstructSpecFromTypeConfig constructs an model.ImageSpec from a type config.
// Returns nil if source URL template resolution fails (missing variables),
// matching Python's render_template KeyError propagation.
func ConstructSpecFromTypeConfig(
	config download.ResolverConfig,
	versionStr, arch string,
	ciVersion string,
) (*model.ImageSpec, error) {
	typeName := config.Type
	resolver := config.Resolver
	configName := config.Name
	format_ := config.Format
	opts := config.Options

	if resolver == "" {
		versionStr = "latest"
	}

	archMapping := opts.ArchMapping
	resolvedArch := arch
	if mapped, ok := archMapping[resolvedArch]; ok {
		resolvedArch = mapped
	}

	codenameMapping := opts.CodenameMapping
	if resolver == "http-dir" {
		if mappedVersion, ok := codenameMapping[versionStr]; ok {
			versionStr = mappedVersion
		}
	}

	versionPrefix := opts.VersionPrefix
	templateVersion := versionStr
	if versionPrefix != "" {
		templateVersion = versionPrefix + versionStr
	}

	codename := versionStr
	if resolver == "http-dir" {
		reverseMap := make(map[string]string)
		for k, v := range codenameMapping {
			reverseMap[v] = k
		}
		if c, ok := reverseMap[versionStr]; ok {
			codename = c
		}
	}

	templateVars := map[string]string{
		"version":  templateVersion,
		"codename": codename,
		"arch":     resolvedArch,
	}
	if resolver == "firecracker-s3" {
		templateVars["ci_version"] = ciVersion
	}

	downloadURLTmpl := config.DownloadURL
	source, err := infra.RenderTemplate(downloadURLTmpl, templateVars)
	if err != nil {
		// Python's render_template raises KeyError → propagates up to caller
		return nil, errs.New(errs.CodeImageError, fmt.Sprintf("Failed to render download URL template: %s", err))
	}

	sha256URL := ""
	sha256Config := config.SHA256URL
	if sha256Config != "" {
		sha256Rendered, err := infra.RenderTemplate(sha256Config, templateVars)
		if err == nil {
			sha256URL = sha256Rendered
		}
	}

	name := strings.TrimSpace(configName + " " + versionStr)
	if config.VersionNameTmpl != "" {
		nameVars := map[string]string{
			"version":  versionStr,
			"codename": codename,
			"type":     typeName,
		}
		if resolver == "firecracker-s3" {
			nameVars["ci_version"] = ciVersion
		}
		rendered, err := infra.RenderTemplate(config.VersionNameTmpl, nameVars)
		if err == nil {
			name = rendered
		}
	}

	return &model.ImageSpec{
		Type:      typeName,
		Version:   versionStr,
		Name:      name,
		Source:    source,
		Format:    format_,
		Arch:      arch,
		SHA256URL: sha256URL,
	}, nil
}
