package infra

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ── Identity ──
const BootstrapName = "mvmctl"

// CLIName is the canonical CLI name. In Go, unlike Python (which needed dynamic
// resolution for Nuitka console_scripts entry points), the binary name is
// always "mvm" — a compiled Go binary has one name.
const CLIName = "mvm"

// ProjectNameDefault is the compile-time constant default for the project name.
const ProjectNameDefault = "mvmctl"

// ProjectName is the runtime project name, defaulting to "mvmctl".
// It can be overridden at build time via:
//
//	-ldflags "-X mvmctl/internal/infra.ProjectName=customname"
//
// This matches Python's PROJECT_NAME which resolves from importlib.metadata
// or falls back to _BOOTSTRAP_NAME ("mvmctl").
var ProjectName = ProjectNameDefault

// MVMUnixGroup is the Unix group name for mvm privilege management.
// In Go this is always the same as the CLI name.
const MVMUnixGroup = CLIName

const MVMDBFilename = "mvmdb.db"

func MVMFwdChain() string {
	return fmt.Sprintf("%s-FORWARD", strings.ToUpper(CLIName))
}

func MVMPostroutingChain() string {
	return fmt.Sprintf("%s-POSTROUTING", strings.ToUpper(CLIName))
}

func MVMNocloudNetInputChain() string {
	return fmt.Sprintf("%s-NOCLOUDNET-INPUT", strings.ToUpper(CLIName))
}

func SudoersDropInPath() string {
	return fmt.Sprintf("/etc/sudoers.d/%s", CLIName)
}

// ── User-overridable defaults ──
var OverridableDefaults = map[string]map[string]any{
	"settings.vm": {
		"max_vms":    1000,
		"log_lines":  50,
		"log_follow": false,
	},
	"defaults.vm": {
		"vcpu_count":       1,
		"mem_size_mib":     512,
		"ssh_user":         "root",
		"user_password":    "password",
		"dns_server":       "1.1.1.1",
		"root_uid":         0,
		"root_gid":         0,
		"user_uid":         1000,
		"user_gid":         1000,
		"pci_enabled":      true,
		"nested_virt":      false,
		"enable_logging":   true,
		"enable_metrics":   false,
		"enable_console":   true,
		"lsm_flags":        "landlock,lockdown,yama,integrity,selinux,bpf",
		"boot_args":        "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait quiet loglevel=3 no_timer_check clocksource=kvm-clock systemd.show_status=false",
		"guest_mac_prefix": "02:FC",
	},
	"defaults.network": {
		"name":        "net",
		"subnet":      "172.27.0.0/24",
		"nat_enabled": true,
	},
	"defaults.image": {
		"arch":                  "x86_64",
		"import_format":         "auto",
		"remote_list_limit":     5,
		"remote_list_cache_ttl": 3600,
	},
	"defaults.kernel": {
		"arch":                  "x86_64",
		"version":               "6.19.9",
		"build_jobs":            nil,
		"remote_list_limit":     5,
		"remote_list_cache_ttl": 14400,
	},
	"defaults.firecracker": {
		"log_level":               "Debug",
		"log_filename":            "firecracker.log",
		"serial_output_filename":  "firecracker.console.log",
		"metrics_filename":        "firecracker.metrics",
		"api_socket_filename":     "firecracker.api.socket",
		"pid_filename":            "firecracker.pid",
		"config_filename":         "firecracker.json",
		"console_socket_filename": "console.sock",
		"console_pid_filename":    "console.pid",
	},
	"defaults.cloudinit": {
		"iso_name":                 "cloud-init.iso",
		"nocloud_port_range_start": 8000,
		"nocloud_port_range_end":   9000,
		"nocloud_max_port_retries": 100,
	},
	"defaults.binary": {
		"remote_version_limit": 5,
	},
	"cli": {
		"listing_style": "short",
	},
	"settings": {
		"guestfs_enabled":  false,
		"firewall_backend": "nftables",
	},
	"settings.firewall": {
		"iptables_xtcomment": true,
	},
}

func GetDefault(category, key string) (any, error) {
	cat, ok := OverridableDefaults[category]
	if !ok {
		return nil, fmt.Errorf("default category not found: %s", category)
	}
	val, ok := cat[key]
	if !ok {
		return nil, fmt.Errorf("default key not found: %s", key)
	}
	return val, nil
}

// ── VM Limits ──
const MemMinMB = 128
const MemMaxMB = 65536
const VCPUMin = 1
const VCPUMax = 32
const SignalExitCodeBase = 128

// ── VM Lifecycle ──
const LogFollowPollIntervalS = 0.3

// ── Network ──
const ConstIPTablesMaxCommentLen = 240
const DefaultIPLocalPortRangeStart = 32768
const DefaultIPLocalPortRangeEnd = 60999

// DefaultIPLocalPortRange is the default ip_local_port_range used when
// /proc/sys/net/ipv4/ip_local_port_range cannot be read.
var DefaultIPLocalPortRange = [2]int{DefaultIPLocalPortRangeStart, DefaultIPLocalPortRangeEnd}

// ── File permissions ──
const PrivateKeyPerm = 0600
const PublicKeyPerm = 0644
const CacheDirPerm = 0700
const SudoersPerm = 0440
const DBFilePerm = 0640
const ExecutablePerm = 0755
const ShadowPerm = 0640

// ── HTTP defaults ──

const HTTPTimeout = 300 * time.Second
const HTTPChunkSize = 1 << 20 // 1 MiB
const HTTPMaxRetries = 3
const HTTPRetryDelay = 1 * time.Second
const HTTPBackoffFactor = 2
const DefaultUserAgent = "mvmctl/dev"
const SocketTimeoutSeconds = 5.0
const PollStepSeconds = 0.1

// ── HTTP status codes ──
const HTTPStatusNoContent = 204
const HTTPStatusSuccess = 200

// ── HTTP timeouts ──
const HTTPTimeoutKernelDownloadS = 600
const HTTPTimeoutKernelConfigS = 60
const HTTPTimeoutSha256FetchS = 30
const HTTPTimeoutSha256SidecarS = 15

// ── Default versions ──
const DefaultFirecrackerCIVersion = "v1.15"
const MinKernelMajor = 5
const MinKernelMinor = 10

// ── Default VM config ──
const DefaultVCPUCount = 1
const DefaultMemoryMiB = 512
const DefaultSSHUser = "root"
const DefaultDNS = "1.1.1.1"
const DefaultGuestMACPrefix = "02:FC"

// ── Default network ──
const DefaultNetworkSubnet = "172.27.0.0/24"

// ── Image processing ──
const RuntimeBufferMB = 160
const ShrinkSafetyMargin = 1.01
const RatioMin = 1.0
const MinRootfsSizeMiB = 128
const RootfsHeadroomFactor = 1.25
const RootfsMinHeadroomBytes = 150 * 1024 * 1024
const Percent = 100

// ── Buffer / sector ──
const BufferSizeBytes = 1024

// ── Download retry backoff (float64 to match Python's CONST_DOWNLOAD_RETRY_BACKOFF) ──
const DownloadRetryBackoff = 2.0

// ── Cloud-init ──
const RequiredISOTool = "cloud-localds"
const NoCloudNetBindTimeoutS = 5.0

// ── Console ──
const ConsoleSocketTimeoutS = 2.0
const ConsoleKillTimeoutS = 5.0

// ── Kernel types ──
const KernelTypeFirecracker = "firecracker"
const KernelTypeOfficial = "official"

// ── Shadow file ──
const ShadowDaysSinceEpoch = 19700
const ShadowMinDays = 0
const ShadowMaxDays = 99999
const ShadowWarnDays = 7

// ── Supported image extensions ──
var SupportedImageExtensions = []string{
	".ext4",
	".btrfs",
	".img",
	".raw",
	".ext4.zst",
	".btrfs.zst",
}

// ── Image import format map ──
var ImageImportFormatMap = map[string]string{
	".qcow2":  "qcow2",
	".raw":    "raw",
	".img":    "raw",
	".ext4":   "raw",
	".ext3":   "raw",
	".ext2":   "raw",
	".btrfs":  "raw",
	".xfs":    "raw",
	".vhd":    "vhd",
	".vhdx":   "vhdx",
	".tar":    "tar-rootfs",
	".tar.gz": "tar-rootfs",
	".tar.xz": "tar-rootfs",
	".tgz":    "tar-rootfs",
}

// ── Binary size ──
const MinBinarySizeBytes = 512

// ── Host system paths ──
const DefaultSysctlConfDir = "/etc/sysctl.d"
const DefaultSudoersDir = "/etc/sudoers"
const DefaultSysctlConfPath = "/etc/sysctl.d/mvmctl.conf"

// ── Libguestfs ──
const DefaultLibguestfsSeedDir = "/var/lib/cloud/seed/nocloud"

// ── Firecracker GitHub ──
const FirecrackerGithubReleasesAPIURL = "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
const FirecrackerGithubDownloadURL = "https://github.com/firecracker-microvm/firecracker/releases/download"
const FirecrackerGitRepoURL = "https://github.com/firecracker-microvm/firecracker.git"

// ── Privileged system binaries ──
var PrivilegedBinaries = map[string]string{
	"/usr/sbin/ip":               "iproute2",
	"/usr/sbin/iptables":         "iptables",
	"/usr/sbin/iptables-restore": "iptables",
	"/usr/sbin/iptables-save":    "iptables",
	"/usr/sbin/nft":              "nftables",
	"/usr/sbin/sysctl":           "procps",
	"/usr/sbin/modprobe":         "kmod",
}

// PrivilegedBinariesOrdered returns the keys of PrivilegedBinaries in
// insertion order, matching Python's PRIVILEGED_BINARIES dict literal order.
// Go maps have random iteration, so an ordered slice is needed for
// deterministic sudoers content generation.
var PrivilegedBinariesOrdered = [...]string{
	"/usr/sbin/ip",
	"/usr/sbin/iptables",
	"/usr/sbin/iptables-restore",
	"/usr/sbin/iptables-save",
	"/usr/sbin/nft",
	"/usr/sbin/sysctl",
	"/usr/sbin/modprobe",
}

// ── Init binaries ──
var InitBinaries = []string{
	"ip",
	"modprobe",
	"lsmod",
	"groupadd",
	"usermod",
	"groupdel",
	"visudo",
	"ssh-keygen",
	"tar",
}

// ── Infra binaries ──
var InfraBinaries = []string{
	"qemu-img",
	"mkfs.ext4",
	"blkid",
	"sfdisk",
	"dumpe2fs",
}

// ── Required binaries ──
var RequiredBinaries = []string{
	"ip", "modprobe", "lsmod", "groupadd", "usermod",
	"groupdel", "visudo", "ssh-keygen", "tar",
	"qemu-img", "mkfs.ext4", "blkid", "sfdisk", "dumpe2fs",
	"iptables", "nft",
}

// CloudInitMode type moved to mvmctl/internal/infra/model (model.CloudInitMode).
// ProvisionerType type moved to mvmctl/internal/infra/model (model.ProvisionerType).

// ── Debug ──
var debugMode = false

func SetDebugMode(v bool) { debugMode = v }
func IsDebugMode() bool   { return debugMode }

// ── Compiled mode ──
//

// ══════════════════════════════════════════════════════════════════════════════
// Environment variable access
// ══════════════════════════════════════════════════════════════════════════════

func EnvKey(suffix string) string {
	return fmt.Sprintf("%s_%s", strings.ToUpper(CLIName), suffix)
}

// EnvGet returns the environment variable value and whether it was set.
// Python's env.get(suffix) returns str|None — Go maps None to "" and
// the bool indicates if the variable was present in the environment.
func EnvGet(suffix string) (string, bool) {
	val, ok := os.LookupEnv(EnvKey(suffix))
	return val, ok
}

func EnvGetDefault(suffix, defaultVal string) string {
	val := os.Getenv(EnvKey(suffix))
	if val == "" {
		return defaultVal
	}
	return val
}

func EnvSet(suffix, value string) {
	os.Setenv(EnvKey(suffix), value)
}

// ══════════════════════════════════════════════════════════════════════════════
// CacheUtils — directory/path resolution
// ══════════════════════════════════════════════════════════════════════════════

// ensureDirAndChown creates the directory with 0700 permissions (matching
// Python's CONST_DIR_PERMS_CACHE = 0o700) and chowns to the real invoking
// user when running under sudo. Mirrors Python's CacheUtils.resolve_dir().
func ensureDirAndChown(path string) error {
	if err := os.MkdirAll(path, CacheDirPerm); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	ChownToRealUser(path)
	return nil
}

func GetRealHome() string {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser != "" {
		u, err := user.Lookup(sudoUser)
		if err == nil {
			return u.HomeDir
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/root"
	}
	return home
}

func GetCacheDir() (string, error) {
	override, ok := EnvGet("CACHE_DIR")
	if ok && override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("invalid cache dir path: %w", err)
		}
		// Ensure the directory exists with proper permissions (matching
		// Python's CacheUtils.resolve_dir which creates the directory).
		if err := ensureDirAndChown(resolved); err != nil {
			return "", fmt.Errorf("create cache dir: %w", err)
		}
		return resolved, nil
	}
	path := filepath.Join(GetRealHome(), ".cache", ProjectName)
	if err := ensureDirAndChown(path); err != nil {
		return "", fmt.Errorf("create default cache dir: %w", err)
	}
	return path, nil
}

func GetConfigDir() (string, error) {
	override, ok := EnvGet("CONFIG_DIR")
	if ok && override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("invalid config dir path: %w", err)
		}
		// Ensure the directory exists with proper permissions (matching
		// Python's CacheUtils.resolve_dir which creates the directory).
		if err := ensureDirAndChown(resolved); err != nil {
			return "", fmt.Errorf("create config dir: %w", err)
		}
		return resolved, nil
	}
	path := filepath.Join(GetRealHome(), ".config", ProjectName)
	if err := ensureDirAndChown(path); err != nil {
		return "", fmt.Errorf("create default config dir: %w", err)
	}
	return path, nil
}

func GetMvmDBPath() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	return filepath.Join(cacheDir, MVMDBFilename)
}

func GetTempDir() string {
	override, ok := EnvGet("TEMP_DIR")
	if ok && override != "" {
		if err := ensureDirAndChown(override); err != nil {
			slog.Warn("failed to create temp directory", "path", override, "error", err)
		}
		return override
	}
	path := filepath.Join("/tmp", ProjectName)
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create temp directory", "path", path, "error", err)
	}
	return path
}

func GetVmsDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "vms")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create vms directory", "path", path, "error", err)
	}
	return path
}

func GetVmDir(id string) string {
	return filepath.Join(GetVmsDir(), id)
}

func GetImagesDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "images")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create images directory", "path", path, "error", err)
	}
	return path
}

func GetKernelsDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "kernels")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create kernels directory", "path", path, "error", err)
	}
	return path
}

func GetKeyDir() string {
	configDir, err := GetConfigDir()
	if err != nil {
		configDir = filepath.Join(GetRealHome(), ".config", ProjectName)
	}
	path := filepath.Join(configDir, "keys")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create keys directory", "path", path, "error", err)
	}
	return path
}

func GetVolumesDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "volumes")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create volumes directory", "path", path, "error", err)
	}
	return path
}

func GetBinDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "bin")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create bin directory", "path", path, "error", err)
	}
	return path
}

func GetLogsDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "logs")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create logs directory", "path", path, "error", err)
	}
	return path
}

func GetAuditLogPath() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	return filepath.Join(cacheDir, "audit.log")
}

func GetLogPath() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	logPath := filepath.Join(cacheDir, "mvmctl.log")
	os.MkdirAll(filepath.Dir(logPath), 0755)
	return logPath
}

func GetTimingLogPath() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	return filepath.Join(cacheDir, "timing.log")
}

// ── Warm image directory ──
func GetWarmImageDir(tmpPath string) string {
	base := tmpPath
	if base == "" {
		base = os.TempDir()
	}
	path := filepath.Join(base, ProjectName, "ready")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create warm image directory", "path", path, "error", err)
	}
	return path
}

// ══════════════════════════════════════════════════════════════════════════════
// CommonUtils — domain-agnostic helpers
// ══════════════════════════════════════════════════════════════════════════════

// ReservedNames that cannot be used as entity names
var ReservedNames = map[string]bool{
	"help": true, "all": true, "default": true, "none": true,
	"root": true, "self": true, "system": true,
	"true": true, "false": true, "yes": true, "no": true,
	"on": true, "off": true, "nil": true, "null": true,
}

var DangerousChars = func() map[rune]bool {
	chars := make(map[rune]bool)
	for _, c := range ";|&$`\\\"'\n\r\t<>{}[]()" {
		chars[c] = true
	}
	for _, c := range "./~\\" {
		chars[c] = true
	}
	for i := range 32 {
		chars[rune(i)] = true
	}
	for i := range 32 {
		chars[rune(i)] = true
	}
	chars[127] = true
	chars[0x200b] = true
	chars[0x200c] = true
	chars[0x200d] = true
	chars[0xfeff] = true
	return chars
}()

func ContainsDangerousChars(value string) bool {
	for _, c := range value {
		if DangerousChars[c] {
			return true
		}
	}
	return false
}

func IsReservedName(name string) bool {
	return ReservedNames[strings.ToLower(name)]
}

var _controlChars = func() map[rune]bool {
	chars := make(map[rune]bool)
	for i := range 32 {
		chars[rune(i)] = true
	}
	chars[127] = true
	return chars
}()

var _zeroWidthChars = map[rune]bool{
	'\u200b': true,
	'\u200c': true,
	'\u200d': true,
	'\ufeff': true,
}

func SanitizeForLog(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, c := range value {
		if !_controlChars[c] && !_zeroWidthChars[c] {
			result.WriteRune(c)
		}
	}
	return result.String()
}

// ── CommonUtils helpers ──

// Coerce coerces a value to a target kind, matching Python's CommonUtils.coerce().
//
// NOTE ON PYTHON TYPE NAMES: The case labels below use Python-style type names
// ("str", "dict", "NoneType") because the target parameter originates from DB
// persistence — these string values were stored by the Python codebase and must
// be kept for backward compatibility. The dispatch preserves the stored values.
// Error messages to users use Go-native names ("string", "map", "nil").
//
// Python semantics:
//   - bool is a subclass of int: True→1, False→0 when target is int
//   - string→bool via truthy keywords: "true"/"1"/"yes"/"on"
//   - string→int via strconv.Atoi
//   - string→float via strconv.ParseFloat
//   - string→dict via json.Unmarshal
//   - identity (already correct type): returns as-is
//
// target is a string like "bool", "int", "float", "string", "map", "nil".
func Coerce(value any, target string) (any, error) {
	switch target {
	case "bool":
		switch v := value.(type) {
		case bool:
			return v, nil
		case string:
			lower := strings.ToLower(strings.TrimSpace(v))
			return lower == "true" || lower == "1" || lower == "yes" || lower == "on", nil
		case int:
			return v == 1, nil
		case int64:
			return v == int64(1), nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to bool", value)
		}

	case "int":
		switch v := value.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			return int(v), nil
		case bool:
			// Python: True is subclass of int, True→1, False→0
			if v {
				return 1, nil
			}
			return 0, nil
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("cannot coerce string %q to int: %w", v, err)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to int", value)
		}

	case "float":
		switch v := value.(type) {
		case float64:
			return v, nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("cannot coerce string %q to float: %w", v, err)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to float", value)
		}

	case "string":
		if s, ok := value.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("cannot coerce %T to string", value)

	case "map":
		if s, ok := value.(string); ok {
			var result map[string]any
			if err := json.Unmarshal([]byte(s), &result); err != nil {
				return nil, fmt.Errorf("cannot coerce to map: %w", err)
			}
			return result, nil
		}
		if m, ok := value.(map[string]any); ok {
			return m, nil
		}
		return nil, fmt.Errorf("cannot coerce %T to map", value)

	case "nil":
		if value == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot coerce %T to nil", value)

	default:
		return nil, fmt.Errorf("unsupported expected type: %s", target)
	}
}

func CoerceBoolFields(instance map[string]any, fieldNames []string) {
	for _, name := range fieldNames {
		if val, ok := instance[name]; ok {
			switch v := val.(type) {
			case bool:
				instance[name] = v
			case int:
				instance[name] = v == 1
			case float64:
				instance[name] = v == 1
			case string:
				lower := strings.ToLower(strings.TrimSpace(v))
				instance[name] = lower == "true" || lower == "1" || lower == "yes" || lower == "on"
			default:
				instance[name] = false
			}
		}
	}
}

func FormatBytesHumanReadable(sizeBytes int64) string {
	if sizeBytes < 1024 {
		return fmt.Sprintf("%d B", sizeBytes)
	}
	sizeFloat := float64(sizeBytes)
	units := []string{"KiB", "MiB", "GiB"}
	for _, unit := range units {
		sizeFloat /= 1024
		if sizeFloat < 1024 {
			return fmt.Sprintf("%.1f %s", sizeFloat, unit)
		}
	}
	return fmt.Sprintf("%.1f TiB", sizeFloat)
}

// ── Timestamp format constants ──
// Only Go stdlib time constants are used (ARCHITECTURE: V17 — RFC3339 everywhere).
// Legacy Python microsecond/no-timezone formats are NOT supported — DB migration
// converts them to RFC3339 on read.

func HumanReadableDatetime(isoTimestamp string) string {
	if isoTimestamp == "" {
		return "-"
	}
	// Only RFC3339 and nanosecond-precision RFC3339 are valid formats.
	// Matches Python's .isoformat() behavior with microsecond precision loss.
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
	}
	normalized := strings.Replace(isoTimestamp, "Z", "+00:00", 1)
	for _, f := range formats {
		if t, err := time.Parse(f, normalized); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return isoTimestamp
}

func GenerateBatchNames(baseName string, count int) []string {
	if count == 1 {
		return []string{baseName}
	}
	names := make([]string, count)
	names[0] = baseName
	for i := 2; i <= count; i++ {
		names[i-1] = fmt.Sprintf("%s-%d", baseName, i)
	}
	return names
}

func DeepMergeDict(base, override map[string]any) map[string]any {
	result := make(map[string]any)
	maps.Copy(result, base)
	for key, overrideVal := range override {
		if existingVal, ok := result[key]; ok {
			if existingMap, ok1 := existingVal.(map[string]any); ok1 {
				if overrideMap, ok2 := overrideVal.(map[string]any); ok2 {
					result[key] = DeepMergeDict(existingMap, overrideMap)
					continue
				}
			}
		}
		result[key] = overrideVal
	}
	return result
}

// NumCPU returns number of available CPUs, matching os.cpu_count() semantics.
func NumCPU() int {
	return runtime.NumCPU()
}

func SafeInt(value any, defaultVal int) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}
