package download

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

// dirHrefRegex matches href="<dir>/" in Apache HTML directory listings.
var dirHrefRegex = regexp.MustCompile(`href="([^"]+)/"`)

// allHrefRegex matches any href="..." attribute in HTML.
var allHrefRegex = regexp.MustCompile(`href="([^"]+)"`)

// --- HttpDirVersionResolver ---

// HttpDirVersionResolver resolves available versions from Apache HTML directory
// listings or S3 bucket XML listings.
//
// Three resolver strategies:
// - "http-dir" — Apache HTML directory listings
// - "firecracker-s3" — S3 bucket XML listings
// - "" or nil (single-source) — single "latest" version from URL templates
type HttpDirVersionResolver struct {
	client *http.Client
	cache  *HttpDiskCache
}

// NewHttpDirVersionResolver creates a new HttpDirVersionResolver with default
// HTTP client and cache.
func NewHttpDirVersionResolver() *HttpDirVersionResolver {
	return &HttpDirVersionResolver{
		client: &http.Client{
			Timeout: infra.HTTPTimeout,
		},
		cache: NewHttpDiskCache(),
	}
}

// resolveVersion resolves a directory name to a (version, codename) pair.
func resolveVersion(
	dirName string,
	skipPatterns []string,
	versionPrefix string,
	codenameMapping map[string]string,
) (string, string, bool) {
	if dirName == "." || dirName == ".." {
		return "", "", false
	}

	for _, pattern := range skipPatterns {
		if strings.Contains(dirName, pattern) {
			return "", "", false
		}
	}

	if len(codenameMapping) > 0 {
		versionStr, ok := codenameMapping[dirName]
		if !ok {
			return "", "", false
		}
		return versionStr, dirName, true
	}

	if versionPrefix != "" {
		if !strings.HasPrefix(dirName, versionPrefix) {
			return "", "", false
		}
		return strings.TrimPrefix(dirName, versionPrefix), "", true
	}

	return dirName, "", true
}

// parseDirectoryListing extracts directory names from Apache HTML directory listing.
func parseDirectoryListing(html string) []string {
	matches := dirHrefRegex.FindAllStringSubmatch(html, -1)
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		dirs = append(dirs, m[1])
	}
	return infra.Dedup(dirs)
}

// versionSortKey returns a sort key for a dotted numeric version string.
func versionSortKey(ver string) []int {
	parts := strings.Split(ver, ".")
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return []int{0}
		}
		nums = append(nums, n)
	}
	return nums
}

// discoverFileFromListing fetches a directory listing HTML and finds a matching file.
// Returns "" when no match is found or fetch fails.
func (r *HttpDirVersionResolver) discoverFileFromListing(
	ctx context.Context,
	url, pattern, suffix string,
	useCache bool,
	ttl int,
) string {
	html, err := r.fetchRawContent(ctx, url, useCache, ttl)
	if err != nil {
		return ""
	}

	allLinks := extractAllHrefs(html)
	base := strings.TrimRight(url, "/") + "/"

	for _, link := range allLinks {
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

// extractAllHrefs extracts all href attribute values from HTML.
func extractAllHrefs(html string) []string {
	matches := allHrefRegex.FindAllStringSubmatch(html, -1)
	var links []string
	for _, m := range matches {
		links = append(links, m[1])
	}
	return links
}

// fetchRawContent fetches a URL's content with optional caching.
func (r *HttpDirVersionResolver) fetchRawContent(
	ctx context.Context,
	url string,
	useCache bool,
	ttl int,
) (string, error) {
	if useCache && r.cache != nil {
		cacheFile := r.cache.Path(url)
		if r.cache.IsValid(cacheFile, ttl) {
			data, err := r.cache.Read(cacheFile)
			if err == nil {
				return string(data), nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if useCache && r.cache != nil {
		cacheFile := r.cache.Path(url)
		if writeErr := r.cache.Write(body, cacheFile); writeErr != nil {
			slog.Debug("Failed to cache content", "error", writeErr)
		}
	}

	return string(body), nil
}

// --- Resolver configuration ---

// ResolverConfig represents a single version source configuration.
type ResolverConfig struct {
	Type            string          `json:"type"                            yaml:"type"`
	Resolver        string          `json:"resolver,omitempty"              yaml:"resolver,omitempty"`
	VersionsURL     string          `json:"versions_url,omitempty"          yaml:"versions_url,omitempty"`
	DownloadURL     string          `json:"download_url,omitempty"          yaml:"download_url,omitempty"`
	SHA256URL       string          `json:"sha256_url,omitempty"            yaml:"sha256_url,omitempty"`
	ListURLTemplate string          `json:"list_url_template,omitempty"     yaml:"list_url_template,omitempty"`
	Format          string          `json:"format,omitempty"                yaml:"format,omitempty"`
	Name            string          `json:"name,omitempty"                  yaml:"name,omitempty"`
	VersionNameTmpl string          `json:"version_name_template,omitempty" yaml:"version_name_template,omitempty"`
	Source          string          `json:"source,omitempty"                yaml:"source,omitempty"`
	Version         string          `json:"version,omitempty"               yaml:"version,omitempty"`
	Limit           int             `json:"limit,omitempty"                 yaml:"limit,omitempty"`
	Options         ResolverOptions `json:"options"                         yaml:"options"`
}

// ResolverOptions contains resolver-specific options for version resolution.
type ResolverOptions struct {
	SkipPatterns       []string          `json:"skip_patterns,omitempty"       yaml:"skip_patterns,omitempty"`
	VersionPrefix      string            `json:"version_prefix,omitempty"      yaml:"version_prefix,omitempty"`
	CodenameMapping    map[string]string `json:"codename_mapping,omitempty"    yaml:"codename_mapping,omitempty"`
	ArchMapping        map[string]string `json:"arch_mapping,omitempty"        yaml:"arch_mapping,omitempty"`
	FileDiscovery      *FileDiscoveryOpt `json:"file_discovery,omitempty"      yaml:"file_discovery,omitempty"`
	FilePattern        string            `json:"file_pattern,omitempty"        yaml:"file_pattern,omitempty"`
	FileSuffix         string            `json:"file_suffix,omitempty"         yaml:"file_suffix,omitempty"`
	VersionDiscoveries []string          `json:"version_discoveries,omitempty" yaml:"version_discoveries,omitempty"`
	S3VersionPattern   string            `json:"s3_version_pattern,omitempty"  yaml:"s3_version_pattern,omitempty"`
	Limit              int               `json:"limit,omitempty"               yaml:"limit,omitempty"`
}

// FileDiscoveryOpt configures file discovery from directory listings.
type FileDiscoveryOpt struct {
	Enabled      bool   `json:"enabled"                 yaml:"enabled"`
	Pattern      string `json:"pattern"                 yaml:"pattern"`
	Suffix       string `json:"suffix,omitempty"        yaml:"suffix,omitempty"`
	SHA256Suffix string `json:"sha256_suffix,omitempty" yaml:"sha256_suffix,omitempty"`
}

// --- Resolve method ---

// Resolve fetches and parses version listings for all provided configs.
// Returns a map of type name → sorted list of VersionInfo (newest first).
// On fetch failure for a given type, returns an empty list for that type.
func (r *HttpDirVersionResolver) Resolve(
	ctx context.Context,
	configs []ResolverConfig,
	arch string,
	ciVersion string,
	cacheTTLSeconds int,
	limit int,
) map[string][]model.VersionInfo {
	result := make(map[string][]model.VersionInfo)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Phase 1: http-dir resolver types — parallel per config
	for _, config := range configs {
		if config.Resolver != "http-dir" || config.VersionsURL == "" {
			continue
		}

		wg.Add(1)
		cfg := config
		go func() {
			defer wg.Done()
			opts := cfg.Options
			local := make(map[string][]model.VersionInfo)
			if len(opts.VersionDiscoveries) > 0 {
				r.resolveViaVersionDiscoveries(ctx, cfg, arch, cacheTTLSeconds, local)
			} else {
				r.resolveViaDirectoryListing(ctx, cfg, arch, cacheTTLSeconds, local)
			}
			mu.Lock()
			for k, v := range local {
				result[k] = v
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Phase 2: single-source types (no resolver or empty resolver) — parallel
	for _, config := range configs {
		if config.Resolver != "" && config.Resolver != "http-dir" {
			if config.Resolver == "firecracker-s3" {
				continue
			}
		}
		if config.Resolver == "http-dir" {
			continue
		}

		wg.Add(1)
		cfg := config
		go func() {
			defer wg.Done()

			typeName := cfg.Type
			mu.Lock()
			_, exists := result[typeName]
			mu.Unlock()
			if exists {
				return
			}

			if cfg.DownloadURL == "" {
				return
			}

			resolvedArch := arch
			if mapped, ok := cfg.Options.ArchMapping[resolvedArch]; ok {
				resolvedArch = mapped
			}

			tmplVars := map[string]string{
				"version":  "latest",
				"codename": "",
				"arch":     resolvedArch,
			}

			downloadURL, err := infra.RenderTemplate(cfg.DownloadURL, tmplVars)
			if err != nil {
				slog.Debug("Failed to render download URL", "type", typeName, "error", err)
				return
			}

			var sha256URL string
			if cfg.SHA256URL != "" {
				var rendered string
				rendered, err = infra.RenderTemplate(cfg.SHA256URL, tmplVars)
				if err != nil {
					slog.Debug("Failed to render sha256 URL", "type", typeName, "error", err)
				} else {
					sha256URL = rendered
				}
			}

			displayName := cfg.Name
			if cfg.VersionNameTmpl != "" {
				if dn, err := infra.RenderTemplate(cfg.VersionNameTmpl, map[string]string{
					"version":  "latest",
					"codename": "",
					"type":     typeName,
				}); err == nil {
					displayName = dn
				}
			}

			mu.Lock()
			result[typeName] = []model.VersionInfo{
				{
					Version:     "latest",
					DownloadURL: downloadURL,
					SHA256URL:   sha256URL,
					DisplayName: displayName,
					Type:        typeName,
					Format:      cfg.Format,
				},
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Phase 3: firecracker-s3 resolver types — parallel
	for _, config := range configs {
		if config.Resolver != "firecracker-s3" {
			continue
		}

		wg.Add(1)
		cfg := config
		go func() {
			defer wg.Done()

			typeName := cfg.Type
			mu.Lock()
			_, exists := result[typeName]
			mu.Unlock()
			if exists {
				return
			}

			local := make(map[string][]model.VersionInfo)
			r.resolveViaFirecrackerS3(ctx, cfg, arch, ciVersion, cacheTTLSeconds, local)
			mu.Lock()
			for k, v := range local {
				result[k] = v
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Apply global limit across all type groups
	if limit > 0 {
		for key := range result {
			if len(result[key]) > limit {
				result[key] = result[key][:limit]
			}
		}
	}

	return result
}

// --- Phase 1 helpers: http-dir ---

func (r *HttpDirVersionResolver) resolveViaDirectoryListing(
	ctx context.Context, config ResolverConfig, arch string, cacheTTLSeconds int,
	result map[string][]model.VersionInfo,
) {
	typeName := config.Type
	versionsURL := config.VersionsURL
	useCache := cacheTTLSeconds != 0
	ttl := max(cacheTTLSeconds, 0)

	html, err := r.fetchRawContent(ctx, versionsURL, useCache, ttl)
	if err != nil {
		slog.Debug("Failed to fetch version listing", "type", typeName, "url", versionsURL, "error", err)
		result[typeName] = []model.VersionInfo{}
		return
	}

	opts := config.Options
	skipPatterns := opts.SkipPatterns
	versionPrefix := opts.VersionPrefix
	codenameMapping := opts.CodenameMapping
	archMapping := opts.ArchMapping

	dirs := parseDirectoryListing(html)
	configName := config.Name

	var versions []model.VersionInfo
	for _, dirName := range dirs {
		versionStr, codename, ok := resolveVersion(dirName, skipPatterns, versionPrefix, codenameMapping)
		if !ok {
			continue
		}

		resolvedArch := arch
		if mapped, ok := archMapping[resolvedArch]; ok {
			resolvedArch = mapped
		}

		tmplVars := map[string]string{
			"version":  versionStr,
			"codename": codename,
			"arch":     resolvedArch,
		}

		downloadURL, err := infra.RenderTemplate(config.DownloadURL, tmplVars)
		if err != nil {
			slog.Debug(
				"Failed to render download URL for version",
				"type",
				typeName,
				"version",
				versionStr,
				"error",
				err,
			)
			continue
		}

		var sha256URL string
		if config.SHA256URL != "" {
			var rendered string
			rendered, err = infra.RenderTemplate(config.SHA256URL, tmplVars)
			if err != nil {
				slog.Debug(
					"Failed to render sha256 URL for version",
					"type",
					typeName,
					"version",
					versionStr,
					"error",
					err,
				)
			} else {
				sha256URL = rendered
			}
		}

		// File discovery for directory-style download URLs
		if opts.FileDiscovery != nil && opts.FileDiscovery.Enabled && downloadURL != "" {
			discoveredURL := r.discoverFileFromListing(ctx, downloadURL,
				opts.FileDiscovery.Pattern, opts.FileDiscovery.Suffix,
				useCache, ttl)
			if discoveredURL != "" {
				downloadURL = discoveredURL
				if opts.FileDiscovery.SHA256Suffix != "" {
					url := downloadURL + opts.FileDiscovery.SHA256Suffix
					sha256URL = url
				}
			} else {
				slog.Debug("No matching cloud image", "type", typeName, "version", versionStr)
				continue
			}
		}

		// Build display name
		displayName := strings.TrimSpace(fmt.Sprintf("%s %s", configName, versionStr))
		if config.VersionNameTmpl != "" {
			if dn, err := infra.RenderTemplate(config.VersionNameTmpl, map[string]string{
				"version":  versionStr,
				"codename": codename,
				"type":     typeName,
			}); err == nil {
				displayName = dn
			}
		}
		displayName = infra.ToTitle(displayName)

		versions = append(versions, model.VersionInfo{
			Version:     versionStr,
			DownloadURL: downloadURL,
			SHA256URL:   sha256URL,
			DisplayName: displayName,
			Type:        typeName,
			Format:      config.Format,
		})
	}

	// Sort newest first
	sort.Slice(versions, func(i, j int) bool {
		ki := versionSortKey(versions[i].Version)
		kj := versionSortKey(versions[j].Version)
		for idx := 0; idx < len(ki) && idx < len(kj); idx++ {
			if ki[idx] != kj[idx] {
				return ki[idx] > kj[idx]
			}
		}
		return len(ki) > len(kj) // more specific version wins (e.g. 1.2.3 > 1.2)
	})

	cfgLimit := config.Limit
	if cfgLimit <= 0 {
		cfgLimit = 5
	}
	if cfgLimit > 0 && len(versions) > cfgLimit {
		versions = versions[:cfgLimit]
	}
	result[typeName] = versions
}

func (r *HttpDirVersionResolver) resolveViaVersionDiscoveries(
	ctx context.Context, config ResolverConfig, arch string, cacheTTLSeconds int,
	result map[string][]model.VersionInfo,
) {
	typeName := config.Type
	versionsURL := config.VersionsURL
	useCache := cacheTTLSeconds != 0
	ttl := max(cacheTTLSeconds, 0)

	opts := config.Options
	discoveries := opts.VersionDiscoveries
	filePattern := opts.FilePattern
	fileSuffix := opts.FileSuffix
	configName := config.Name

	for _, discovery := range discoveries {
		discoveryKey := fmt.Sprintf("%s-%s", typeName, strings.TrimRight(discovery, "/"))

		discoveryPath := strings.TrimRight(discovery, "/") + "/"
		discoveryURL := strings.TrimRight(versionsURL, "/") + "/" + discoveryPath

		html, err := r.fetchRawContent(ctx, discoveryURL, useCache, ttl)
		if err != nil {
			slog.Debug("Failed to fetch version listing", "type", typeName, "url", discoveryURL, "error", err)
			continue
		}

		allLinks := extractAllHrefs(html)
		var discVersions []model.VersionInfo

		for _, link := range allLinks {
			if strings.HasSuffix(link, "/") || link == "." || link == ".." || link == "../" {
				continue
			}
			if strings.Contains(link, "?") || strings.HasPrefix(link, "http") {
				continue
			}
			if strings.HasPrefix(link, "/") {
				continue
			}
			if filePattern != "" && !strings.Contains(link, filePattern) {
				continue
			}
			if fileSuffix != "" && !strings.HasSuffix(link, fileSuffix) {
				continue
			}

			versionStr := link
			if filePattern != "" && strings.HasPrefix(versionStr, filePattern) {
				versionStr = strings.TrimPrefix(versionStr, filePattern)
			}
			if fileSuffix != "" && strings.HasSuffix(versionStr, fileSuffix) {
				versionStr = strings.TrimSuffix(versionStr, fileSuffix)
			}
			if versionStr == "" {
				continue
			}

			downloadURL := strings.TrimRight(discoveryURL, "/") + "/" + link

			var sha256URL string
			if config.SHA256URL != "" {
				series := versionStr
				if s, _, found := strings.Cut(versionStr, "."); found {
					series = s
				}
				if su, err := infra.RenderTemplate(config.SHA256URL, map[string]string{
					"version": versionStr,
					"series":  series,
					"arch":    arch,
				}); err == nil {
					sha256URL = su
				}
			}

			displayName := strings.TrimSpace(fmt.Sprintf("%s %s", configName, versionStr))
			if config.VersionNameTmpl != "" {
				series := versionStr
				if s, _, found := strings.Cut(versionStr, "."); found {
					series = s
				}
				if dn, err := infra.RenderTemplate(config.VersionNameTmpl, map[string]string{
					"version": versionStr,
					"series":  series,
					"type":    typeName,
				}); err == nil {
					displayName = dn
				}
			}

			discVersions = append(discVersions, model.VersionInfo{
				Version:     versionStr,
				DownloadURL: downloadURL,
				SHA256URL:   sha256URL,
				DisplayName: displayName,
				Type:        discoveryKey,
				Format:      config.Format,
			})
		}

		sort.Slice(discVersions, func(i, j int) bool {
			ki := versionSortKey(discVersions[i].Version)
			kj := versionSortKey(discVersions[j].Version)
			for idx := 0; idx < len(ki) && idx < len(kj); idx++ {
				if ki[idx] != kj[idx] {
					return ki[idx] > kj[idx]
				}
			}
			return len(ki) > len(kj)
		})

		cfgLimit := opts.Limit
		if cfgLimit <= 0 {
			cfgLimit = 5
		}
		if cfgLimit > 0 && len(discVersions) > cfgLimit {
			discVersions = discVersions[:cfgLimit]
		}
		result[discoveryKey] = discVersions
	}
}

// --- Phase 3 helper: firecracker-s3 ---

// S3ListBucketResult represents an S3 ListBucketResult XML document.
// Uses namespace-aware XML tags matching the default namespace
// http://s3.amazonaws.com/doc/2006-03-01/ used by AWS S3.
type S3ListBucketResult struct {
	XMLName  xml.Name     `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Contents []S3Contents `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Contents"`
}

// S3Contents represents a single <Contents> entry in S3 listing.
type S3Contents struct {
	Key          string `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Key"`
	LastModified string `xml:"http://s3.amazonaws.com/doc/2006-03-01/ LastModified"`
	Size         int64  `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Size"`
}

func (r *HttpDirVersionResolver) resolveViaFirecrackerS3(
	ctx context.Context, config ResolverConfig, arch string, ciVersion string, cacheTTLSeconds int,
	result map[string][]model.VersionInfo,
) {
	typeName := config.Type
	configName := config.Name
	useCache := cacheTTLSeconds != 0
	ttl := max(cacheTTLSeconds, 0)

	if config.ListURLTemplate == "" {
		slog.Debug("Skipping type with missing list_url_template", "type", typeName)
		result[typeName] = []model.VersionInfo{}
		return
	}

	resolvedCIVersion := ciVersion
	if resolvedCIVersion == "" {
		resolvedCIVersion = infra.DefaultFirecrackerCIVersion
	}

	s3VersionPattern := config.Options.S3VersionPattern
	if s3VersionPattern == "" {
		s3VersionPattern = `([\d.]+)`
	}

	listVars := map[string]string{
		"ci_version": resolvedCIVersion,
		"arch":       arch,
		"version":    config.Version,
	}
	listURL, err := infra.RenderTemplate(config.ListURLTemplate, listVars)
	if err != nil {
		slog.Debug("Failed to render S3 list URL", "type", typeName, "error", err)
		result[typeName] = []model.VersionInfo{}
		return
	}
	xmlContent, err := r.fetchRawContent(ctx, listURL, useCache, ttl)
	if err != nil {
		slog.Debug("Failed to fetch S3 version listing", "type", typeName, "url", listURL, "error", err)
		result[typeName] = []model.VersionInfo{}
		return
	}

	var bucketResult S3ListBucketResult
	if err := xml.Unmarshal([]byte(xmlContent), &bucketResult); err != nil {
		slog.Debug("Failed to parse S3 XML for type", "type", typeName, "error", err)
		result[typeName] = []model.VersionInfo{}
		return
	}

	reVersion := regexp.MustCompile(s3VersionPattern)
	seenVersions := make(map[string]bool)
	var s3Versions []model.VersionInfo

	for _, contents := range bucketResult.Contents {
		key := contents.Key
		match := reVersion.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		versionStr := strings.TrimRight(match[1], ".")
		if seenVersions[versionStr] {
			continue
		}
		seenVersions[versionStr] = true

		downloadVars := map[string]string{
			"ci_version": resolvedCIVersion,
			"arch":       arch,
			"version":    versionStr,
		}

		var downloadURL string
		if config.DownloadURL != "" {
			downloadURL, err = infra.RenderTemplate(config.DownloadURL, downloadVars)
			if err != nil {
				slog.Debug(
					"Failed to render download URL for version",
					"type",
					typeName,
					"version",
					versionStr,
					"error",
					err,
				)
				continue
			}
		} else if config.Source != "" {
			downloadURL = fmt.Sprintf("%s/%s", strings.TrimRight(config.Source, "/"), key)
		}

		var sha256URL string
		if config.SHA256URL != "" {
			var rendered string
			rendered, err = infra.RenderTemplate(config.SHA256URL, downloadVars)
			if err != nil {
				slog.Debug(
					"Failed to render sha256 URL for version",
					"type",
					typeName,
					"version",
					versionStr,
					"error",
					err,
				)
			} else {
				sha256URL = rendered
			}
		}

		displayName := strings.TrimSpace(fmt.Sprintf("%s %s", configName, versionStr))
		if config.VersionNameTmpl != "" {
			if dn, err := infra.RenderTemplate(config.VersionNameTmpl, map[string]string{
				"version":    versionStr,
				"ci_version": resolvedCIVersion,
				"type":       typeName,
			}); err == nil {
				displayName = dn
			}
		}

		s3Versions = append(s3Versions, model.VersionInfo{
			Version:     versionStr,
			DownloadURL: downloadURL,
			SHA256URL:   sha256URL,
			DisplayName: displayName,
			Type:        typeName,
			Format:      config.Format,
		})
	}

	sort.Slice(s3Versions, func(i, j int) bool {
		ki := versionSortKey(s3Versions[i].Version)
		kj := versionSortKey(s3Versions[j].Version)
		for idx := 0; idx < len(ki) && idx < len(kj); idx++ {
			if ki[idx] != kj[idx] {
				return ki[idx] > kj[idx]
			}
		}
		return len(ki) > len(kj)
	})

	cfgLimit := config.Limit
	if cfgLimit <= 0 {
		cfgLimit = 5
	}

	if len(s3Versions) == 0 {
		// No images found for this CI version — emit a warning and a marker
		// entry so the type still shows in listings.
		slog.Debug("Firecracker CI has no images", "ci_version", resolvedCIVersion, "url", listURL)
		s3Versions = append(s3Versions, model.VersionInfo{
			Version:     "",
			DisplayName: fmt.Sprintf("No images for %s", resolvedCIVersion),
			Type:        typeName,
			Format:      config.Format,
		})
	} else if cfgLimit > 0 && len(s3Versions) > cfgLimit {
		s3Versions = s3Versions[:cfgLimit]
	}
	result[typeName] = s3Versions
}
