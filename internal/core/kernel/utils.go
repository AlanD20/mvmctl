package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// ── Type definitions ────────────────────────────────────────────────────────

// ParsedKernelFilename corresponds to Python's ParsedKernelFilename.
type ParsedKernelFilename struct {
	BaseName string
	Version  string
	Arch     string
}

// ── String / slice helpers ──────────────────────────────────────────────────

// TODO(verdict#33): belongs in infra/slices or similar shared utility
func makeSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// TODO(verdict#33): belongs in infra/strings or similar shared utility
func majorMinorFromVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// ── Map / config parsing helpers ────────────────────────────────────────────

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func requireStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrPtr(m map[string]any, key string) *string {
	v, _ := m[key].(string)
	if v == "" {
		return nil
	}
	return &v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalIntPtr(m map[string]any, key string) *int {
	v, ok := m[key].(int)
	if !ok {
		if f, ok := m[key].(float64); ok {
			v = int(f)
		} else {
			return nil
		}
	}
	if v == 0 {
		return nil
	}
	return &v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrFromPtr(m map[string]any, parent, key string) *string {
	if p, ok := m[parent].(map[string]any); ok {
		v, _ := p[key].(string)
		if v != "" {
			return &v
		}
	}
	return nil
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalStrFrom(m map[string]any, parent, key string) string {
	if p, ok := m[parent].(map[string]any); ok {
		v, _ := p[key].(string)
		return v
	}
	return ""
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func requireStrList(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func optionalInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func parseSetValList(m map[string]any, key string) [][2]string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var result [][2]string
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			continue
		}
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			result = append(result, [2]string{parts[0], parts[1]})
		}
	}
	return result
}

// Helper to get a string list from options map
func getStringListOption(opts map[string]any, key string) []string {
	if opts == nil {
		return nil
	}
	raw, ok := opts[key]
	if !ok {
		return nil
	}
	rawList, ok := raw.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range rawList {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// Helper to get a string from options map
func getStringOption(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	v, _ := opts[key].(string)
	return v
}

// TODO(verdict#33): belongs in infra/maps or similar shared utility
func getStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// ── Kernel-specific helpers ─────────────────────────────────────────────────

// extractVersionFromKey extracts the version from a Firecracker S3 key.
// e.g. "firecracker-ci/v1.15/x86_64/vmlinux-6.1.155" → "6.1.155"
func extractVersionFromKey(key string) string {
	idx := strings.LastIndex(key, "/vmlinux-")
	if idx < 0 {
		return key
	}
	return key[idx+len("/vmlinux-"):]
}

// TODO(verdict#33): belongs in infra/config or similar shared utility
func extractConfigKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "# ") && strings.HasSuffix(line, " is not set") {
		key := line[2 : len(line)-11]
		if strings.HasPrefix(key, "CONFIG_") {
			return key
		}
		return ""
	}
	if strings.HasPrefix(line, "CONFIG_") && strings.Contains(line, "=") {
		return strings.SplitN(line, "=", 2)[0]
	}
	return ""
}

// parseKernelConfig reads .config and returns a set of enabled settings.
// A setting "FOO" is enabled if its line is "FOO=y", "FOO=m", or "FOO=value".
// A setting "# FOO is not set" means disabled and is excluded.
func parseKernelConfig(kernelDir string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(kernelDir, ".config"))
	if err != nil {
		return nil, err
	}
	// Normalize line endings (matching Python's splitlines())
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	settings := make(map[string]bool)
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq != -1 {
			settings[line[:eq]] = true
		}
	}
	return settings, nil
}

// TODO(verdict#33): belongs in infra/config or similar shared utility
func mergeConfigLines(content, configPath string) {
	existing := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existing = string(data)
	}

	existingLines := strings.Split(existing, "\n")
	keyToIdx := make(map[string]int)
	for i, line := range existingLines {
		if key := extractConfigKey(line); key != "" {
			keyToIdx[key] = i
		}
	}

	for _, fragLine := range strings.Split(content, "\n") {
		normalized := strings.TrimSpace(fragLine)
		if key := extractConfigKey(normalized); key != "" {
			if idx, ok := keyToIdx[key]; ok {
				existingLines[idx] = normalized
			} else {
				keyToIdx[key] = len(existingLines)
				existingLines = append(existingLines, normalized)
			}
		}
	}

	merged := strings.Join(existingLines, "\n") + "\n"
	os.WriteFile(configPath, []byte(merged), 0644)
}

// runConfigScript runs scripts/config with the given args, logging a warning on failure.
// Matches Python's KernelService._run_config_script() which captures stderr separately.
func runConfigScript(ctx context.Context, configScript, kernelDir string, args ...string) {
	cmdArgs := []string{configScript}
	cmdArgs = append(cmdArgs, args...)
	result := system.RunCmdCompat(ctx, cmdArgs, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   false,
	})
	exitCode := result.ExitCode
	if result.Err != nil || exitCode != 0 {
		slog.Warn("scripts/config failed",
			"args", strings.Join(args, " "),
			"rc", exitCode,
			"stderr", strings.TrimSpace(result.Stderr))
	}
}

func runMake(ctx context.Context, kernelDir, target string, jobs int) (int, string, string) {
	result := system.RunCmdCompat(ctx, []string{"make", target, fmt.Sprintf("-j%d", jobs)}, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   false,
	})
	stdoutStr := result.Stdout
	stderrStr := result.Stderr
	// Log config warnings from make output (matching Python)
	for _, line := range strings.Split(stderrStr, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.Contains(stripped, ".config:") || strings.Contains(strings.ToLower(stripped), "warning:") {
			slog.Debug("Config warning", "message", stripped)
		}
	}
	return result.ExitCode, stdoutStr, stderrStr
}

// checkBuildDependencies checks for required kernel build dependencies.
func checkBuildDependencies(ctx context.Context) error {
	requiredCommands := []string{
		"git", "curl", "make", "gcc", "flex", "bison", "bc", "pahole", "ld",
	}
	var missing []string
	for _, cmd := range requiredCommands {
		result := system.RunCmdCompat(ctx, []string{"which", cmd}, system.RunCmdOpts{Capture: true, Check: false})
		if result.ExitCode != 0 {
			missing = append(missing, cmd)
		}
	}
	libraryChecks := []struct {
		pkg, display string
	}{
		{"libelf", "libelf"},
		{"openssl", "libssl-dev"},
	}
	for _, lc := range libraryChecks {
		result := system.RunCmdCompat(
			ctx,
			[]string{"pkg-config", "--exists", lc.pkg},
			system.RunCmdOpts{Check: true},
		)
		if result.Err != nil {
			missing = append(missing, lc.display)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		missingStr := strings.Join(missing, ", ")
		return NewKernelErrorf(
			"Missing kernel build dependencies: %s\n\n"+
				"Install on Ubuntu/Debian:\n"+
				"  sudo apt update\n"+
				"  sudo apt install -y build-essential libncurses-dev bison flex\n"+
				"  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves\n\n"+
				"Install on Arch Linux:\n"+
				"  sudo pacman -S base-devel ncurses bison flex\n"+
				"  sudo pacman -S openssl bc curl git pahole\n",
			missingStr)
	}
	return nil
}

// ParseFilename parses a kernel filename to extract base name, version, and arch.
// Matches Python's KernelService.parse_filename().
func ParseFilename(filename string) ParsedKernelFilename {
	name := filename
	arches := infra.FirecrackerSupportedArches
	version := "-"
	arch := "-"

	for _, a := range arches {
		if strings.HasSuffix(name, "-"+a) {
			arch = a
			name = name[:len(name)-len(a)-1]
			break
		}
	}

	versionRe := regexp.MustCompile(`-v?(\d+(?:\.\d+)*)$`)
	if m := versionRe.FindStringSubmatch(name); len(m) >= 2 {
		versionNum := m[1]
		fullMatch := m[0]
		if strings.HasPrefix(fullMatch, "-v") {
			version = "v" + versionNum
		} else {
			version = versionNum
		}
		name = name[:len(name)-len(fullMatch)]
	}

	baseName := strings.Split(name, "-")[0]
	return ParsedKernelFilename{BaseName: baseName, Version: version, Arch: arch}
}

// ── Version resolution helpers ─────────────────────────────────────────────

// kernelSpecsToResolverConfigs converts a list of KernelSpec to ResolverConfig structs
// for delegation to the shared HttpDirVersionResolver.
func kernelSpecsToResolverConfigs(specs []*model.KernelSpec) []download.ResolverConfig {
	configs := make([]download.ResolverConfig, 0, len(specs))
	for _, spec := range specs {
		cfg := download.ResolverConfig{
			Type: spec.KernelType,
			Name: spec.Name,
		}

		if spec.Resolver != nil {
			cfg.Resolver = *spec.Resolver
		}
		if spec.VersionsURL != nil {
			cfg.VersionsURL = *spec.VersionsURL
		}
		if spec.Source != "" {
			cfg.Source = spec.Source
		}
		if spec.SHA256URL != "" {
			cfg.SHA256URL = spec.SHA256URL
		}
		if spec.Version != "" {
			cfg.Version = spec.Version
		}

		if spec.KernelType == infra.KernelTypeOfficial {
			cfg.Format = "tar.xz"
		} else {
			cfg.Format = "vmlinux"
		}

		resolver := ""
		if spec.Resolver != nil {
			resolver = *spec.Resolver
		}

		switch resolver {
		case "http-dir":
			if spec.VersionsURL != nil && *spec.VersionsURL != "" {
				cfg.DownloadURL = spec.Source
				if spec.SHA256URL != "" {
					cfg.SHA256URL = spec.SHA256URL
				}
				filePattern := "linux-"
				if spec.FilePattern != nil {
					filePattern = *spec.FilePattern
				}
				fileSuffix := ".tar.xz"
				if spec.FileSuffix != nil {
					fileSuffix = *spec.FileSuffix
				}
				discoveries := getStringSlice(spec.Options, "version_discoveries")
				cfg.Options = download.ResolverOptions{
					VersionDiscoveries: discoveries,
					FilePattern:        filePattern,
					FileSuffix:         fileSuffix,
				}
			}
		case "firecracker-s3":
			if spec.ListURLTemplate != nil && *spec.ListURLTemplate != "" {
				cfg.ListURLTemplate = *spec.ListURLTemplate
				// Strip {version} from list_url_template for listing purposes
				cfg.ListURLTemplate = strings.ReplaceAll(cfg.ListURLTemplate, "{version}", "")
				// Download URL template
				sourceBase := strings.TrimRight(spec.Source, "/")
				cfg.DownloadURL = fmt.Sprintf("%s/firecracker-ci/{ci_version}/{arch}/vmlinux-{version}", sourceBase)
				if spec.SHA256URL != "" {
					cfg.SHA256URL = spec.SHA256URL
				}
				s3Pattern := "vmlinux-([\\d.]+)"
				if spec.Options != nil {
					if p, ok := spec.Options["s3_version_pattern"].(string); ok && p != "" {
						s3Pattern = p
					}
				}
				cfg.Options = download.ResolverOptions{
					S3VersionPattern: s3Pattern,
				}
			}
		default:
		}

		configs = append(configs, cfg)
	}
	return configs
}

// resolverConfigsFromMaps converts []map[string]any to []download.ResolverConfig.
func resolverConfigsFromMaps(configs []map[string]any) []download.ResolverConfig {
	result := make([]download.ResolverConfig, 0, len(configs))
	for _, m := range configs {
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
		if v, ok := m["source"].(string); ok {
			cfg.Source = v
		}
		if v, ok := m["version"].(string); ok {
			cfg.Version = v
		}

		if optsRaw, ok := m["options"].(map[string]any); ok {
			if v, ok := optsRaw["version_discoveries"].([]any); ok {
				cfg.Options.VersionDiscoveries = make([]string, len(v))
				for i, item := range v {
					cfg.Options.VersionDiscoveries[i], _ = item.(string)
				}
			}
			if v, ok := optsRaw["file_pattern"].(string); ok {
				cfg.Options.FilePattern = v
			}
			if v, ok := optsRaw["file_suffix"].(string); ok {
				cfg.Options.FileSuffix = v
			}
			if v, ok := optsRaw["s3_version_pattern"].(string); ok {
				cfg.Options.S3VersionPattern = v
			}
		}

		result = append(result, cfg)
	}
	return result
}

// extractVMName extracts the "name" from a VM object.
// VMs are now typed as *model.VM from the shared model package,
// so we access Name directly without reflection.
func extractVMName(vm *model.VM) string {
	return vm.Name
}
