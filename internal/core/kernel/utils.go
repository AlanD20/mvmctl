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
	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// ── Type definitions ────────────────────────────────────────────────────────

// ParsedKernelFilename corresponds to Python's ParsedKernelFilename.
type ParsedKernelFilename struct {
	BaseName string
	Version  string
	Arch     string
}

// ── String / slice helpers ──────────────────────────────────────────────────

func majorMinorFromVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// ── Kernel-specific helpers ─────────────────────────────────────────────────

func extractVersionFromKey(key string) string {
	idx := strings.LastIndex(key, "/vmlinux-")
	if idx < 0 {
		return key
	}
	return key[idx+len("/vmlinux-"):]
}

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
	result, err := system.DefaultRunner.Run(ctx, cmdArgs, system.RunCmdOpts{
		Cwd:     kernelDir,
		Capture: true,
		Check:   false,
	})
	exitCode := result.ExitCode
	if err != nil || !result.Success() {
		slog.Warn("scripts/config failed",
			"args", strings.Join(args, " "),
			"rc", exitCode,
			"stderr", strings.TrimSpace(result.Stderr))
	}
}

func runMake(ctx context.Context, kernelDir, target string, jobs int) (int, string, string) {
	result, _ := system.DefaultRunner.Run(ctx, []string{"make", target, fmt.Sprintf("-j%d", jobs)}, system.RunCmdOpts{
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
	var missing []string
	for _, cmd := range KernelBuildCommands {
		result, _ := system.DefaultRunner.Run(ctx, []string{"which", cmd}, system.RunCmdOpts{Capture: true, Check: false})
		if !result.Success() {
			missing = append(missing, cmd)
		}
	}

	for _, lc := range KernelBuildLibraries {
		_, err := system.DefaultRunner.Run(
			ctx,
			[]string{"pkg-config", "--exists", lc.Pkg},
			system.RunCmdOpts{Check: true},
		)
		if err != nil {
			missing = append(missing, lc.Display)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		missingStr := strings.Join(missing, ", ")
		return errs.New(errs.CodeKernelBuildFailed, fmt.Sprintf(
			"Missing kernel build dependencies: %s\n\n"+
				"Install on Ubuntu/Debian:\n"+
				"  sudo apt update\n"+
				"  sudo apt install -y build-essential libncurses-dev bison flex\n"+
				"  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves\n\n"+
				"Install on Arch Linux:\n"+
				"  sudo pacman -S base-devel ncurses bison flex\n"+
				"  sudo pacman -S openssl bc curl git pahole\n",
			missingStr), errs.WithClass(errs.ClassInternal))
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
				var discoveries []string
				if spec.Options != nil {
					if raw, ok := spec.Options["version_discoveries"].([]any); ok {
						for _, item := range raw {
							if s, ok := item.(string); ok {
								discoveries = append(discoveries, s)
							}
						}
					}
				}
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
