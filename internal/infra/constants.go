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
	"sync"
	"time"
)

// --- Identity ---
const BootstrapName = "mvmctl"

// CLIName is the canonical CLI name. A compiled Go binary has one name.
const CLIName = "mvm"

// ProjectNameDefault is the compile-time constant default for the project name.
const ProjectNameDefault = "mvmctl"

// DefaultEnvSpecNames lists the default filenames to look for when no
// spec path is provided to "mvm env up". Checked in order.
var DefaultEnvSpecNames = []string{ProjectNameDefault + ".yaml", ProjectNameDefault + ".yml"}

// ProjectName is the runtime project name, defaulting to "mvmctl".
// It can be overridden at build time via:
//
//	-ldflags "-X mvmctl/internal/infra.ProjectName=customname"
var ProjectName = ProjectNameDefault

// MVMUnixGroup is the Unix group name for mvm privilege management.
// In Go this is always the same as the CLI name.
const MVMUnixGroup = CLIName

const MVMDBFilename = "mvmdb.db"

const MVMForwardChain = "MVM-FORWARD"

const MVMPostroutingChain = "MVM-POSTROUTING"

const MVMNocloudNetInputChain = "MVM-NOCLOUDNET-INPUT"

func SudoersDropInPath() string {
	return fmt.Sprintf("/etc/sudoers.d/%s", CLIName)
}

// --- User-overridable defaults ---
var OverridableDefaults = map[string]map[string]any{
	"settings.vm": {
		"max_vms":         1000,
		"log_lines":       50,
		"log_follow":      false,
		"ssh_timeout_sec": 10,
	},
	"defaults.vm": {
		"vcpu_count":          1,
		"mem_size_mib":        512,
		"ssh_user":            "root",
		"user_password":       "password",
		"dns_server":          "1.1.1.1",
		"root_uid":            0,
		"root_gid":            0,
		"user_uid":            1000,
		"user_gid":            1000,
		"pci_enabled":         true,
		"nested_virt":         false,
		"enable_logging":      true,
		"enable_metrics":      false,
		"enable_console":      true,
		"lsm_flags":           "landlock,lockdown,yama,integrity,selinux,bpf",
		"boot_args":           "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait quiet loglevel=3 no_timer_check clocksource=kvm-clock systemd.show_status=false",
		"guest_mac_prefix":    "02:FC",
		"vsock_port":          1024,
		"vsock_user":          "root",
		"vsock_probe_timeout": 5,
	},
	"defaults.network": {
		"name":        "net",
		"subnet":      "172.27.0.0/24",
		"nat_enabled": true,
	},
	"defaults.image": {
		"import_format":         "auto",
		"remote_list_limit":     5,
		"remote_list_cache_ttl": 3600,
	},
	"defaults.kernel": {
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
		"vsock_filename":          "vsock.sock",
	},
	"defaults.cloudinit": {
		"iso_name":                 "cloud-init.iso",
		"nocloud_port_range_start": 8000,
		"nocloud_port_range_end":   9000,
		"nocloud_max_port_retries": 100,
		"nocloud_kill_after":       "5m",
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

// --- VM Limits ---
const MemMinMB = 128
const MemMaxMB = 65536
const VCPUMin = 1
const VCPUMax = 32
const SignalExitCodeBase = 128

// --- VM Lifecycle ---
const LogFollowPollIntervalS = 0.3

// --- Network ---
const ConstIPTablesMaxCommentLen = 240
const DefaultIPLocalPortRangeStart = 32768
const DefaultIPLocalPortRangeEnd = 60999

// DefaultIPLocalPortRange is the default ip_local_port_range used when
// /proc/sys/net/ipv4/ip_local_port_range cannot be read.
var DefaultIPLocalPortRange = [2]int{DefaultIPLocalPortRangeStart, DefaultIPLocalPortRangeEnd}

// --- File permissions ---
const DirPerm = 0755
const PrivateKeyPerm = 0600
const PublicKeyPerm = 0644
const CacheDirPerm = 0700
const SudoersPerm = 0440
const DBFilePerm = 0640
const ExecutablePerm = 0755
const ShadowPerm = 0640

// --- Copy direction constants ---
const (
	DirectionHostToVM = "host_to_vm"
	DirectionVMToHost = "vm_to_host"
	DirectionVMToVM   = "vm_to_vm"
)

// --- Firecracker architecture support ---
var FirecrackerSupportedArches = []string{"x86_64", "amd64", "aarch64", "arm64"}

// --- HTTP defaults ---

const HTTPTimeout = 900 * time.Second
const HTTPChunkSize = 1 << 20 // 1 MiB
const HTTPMaxRetries = 3
const HTTPRetryDelay = 1 * time.Second
const HTTPBackoffFactor = 2
const DefaultUserAgent = "mvmctl/dev"
const SocketTimeoutSeconds = 5.0
const PollStepSeconds = 0.1

// --- Filesystem type <-> extension mapping ---

// FSTypeToExt maps filesystem type to file extension.
var FSTypeToExt = map[string]string{
	"ext4":  ".ext4",
	"ext3":  ".ext4",
	"ext2":  ".ext4",
	"btrfs": ".btrfs",
	"xfs":   ".xfs",
}

// ExtToFSType maps file extension to filesystem type.
var ExtToFSType = map[string]string{
	".ext4":  "ext4",
	".btrfs": "btrfs",
	".xfs":   "xfs",
}

// QemuImgFormat maps disk image formats to qemu-img convert -f flags.
var QemuImgFormat = map[string]string{
	"qcow2": "qcow2",
	"vhd":   "vpc",
	"vhdx":  "vhdx",
}

// --- HTTP status codes ---
const HTTPStatusNoContent = 204
const HTTPStatusSuccess = 200

// --- HTTP timeouts ---
const HTTPTimeoutKernelDownloadS = 600
const HTTPTimeoutKernelConfigS = 60
const HTTPTimeoutSha256FetchS = 30
const HTTPTimeoutSha256SidecarS = 15

// --- Default versions ---
const DefaultFirecrackerCIVersion = "v1.15"
const MinKernelMajor = 5
const MinKernelMinor = 10

// --- Default VM config ---
const DefaultVCPUCount = 1
const DefaultMemoryMiB = 512
const DefaultSSHUser = "root"
const DefaultDNS = "1.1.1.1"
const DefaultGuestMACPrefix = "02:FC"

// --- Default network ---
const DefaultNetworkSubnet = "172.27.0.0/24"

// --- Image processing ---
const RuntimeBufferMB = 160
const ShrinkSafetyMargin = 1.01
const RatioMin = 1.0
const MinRootfsSizeMiB = 128
const RootfsHeadroomFactor = 1.25
const RootfsMinHeadroomBytes = 150 * 1024 * 1024
const Percent = 100

// --- Buffer / sector ---
const BufferSizeBytes = 1024

// --- Download retry backoff ---
const DownloadRetryBackoff = 2.0

// --- Cloud-init ---
const RequiredISOTool = "cloud-localds"
const NoCloudNetBindTimeoutS = 5.0

// --- Console ---
const ConsoleSocketTimeoutS = 2.0
const ConsoleKillTimeoutS = 5.0

// --- Kernel types ---
const MVMBackgroundServiceEnv = "MVM_BACKGROUND_SERVICE=1"

// MVMProvisionPrefix is the prefix for provisioner temp directories.
const MVMProvisionPrefix = CLIName + "-provision-"

const KernelTypeFirecracker = "firecracker"
const KernelTypeOfficial = "official"

// --- Shadow file ---
const ShadowDaysSinceEpoch = 19700
const ShadowMinDays = 0
const ShadowMaxDays = 99999
const ShadowWarnDays = 7

// --- Supported image extensions ---
var SupportedImageExtensions = []string{
	".ext4",
	".btrfs",
	".img",
	".raw",
	".ext4.zst",
	".btrfs.zst",
}

// --- Image import format map ---
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

// --- Binary size ---
const MinBinarySizeBytes = 512

// --- Host system paths ---
const DefaultSysctlConfDir = "/etc/sysctl.d"
const DefaultSudoersDir = "/etc/sudoers"
const DefaultSysctlConfPath = "/etc/sysctl.d/mvmctl.conf"

// --- Libguestfs ---
const DefaultLibguestfsSeedDir = "/var/lib/cloud/seed/nocloud"

// --- Firecracker GitHub ---
const FirecrackerGithubReleasesAPIURL = "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
const FirecrackerGithubDownloadURL = "https://github.com/firecracker-microvm/firecracker/releases/download"
const FirecrackerGitRepoURL = "https://github.com/firecracker-microvm/firecracker.git"

// --- Privileged system binaries ---
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
// insertion order. Go maps have random iteration, so an ordered slice is
// needed for deterministic sudoers content generation.
var PrivilegedBinariesOrdered = [...]string{
	"/usr/sbin/ip",
	"/usr/sbin/iptables",
	"/usr/sbin/iptables-restore",
	"/usr/sbin/iptables-save",
	"/usr/sbin/nft",
	"/usr/sbin/sysctl",
	"/usr/sbin/modprobe",
}

// --- Init binaries ---
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

// --- Infra binaries ---
var InfraBinaries = []string{
	"qemu-img",
	"mkfs.ext4",
	"blkid",
	"sfdisk",
	"dumpe2fs",
}

// --- Required binaries ---
var RequiredBinaries = []string{
	"ip", "modprobe", "lsmod", "groupadd", "usermod",
	"groupdel", "visudo", "ssh-keygen", "tar",
	"qemu-img", "mkfs.ext4", "blkid", "sfdisk", "dumpe2fs",
	"iptables", "nft",
}

// CloudInitMode type moved to mvmctl/internal/infra/model (model.CloudInitMode).
// ProvisionerType type moved to mvmctl/internal/infra/model (model.ProvisionerType).

// --- Debug ---
var debugMode = false

func SetDebugMode(v bool) { debugMode = v }
func IsDebugMode() bool   { return debugMode }

// --- Compiled mode ---

// --- Environment variable access ---

func EnvKey(suffix string) string {
	return fmt.Sprintf("%s_%s", strings.ToUpper(CLIName), suffix)
}

// EnvGet returns the environment variable value and whether it was set.
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

// --- CacheUtils -- directory/path resolution ---

// ensureDirAndChown creates the directory with 0700 permissions and chowns
// to the real invoking user when running under sudo.
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

var (
	cacheDirOnce sync.Once
	cacheDirVal  string
	cacheDirErr  error
)

func GetCacheDir() (string, error) {
	cacheDirOnce.Do(func() {
		override, ok := EnvGet("CACHE_DIR")
		if ok && override != "" {
			var resolved string
			resolved, cacheDirErr = filepath.Abs(override)
			if cacheDirErr != nil {
				cacheDirErr = fmt.Errorf("invalid cache dir path: %w", cacheDirErr)
				return
			}
			// Ensure the directory exists with proper permissions.
			if err := ensureDirAndChown(resolved); err != nil {
				cacheDirErr = fmt.Errorf("create cache dir: %w", err)
				return
			}
			cacheDirVal = resolved
			return
		}
		path := filepath.Join(GetRealHome(), ".cache", ProjectName)
		if err := ensureDirAndChown(path); err != nil {
			cacheDirErr = fmt.Errorf("create default cache dir: %w", err)
			return
		}
		cacheDirVal = path
	})
	return cacheDirVal, cacheDirErr
}

func GetConfigDir() (string, error) {
	override, ok := EnvGet("CONFIG_DIR")
	if ok && override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("invalid config dir path: %w", err)
		}
		// Ensure the directory exists with proper permissions.
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

func GetVMDirByID(id string) string {
	return filepath.Join(GetVmsDir(), id)
}

func GetWorkflowsStateDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "workflows")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create workflows state directory", "path", path, "error", err)
	}
	return path
}

// GetWorkflowsStateDirByID returns the workflow state directory for a specific workflow ID.
func GetWorkflowsStateDirByID(wfID string) string {
	return filepath.Join(GetWorkflowsStateDir(), wfID)
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

func GetKeysDir() string {
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
	os.MkdirAll(filepath.Dir(logPath), DirPerm)
	return logPath
}

func GetTimingLogPath() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	return filepath.Join(cacheDir, "timing.log")
}

// --- Warm image directory ---
func GetWarmImagesDir() string {
	pool, ok := EnvGet("WARM_POOL")
	if ok && pool == "disk" {
		path := filepath.Join(GetImagesDir(), "ready")
		if err := ensureDirAndChown(path); err != nil {
			slog.Warn("failed to create warm image directory", "path", path, "error", err)
		}
		return path
	}
	// default: tmpfs
	path := filepath.Join(GetTempDir(), "ready")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create warm image directory", "path", path, "error", err)
	}
	return path
}

// GetNoCloudNetDir returns the base cache directory for nocloud-net batch servers.
func GetNoCloudNetDir() string {
	cacheDir, err := GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(GetRealHome(), ".cache", ProjectName)
	}
	path := filepath.Join(cacheDir, "nocloudnet")
	if err := ensureDirAndChown(path); err != nil {
		slog.Warn("failed to create nocloudnet directory", "path", path, "error", err)
	}
	return path
}

// GetNoCloudNetBatchDir returns the batch directory for a given nocloud batch ID.
func GetNoCloudNetBatchDir(batchID string) string {
	return filepath.Join(GetNoCloudNetDir(), batchID)
}

// GetNoCloudNetLogPath returns the log file path for a given nocloud batch ID.
func GetNoCloudNetLogPath(batchID string) string {
	return filepath.Join(GetNoCloudNetDir(), batchID+".log")
}

// --- CommonUtils -- domain-agnostic helpers ---

// ReservedNames that cannot be used as entity names.
// Prevents conflicts with CLI subcommands, resource types, and common identifiers.
// Add proactively whenever a new subcommand or resource type is introduced.
var ReservedNames = map[string]bool{
	// CRUD + lifecycle actions
	"create": true, "delete": true, "remove": true, "rm": true,
	"update": true, "set": true, "clear": true, "unset": true,
	"get": true, "list": true, "ls": true, "show": true,
	"describe": true, "inspect": true, "find": true, "search": true,
	"edit": true, "rename": true, "move": true, "copy": true,
	"clone": true, "import": true, "export": true, "backup": true,
	"restore": true, "save": true, "load": true, "sync": true,
	"merge": true, "split": true, "convert": true, "transform": true,

	// State transitions
	"start": true, "stop": true, "pause": true, "resume": true,
	"restart": true, "reboot": true, "shutdown": true, "halt": true,
	"poweroff": true, "poweron": true, "reset": true, "init": true,
	"clean": true, "prune": true, "purge": true, "wipe": true,

	// Lifecycle hooks
	"attach": true, "detach": true, "mount": true, "unmount": true,
	"connect": true, "disconnect": true, "link": true, "unlink": true,
	"lock": true, "unlock": true, "freeze": true, "unfreeze": true,
	"enable": true, "disable": true, "enabled": true, "disabled": true,
	"activate": true, "deactivate": true, "register": true, "unregister": true,

	// Network actions
	"ping": true, "traceroute": true, "dns": true, "dhcp": true,
	"nat": true, "forward": true, "route": true, "bridge": true,
	"firewall": true, "proxy": true, "tunnel": true, "vpn": true,

	// Observability
	"log": true, "logs": true, "tail": true, "follow": true,
	"watch": true, "monitor": true, "trace": true, "debug": true,
	"profile": true, "metrics": true, "stats": true, "health": true,
	"status": true, "info": true, "version": true, "audit": true,

	// Auth & security
	"login": true, "logout": true, "auth": true, "token": true,
	"secret": true, "certificate": true, "cert": true, "key": true, "role": true,
	"user": true, "group": true, "permission": true, "policy": true,

	// Provisioning
	"build": true, "deploy": true, "install": true, "uninstall": true,
	"upgrade": true, "rollback": true, "release": true, "publish": true,
	"compile": true, "validate": true, "verify": true, "check": true,
	"lint": true, "format": true, "test": true, "benchmark": true,

	// Snapshot & migration
	"snapshot": true, "migrate": true, "live-migrate": true,
	"takeover": true, "failover": true, "switchover": true,

	// Scheduling
	"schedule": true, "suspend": true, "reschedule": true,
	"job": true, "task": true, "cron": true, "queue": true,

	// Notifications
	"alert": true, "notification": true, "notify": true,
	"webhook": true, "hook": true, "callback": true, "event": true,

	// Resource types
	"vm": true, "network": true, "image": true, "kernel": true,
	"binary": true, "volume": true, "config": true,
	"cache": true, "host": true, "console": true,
	"ssh": true,

	// Potential future resource types
	"template": true, "tag": true, "label": true, "annotation": true,
	"quota": true, "limit": true,
	"setting": true, "preference": true,

	// Primitive and language type names
	"string": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true,
	"float": true, "float32": true, "float64": true,
	"bool": true, "byte": true, "rune": true,
	"any": true, "interface": true, "struct": true, "map": true,
	"slice": true, "array": true, "chan": true, "func": true,
	"pointer": true, "none": true, "nonetype": true,
	"bytes": true, "bytearray": true, "tuple": true,
	"object": true, "type": true, "class": true, "enum": true,

	// Common identifiers with special meaning
	"all": true, "default": true,
	"new": true, "old": true, "empty": true,
	"root": true, "self": true, "system": true, "parent": true,
	"force": true, "help": true,
	"true": true, "false": true, "yes": true, "no": true,
	"on": true, "off": true, "nil": true, "null": true,
	"0": true, "1": true,
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

// --- CommonUtils helpers ---

// Coerce coerces a value to a target kind.
//
// The case labels use stored type names ("str", "dict", "NoneType") from DB
// persistence. These must be kept for backward compatibility. Error messages
// to users use Go-native names ("string", "map", "nil").
//
// Conversion semantics:
// - bool is convertible to int: true→1, false→0
// - string→bool via truthy keywords: "true"/"1"/"yes"/"on"
// - string→int via strconv.Atoi
// - string→float via strconv.ParseFloat
// - string→map via json.Unmarshal
// - identity (already correct type): returns as-is
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
			// bool converts to int: true→1, false→0
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

// --- Timestamp format constants ---
// Only Go stdlib time constants are used (ARCHITECTURE: V17 — RFC3339 everywhere).
// Legacy microsecond/no-timezone formats are NOT supported — DB migration
// converts them to RFC3339 on read.

// Now returns the current timestamp in RFC3339 format.
func Now() string {
	return time.Now().Format(time.RFC3339)
}

func HumanReadableDatetime(isoTimestamp string) string {
	if isoTimestamp == "" {
		return "-"
	}
	// Only RFC3339 and nanosecond-precision RFC3339 are valid formats.
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

// NumCPU returns number of available CPUs.
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
