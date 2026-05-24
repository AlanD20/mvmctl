package image

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/version"
	"gopkg.in/yaml.v3"
)

// HttpDirVersionResolver resolves available image versions by delegating to
// the shared download.HttpDirVersionResolver and converting results to ImageVersion.
// This is a thin wrapper around the shared infra resolver — matching Python's pattern
// where image/version_resolver.py wraps _http_dir_version_resolver.py.
type HttpDirVersionResolver struct {
	inner *download.HttpDirVersionResolver
}

// NewHttpDirVersionResolver creates a new HttpDirVersionResolver.
func NewHttpDirVersionResolver() *HttpDirVersionResolver {
	return &HttpDirVersionResolver{
		inner: download.NewHttpDirVersionResolver(),
	}
}

// Resolve fetches and parses version listings for all provided image type configs.
// Converts map-based configs to download.ResolverConfig, delegates to the shared
// resolver, then converts version.VersionInfo results to ImageVersion.
func (r *HttpDirVersionResolver) Resolve(
	configs []map[string]any,
	arch string,
	cacheTTLSeconds int,
	ciVersion string,
) map[string][]ImageVersion {
	// Convert map configs to typed ResolverConfig structs
	resolverConfigs := make([]download.ResolverConfig, 0, len(configs))
	for _, cfg := range configs {
		rc, err := configFromMap(cfg)
		if err != nil {
			slog.Warn("Failed to parse config", "error", err)
			continue
		}
		resolverConfigs = append(resolverConfigs, rc)
	}

	// Delegate to the shared infra resolver
	results := r.inner.Resolve(context.Background(), resolverConfigs, arch, ciVersion, cacheTTLSeconds, 0)

	// Convert version.VersionInfo results to ImageVersion with config-aware
	// fields (type_name, codename) — matching Python's inline conversion loop.
	output := make(map[string][]ImageVersion, len(results))
	for typeName, versions := range results {
		imageVersions := make([]ImageVersion, 0, len(versions))
		for _, v := range versions {
			imageVersion := VersionInfoToImageVersion(v, configs, typeName)
			imageVersions = append(imageVersions, imageVersion)
		}
		output[typeName] = imageVersions
	}

	return output
}

// VersionInfoToImageVersion converts a version.VersionInfo to an ImageVersion,
// optionally resolving the config_name (type_name) and codename from the
// original image_types_config. This matches Python's inline conversion in
// HttpDirVersionResolver.resolve().
//
// configs is the original image_types_config list used to find config_name
// and codename. If nil, fields are populated from VersionInfo directly.
func VersionInfoToImageVersion(vi version.VersionInfo, configs []map[string]any, typeName string) ImageVersion {
	// Resolve config_name from the config's "name" field (e.g. "Ubuntu"),
	// matching Python: config = _find_config(image_types_config, type_name);
	// config_name = config.get("name", "") if config else ""
	configName := ""
	config := findConfig(configs, typeName)
	if config != nil {
		if n, ok := config["name"].(string); ok {
			configName = n
		}
	}

	// Resolve codename from codename_mapping, matching Python's reverse lookup:
	// for codename_key, mapped_version in codename_mapping.items():
	//     if mapped_version == v.version:
	//         codename = codename_key
	//         break
	var codename *string
	if config != nil {
		if optsRaw, ok := config["options"].(map[string]any); ok {
			if cmRaw, ok := optsRaw["codename_mapping"].(map[string]any); ok {
				for codenameKey, mappedVersion := range cmRaw {
					if ms, ok := mappedVersion.(string); ok && ms == vi.Version {
						cn := codenameKey
						codename = &cn
						break
					}
				}
			}
		}
	}

	return ImageVersion{
		Version:     vi.Version,
		Codename:    codename,
		DownloadURL: vi.DownloadURL,
		SHA256URL:   vi.SHA256URL, // populated from VersionInfo
		DisplayName: vi.DisplayName,
		Type:        vi.Type,
		Format:      vi.Format,
		TypeName:    configName,
	}
}

// ── Preserved utility methods (used by other parts of the image code) ──
// TODO(verdict#33): move ParseDirectoryListing, DiscoverFileFromListing,
// ResolveVersion, VersionSortKey, SortImageVersions, configFromMap, findConfig,
// getStringMap, getString, getStringSlice to infra/

// ParseDirectoryListing extracts directory names from Apache HTML directory listing.
// Matches Python's HttpDirVersionResolver._parse_directory_listing() exactly.
func ParseDirectoryListing(html string) []string {
	// Use map to deduplicate while preserving insertion order
	seen := make(map[string]bool)
	var result []string
	re := regexp.MustCompile(`href="([^"]+)/"`)
	matches := re.FindAllStringSubmatch(html, -1)
	for _, m := range matches {
		dir := m[1]
		if !seen[dir] {
			seen[dir] = true
			result = append(result, dir)
		}
	}
	return result
}

// DiscoverFileFromListing fetches a directory listing HTML and finds a matching file URL.
// Matches Python's HttpDirVersionResolver._discover_file_from_listing() exactly.
func DiscoverFileFromListing(
	url string,
	pattern string,
	suffix string,
	cacheTTLSeconds int,
) string {
	_ttl := cacheTTLSeconds
	if _ttl < 0 {
		_ttl = 0
	}

	dl := download.New()
	useCache := cacheTTLSeconds >= 0
	html, err := dl.GetRaw(context.Background(), url, 30, nil, useCache, _ttl)
	if err != nil {
		slog.Warn("File discovery directory not available (skipping)", "url", url)
		return ""
	}

	re := regexp.MustCompile(`href="([^"]+)"`)
	allLinks := re.FindAllStringSubmatch(html, -1)

	// Normalise base URL — ensure trailing slash
	base := strings.TrimRight(url, "/") + "/"

	for _, m := range allLinks {
		link := m[1]
		// Skip directories (trailing /) and parent entries
		if strings.HasSuffix(link, "/") || link == "." || link == ".." || link == "../" {
			continue
		}
		if strings.Contains(link, "?") || strings.HasPrefix(link, "http") {
			continue
		}
		if strings.Contains(link, pattern) {
			if suffix == "" || strings.Contains(link, suffix) {
				return base + link
			}
		}
	}
	return ""
}

// ResolveVersion resolves a directory name to a (version, codename) pair.
// Matches Python's HttpDirVersionResolver._resolve_version() exactly.
// Returns version and codename (codename may be empty).
// ok is false when the directory should be skipped.
func ResolveVersion(
	dirName string,
	skipPatterns []string,
	versionPrefix string,
	codenameMapping map[string]string,
) (version string, codename string, ok bool) {
	if dirName == "." || dirName == ".." {
		return "", "", false
	}

	for _, pattern := range skipPatterns {
		if strings.Contains(dirName, pattern) {
			return "", "", false
		}
	}

	if len(codenameMapping) > 0 {
		v, found := codenameMapping[dirName]
		if !found {
			return "", "", false
		}
		return v, dirName, true
	}

	if versionPrefix != "" {
		if !strings.HasPrefix(dirName, versionPrefix) {
			return "", "", false
		}
		return dirName[len(versionPrefix):], "", true
	}

	return dirName, "", true
}

// VersionSortKey returns a sort key for ImageVersion, supporting dotted numeric versions.
// Matches Python's HttpDirVersionResolver._version_sort_key() exactly.
// The returned slice supports comparison: lower sort key = earlier version.
func VersionSortKey(entry ImageVersion) []int {
	parts := strings.Split(entry.Version, ".")
	key := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return []int{0}
		}
		key[i] = n
	}
	return key
}

// SortImageVersions sorts a slice of ImageVersion by version (newest first).
// Matches Python's sort with reverse=True after _version_sort_key.
func SortImageVersions(versions []ImageVersion) {
	sort.Slice(versions, func(i, j int) bool {
		ki := VersionSortKey(versions[i])
		kj := VersionSortKey(versions[j])
		// Compare element by element
		for idx := 0; idx < len(ki) && idx < len(kj); idx++ {
			if ki[idx] != kj[idx] {
				return ki[idx] > kj[idx] // reverse: newest first
			}
		}
		return len(ki) > len(kj) // more specific version wins (e.g. 1.2.3 > 1.2)
	})
}

// configFromMap converts a map[string]any config to a download.ResolverConfig
// using JSON round-tripping (same approach as Python's dict-to-dataclass pattern).
func configFromMap(m map[string]any) (download.ResolverConfig, error) {
	var cfg download.ResolverConfig

	if v, ok := m["type"].(string); ok {
		cfg.Type = v
	}
	if v, ok := m["resolver"].(string); ok {
		cfg.Resolver = v
	}
	if v, ok := m["versions_url"].(string); ok {
		cfg.VersionsURL = v
	}
	if v, ok := m["download_url"].(string); ok {
		cfg.DownloadURL = v
	}
	if v, ok := m["sha256_url"].(string); ok {
		cfg.SHA256URL = v
	}
	if v, ok := m["list_url_template"].(string); ok {
		cfg.ListURLTemplate = v
	}
	if v, ok := m["format"].(string); ok {
		cfg.Format = v
	}
	if v, ok := m["name"].(string); ok {
		cfg.Name = v
	}
	if v, ok := m["version_name_template"].(string); ok {
		cfg.VersionNameTmpl = v
	}
	if v, ok := m["source"].(string); ok {
		cfg.Source = v
	}
	if v, ok := m["version"].(string); ok {
		cfg.Version = v
	}
	if v, ok := m["limit"].(int); ok {
		cfg.Limit = v
	}

	// Build options
	if optsRaw, ok := m["options"].(map[string]any); ok {
		if v, ok := optsRaw["skip_patterns"].([]any); ok {
			cfg.Options.SkipPatterns = make([]string, len(v))
			for i, item := range v {
				cfg.Options.SkipPatterns[i], _ = item.(string)
			}
		}
		if v, ok := optsRaw["version_prefix"].(string); ok {
			cfg.Options.VersionPrefix = v
		}
		if v, ok := optsRaw["codename_mapping"].(map[string]any); ok {
			cfg.Options.CodenameMapping = make(map[string]string, len(v))
			for k, val := range v {
				if s, ok := val.(string); ok {
					cfg.Options.CodenameMapping[k] = s
				}
			}
		}
		if v, ok := optsRaw["arch_mapping"].(map[string]any); ok {
			cfg.Options.ArchMapping = make(map[string]string, len(v))
			for k, val := range v {
				if s, ok := val.(string); ok {
					cfg.Options.ArchMapping[k] = s
				}
			}
		}
		if v, ok := optsRaw["file_discovery"].(map[string]any); ok {
			fd := &download.FileDiscoveryOpt{}
			if enabled, ok := v["enabled"].(bool); ok {
				fd.Enabled = enabled
			}
			if pattern, ok := v["pattern"].(string); ok {
				fd.Pattern = pattern
			}
			if suffix, ok := v["suffix"].(string); ok {
				fd.Suffix = suffix
			}
			if shaSuffix, ok := v["sha256_suffix"].(string); ok {
				fd.SHA256Suffix = shaSuffix
			}
			cfg.Options.FileDiscovery = fd
		}
		if v, ok := optsRaw["file_pattern"].(string); ok {
			cfg.Options.FilePattern = v
		}
		if v, ok := optsRaw["file_suffix"].(string); ok {
			cfg.Options.FileSuffix = v
		}
		if v, ok := optsRaw["version_discoveries"].([]any); ok {
			cfg.Options.VersionDiscoveries = make([]string, len(v))
			for i, item := range v {
				cfg.Options.VersionDiscoveries[i], _ = item.(string)
			}
		}
		if v, ok := optsRaw["s3_version_pattern"].(string); ok {
			cfg.Options.S3VersionPattern = v
		}
		if v, ok := optsRaw["limit"].(int); ok {
			cfg.Options.Limit = v
		}
	}

	return cfg, nil
}

// TypeConfigRaw holds the raw YAML structure from images.yaml.
type TypeConfigRaw struct {
	ImageTypes []map[string]any `yaml:"image_types"`
}

// LoadImageTypesConfig loads the image_types catalog from the given YAML bytes.
func LoadImageTypesConfig(yamlContent []byte) ([]map[string]any, error) {
	var raw TypeConfigRaw
	if err := yaml.Unmarshal(yamlContent, &raw); err != nil {
		return nil, fmt.Errorf("parse images.yaml: %w", err)
	}
	return raw.ImageTypes, nil
}

// LoadImageTypesConfigFromAsset loads the image_types catalog from embedded assets,
// matching Python's AssetManager().get_file("images.yaml") pattern.
func LoadImageTypesConfigFromAsset(assetReader func(name string) ([]byte, error)) ([]map[string]any, error) {
	data, err := assetReader("images.yaml")
	if err != nil {
		return nil, fmt.Errorf("read images.yaml from assets: %w", err)
	}
	return LoadImageTypesConfig(data)
}

// ConstructSpecFromTypeConfig constructs an ImageSpec from a type config dict.
// Returns nil if source URL template resolution fails (missing variables),
// matching Python's render_template KeyError propagation.
func ConstructSpecFromTypeConfig(config map[string]any, versionStr, arch string, ciVersion string) (*ImageSpec, error) {
	typeName, _ := config["type"].(string)
	resolver, _ := config["resolver"].(string)
	configName, _ := config["name"].(string)
	format_, _ := config["format"].(string)
	opts, _ := config["options"].(map[string]any)

	if resolver == "" {
		versionStr = "latest"
	}

	if arch == "" {
		arch = DefaultArch()
	}

	archMapping := getStringMap(opts, "arch_mapping")
	resolvedArch := arch
	if mapped, ok := archMapping[resolvedArch]; ok {
		resolvedArch = mapped
	}

	codenameMapping := getStringMap(opts, "codename_mapping")
	if resolver == "http-dir" {
		if mappedVersion, ok := codenameMapping[versionStr]; ok {
			versionStr = mappedVersion
		}
	}

	versionPrefix, _ := opts["version_prefix"].(string)
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

	downloadURLTmpl, _ := config["download_url"].(string)
	source, err := renderTemplate(downloadURLTmpl, templateVars)
	if err != nil {
		// Python's render_template raises KeyError → propagates up to caller
		return nil, NewImageError(fmt.Sprintf("Failed to render download URL template: %s", err))
	}

	sha256URL := ""
	sha256Config, _ := config["sha256_url"].(string)
	if sha256Config != "" {
		sha256Rendered, err := renderTemplate(sha256Config, templateVars)
		if err == nil {
			sha256URL = sha256Rendered
		}
		// Python catches (ValueError, KeyError) → sha256_url = None
	}

	// Build display name — matching Python EXACTLY:
	//   version_name_template = config.get("version_name_template")
	//   if version_name_template:
	//       try: name = render_template(version_name_template, name_vars)
	//       except (ValueError, KeyError): name = f"{config_name} {version}".strip()
	//   else:
	//       name = f"{config_name} {version}".strip()
	name := strings.TrimSpace(configName + " " + versionStr)
	if nameTmpl, ok := config["version_name_template"].(string); ok && nameTmpl != "" {
		nameVars := map[string]string{
			"version":  versionStr,
			"codename": codename,
			"type":     typeName,
		}
		if resolver == "firecracker-s3" {
			nameVars["ci_version"] = ciVersion
		}
		rendered, err := renderTemplate(nameTmpl, nameVars)
		if err == nil {
			name = rendered
		}
		// Python catches (ValueError, KeyError) → falls back to configName + " " + version
	}

	var sha256Ptr *string
	if sha256URL != "" {
		sha256Ptr = &sha256URL
	}

	return &ImageSpec{
		Type:      typeName,
		Version:   versionStr,
		Name:      name,
		Source:    source,
		Format:    format_,
		Arch:      arch,
		SHA256URL: sha256Ptr,
	}, nil
}

// renderTemplate renders a template by replacing {key} placeholders with values.
// Returns error if any placeholders remain unresolved — matching Python's
// render_template() which raises KeyError for missing template variables.
func renderTemplate(tmpl string, vars map[string]string) (string, error) {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{"+k+"}", v)
	}
	// Check for any remaining {placeholders} — matching Python's str.format()
	// KeyError behavior. If any unreplaced placeholders remain, the render failed.
	if strings.Contains(result, "{") && strings.Contains(result, "}") {
		return "", fmt.Errorf("unresolved template placeholders in: %s", tmpl)
	}
	return result, nil
}

// renderTemplateSafe renders a template by replacing {key} placeholders with values.
// If any placeholders remain unresolved in the result, returns an empty string.
// This is used for optional templates where missing vars are acceptable
// (e.g. sha256_url, version_name_template).
func renderTemplateSafe(tmpl string, vars map[string]string) string {
	result, err := renderTemplate(tmpl, vars)
	if err != nil {
		return ""
	}
	return result
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringSlice(m map[string]any, key string) []string {
	if v, ok := m[key].([]any); ok {
		result := make([]string, len(v))
		for i, item := range v {
			result[i], _ = item.(string)
		}
		return result
	}
	if v, ok := m[key].([]string); ok {
		return v
	}
	return nil
}

// findConfig finds the config dict for a given type name from the
// image_types_config list. Matches Python's _find_config():
// for config in configs:
//     if config.get("type") == type_name:
//         return config
// return None
func findConfig(configs []map[string]any, typeName string) map[string]any {
	for _, config := range configs {
		if t, ok := config["type"].(string); ok && t == typeName {
			return config
		}
	}
	return nil
}

func getStringMap(m map[string]any, key string) map[string]string {
	result := make(map[string]string)
	if v, ok := m[key].(map[string]any); ok {
		for k, val := range v {
			if s, ok := val.(string); ok {
				result[k] = s
			}
		}
	}
	if v, ok := m[key].(map[string]string); ok {
		return v
	}
	return result
}
