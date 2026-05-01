"""Project identity constants derived from pyproject.toml metadata."""

import functools
import ipaddress
from typing import Any, Final

_BOOTSTRAP_NAME: Final[str] = "mvmctl"


# ===========================================================================
# 1. Project identity & global metadata
# ===========================================================================

KERNEL_TYPE_FIRECRACKER: Final[str] = "firecracker"
KERNEL_TYPE_OFFICIAL: Final[str] = "official"

MVM_DB_FILENAME: Final[str] = "mvmdb.db"


# ===========================================================================
# 2. User-overridable defaults
# ===========================================================================

OVERRIDABLE_DEFAULTS: Final[dict[str, dict[str, Any]]] = {
    "defaults.vm": {
        "vcpu_count": 1,
        "mem_size_mib": 512,
        "ssh_user": "root",
        "user_password": "password",
        "network_interface": "eth0",
        "boot_args": (
            "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait"
        ),
        "disk_size": "2G",
        "enable_api_socket": True,
        "enable_pci": False,
        "enable_logging": True,
        "enable_metrics": False,
        "enable_console": True,
        "lsm_flags": "landlock,lockdown,yama,integrity,selinux,bpf",
        "log_lines": 50,
        "log_follow": False,
    },
    "defaults.network": {
        "name": "net",
        "subnet": "172.27.0.0/24",
        "ipv4_gateway": "172.27.0.1",
    },
    "defaults.image": {
        "arch": "x86_64",
        "convert_to": "ext4",
        "import_format": "auto",
        "import_size_mib": 2048,
    },
    "defaults.kernel": {
        "version": "6.19.9",
        "arch": "x86_64",
        "build_jobs": 1,
    },
    "defaults.http": {
        "download_chunk_size": 1048576,
        "download_max_retries": 3,
        "download_retry_delay": 1.0,
        "download_retry_backoff": 2.0,
    },
    "defaults.libguestfs": {
        "launch_timeout": 4,
        "fallback_root_device": "/dev/sda1",
        "seed_dir": "/var/lib/cloud/seed/nocloud",
    },
    "defaults.detectors": {
        "weights": {
            "type_code": 1.0,
            "label": 0.8,
            "size": 0.5,
            "filesystem": 0.7,
        },
        "scores": {
            "ROOT_SCORE": 1.0,
            "EXCLUDE_SCORE": -1.0,
            "NEUTRAL_SCORE": 0.0,
            "MBR_LINUX_SCORE": 0.5,
            "LABEL_ROOT_SCORE": 1.0,
            "LABEL_EXCLUDE_SCORE": -0.5,
            "SIZE_LARGEST_SCORE": 0.5,
            "SIZE_ROOT_SCORE": 0.3,
            "SIZE_TOO_SMALL_SCORE": -0.5,
        },
    },
    "defaults.debug": {
        "enabled": False,
        "verbose_errors": True,
        "show_tracebacks": False,
    },
}


def get_default(category: str, key: str) -> Any:
    """Return the hardcoded default value for a user-overridable setting."""
    return OVERRIDABLE_DEFAULTS[category][key]


# ===========================================================================
# 3. Non-overridable defaults & internal constants
# ===========================================================================

# --- VM internal constants ---
DEFAULT_SNAPSHOT_RESUME: Final[bool] = True
DEFAULT_GUEST_MAC_PREFIX: Final[str] = "02:FC"

# --- VM resource limits ---
CONST_VM_MEM_MIN_MIB: Final[int] = 128
CONST_VM_MEM_MAX_MIB: Final[int] = 65536
CONST_VM_VCPU_MIN: Final[int] = 1
CONST_VM_VCPU_MAX: Final[int] = 32
CONST_SIGNAL_EXIT_CODE_BASE: Final[int] = 128
MAX_VMS: Final[int] = 1000

# --- Firecracker driver settings ---
DEFAULT_FC_LOG_LEVEL: Final[str] = "Debug"

# --- Firecracker file names ---
DEFAULT_FC_LOG_FILENAME: Final[str] = "firecracker.log"
DEFAULT_FC_SERIAL_OUTPUT_FILENAME: Final[str] = "firecracker.console.log"
DEFAULT_FC_METRICS_FILENAME: Final[str] = "firecracker.metrics"
DEFAULT_FC_API_SOCKET_FILENAME: Final[str] = "firecracker.api.socket"
DEFAULT_FC_PID_FILENAME: Final[str] = "firecracker.pid"
DEFAULT_FC_CONFIG_FILENAME: Final[str] = "firecracker.json"
DEFAULT_CONSOLE_SOCKET_FILENAME: Final[str] = "console.sock"
DEFAULT_CONSOLE_PID_FILENAME: Final[str] = "console.pid"

# --- Cloud-init ---
DEFAULT_CLOUD_INIT_ISO_NAME: Final[str] = "cloud-init.iso"
REQUIRED_ISO_TOOL: Final[str] = "cloud-localds"

# --- Console ---
CONST_CONSOLE_SOCKET_TIMEOUT_S: Final[float] = 2.0
CONST_CONSOLE_KILL_TIMEOUT_S: Final[float] = 5.0

# --- Cloud-init / nocloud-net timeouts ---
CONST_NO_CLOUD_NET_PORT_RANGE: Final[tuple[int, int]] = (8000, 9000)
CONST_NO_CLOUD_NET_BIND_TIMEOUT_S: Final[float] = 5.0
CONST_NO_CLOUD_NET_MAX_PORT_RETRIES: Final[int] = 100

# --- VM lifecycle timings ---
LOG_FOLLOW_POLL_INTERVAL_S: Final[float] = 0.3


# ===========================================================================
# 4. Image & rootfs processing
# ===========================================================================

SUPPORTED_IMAGE_EXTENSIONS: Final[list[str]] = [
    ".ext4",
    ".btrfs",
    ".img",
    ".raw",
    ".ext4.zst",
    ".btrfs.zst",
]

IMAGE_IMPORT_FORMAT_MAP: Final[dict[str, str]] = {
    ".qcow2": "qcow2",
    ".raw": "raw",
    ".img": "raw",
    ".tar": "tar-rootfs",
    ".tar.gz": "tar-rootfs",
    ".tar.xz": "tar-rootfs",
    ".tgz": "tar-rootfs",
}

CONST_RUNTIME_BUFFER_MB: Final[int] = 160
CONST_SHRINK_SAFETY_MARGIN: Final[float] = 1.01
CONST_RATIO_MIN: Final[float] = 1.0
CONST_MIN_ROOTFS_SIZE_MIB: Final[int] = 128
CONST_ROOTFS_HEADROOM_FACTOR: Final[float] = 1.25
CONST_PERCENT: Final[int] = 100

DETECTOR_WEIGHTS: Final[dict[str, float]] = {
    "type_code": 1.0,
    "label": 0.8,
    "size": 0.5,
    "filesystem": 0.7,
}
DETECTOR_SCORES: Final[dict[str, float]] = {
    "ROOT_SCORE": 1.0,
    "EXCLUDE_SCORE": -1.0,
    "NEUTRAL_SCORE": 0.0,
    "MBR_LINUX_SCORE": 0.5,
    "LABEL_ROOT_SCORE": 1.0,
    "LABEL_EXCLUDE_SCORE": -0.5,
    "SIZE_LARGEST_SCORE": 0.5,
    "SIZE_ROOT_SCORE": 0.3,
    "SIZE_TOO_SMALL_SCORE": -0.5,
}
MIN_ROOT_SIZE_MB: Final[int] = 500
SIZE_TOO_SMALL_MB: Final[int] = 100


# ===========================================================================
# 5. Network & iptables
# ===========================================================================

IPTABLES_RULES_V4: Final[str] = "/etc/iptables/rules.v4"
CONST_IPTABLES_MAX_COMMENT_LEN: Final[int] = 240
CONST_DEFAULT_NAMESERVER: Final[str] = "1.1.1.1"


# ===========================================================================
# 6. Binary requirements
# ===========================================================================

PRIVILEGED_BINARIES: Final[dict[str, str]] = {
    "/usr/sbin/ip": "iproute2",
    "/usr/sbin/iptables": "iptables",
    "/usr/sbin/iptables-save": "iptables",
    "/usr/sbin/sysctl": "procps",
    "/usr/sbin/modprobe": "kmod",
}

REQUIRED_BINARIES: Final[list[str]] = [
    "ip",
    "iptables",
    "qemu-img",
    "ssh-keygen",
    "tar",
    "mkfs.ext4",
    "blkid",
    "sfdisk",
    "dumpe2fs",
    "modprobe",
    "lsmod",
    "groupadd",
    "usermod",
    "groupdel",
    "visudo",
]
ISO_BINARIES: Final[list[str]] = ["cloud-localds"]

CONST_MIN_BINARY_SIZE_BYTES: Final[int] = 512
DEFAULT_REMOTE_VERSION_LIMIT: Final[int] = 5


# ===========================================================================
# 7. Host system paths
# ===========================================================================

DEFAULT_SYSCTL_CONF_DIR: Final[str] = "/etc/sysctl.d"
DEFAULT_SUDOERS_DIR: Final[str] = "/etc/sudoers"
DEFAULT_SYSCTL_CONF_PATH: Final[str] = "/etc/sysctl.d/mvmctl.conf"


# ===========================================================================
# 8. Libguestfs
# ===========================================================================

DEFAULT_LIBGUESTFS_SEED_DIR: Final[str] = "/var/lib/cloud/seed/nocloud"
CONST_GUESTFS_OS_RELEASE_PATH: Final[str] = "/etc/os-release"


# ===========================================================================
# 9. File permissions (octal)
# ===========================================================================

CONST_FILE_PERMS_PRIVATE_KEY: Final[int] = 0o600
CONST_FILE_PERMS_PUBLIC_KEY: Final[int] = 0o644
CONST_DIR_PERMS_CACHE: Final[int] = 0o700
CONST_FILE_PERMS_SHADOW: Final[int] = 0o640
CONST_FILE_PERMS_SUDOERS: Final[int] = 0o440
CONST_FILE_PERMS_CONFIG: Final[int] = 0o600
CONST_FILE_PERMS_EXECUTABLE: Final[int] = 0o755

CONST_DEFAULT_USER_UID: Final[int] = 1000
CONST_DEFAULT_USER_GID: Final[int] = 1000
CONST_ROOT_UID: Final[int] = 0
CONST_ROOT_GID: Final[int] = 0

CONST_SHADOW_DAYS_SINCE_EPOCH: Final[int] = 19700
CONST_SHADOW_MIN_DAYS: Final[int] = 0
CONST_SHADOW_MAX_DAYS: Final[int] = 99999
CONST_SHADOW_WARN_DAYS: Final[int] = 7


# ===========================================================================
# 10. Buffer & byte constants
# ===========================================================================

CONST_BUFFER_SIZE_BYTES: Final[int] = 1024
CONST_SECTOR_SIZE_BYTES: Final[int] = 512
CONST_MEBIBYTE_BYTES: Final[int] = 1024 * 1024
CONST_MEGABYTE_BYTES: Final[int] = 1_000_000
CONST_DOWNLOAD_CHUNK_SIZE: Final[int] = 1048576


# ===========================================================================
# 11. HTTP / download
# ===========================================================================

HTTP_TIMEOUT_KERNEL_DOWNLOAD_S: Final[int] = 600
HTTP_TIMEOUT_KERNEL_CONFIG_S: Final[int] = 60
HTTP_TIMEOUT_SHA256_FETCH_S: Final[int] = 30
HTTP_TIMEOUT_SHA256_SIDECAR_S: Final[int] = 15
CONST_HTTP_TIMEOUT_SECONDS: Final[int] = 300

CONST_DOWNLOAD_MAX_RETRIES: Final[int] = 3
CONST_DOWNLOAD_RETRY_DELAY: Final[float] = 1.0
CONST_DOWNLOAD_RETRY_BACKOFF: Final[float] = 2.0
CONST_SOCKET_TIMEOUT_SECONDS: Final[float] = 5.0
CONST_POLL_STEP_SECONDS: Final[float] = 0.1

CONST_HTTP_STATUS_NO_CONTENT: Final[int] = 204
CONST_HTTP_STATUS_SUCCESS: Final[int] = 200

FIRECRACKER_GITHUB_RELEASES_API_URL: Final[str] = (
    "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
)
FIRECRACKER_GITHUB_DOWNLOAD_URL: Final[str] = (
    "https://github.com/firecracker-microvm/firecracker/releases/download"
)


# ===========================================================================
# 12. Time constants
# ===========================================================================

CONST_TIMESTAMP_INITIAL: Final[float] = 0.0


# ===========================================================================
# 13. Debug flags
# ===========================================================================

DEBUG_MODE: Final[bool] = False


# ===========================================================================
# 14. Archive — helper functions (called lazily or not at all)
# ===========================================================================

_LAZY_CONSTANTS: dict[str, Any] = {}


@functools.lru_cache(maxsize=1)
def _resolve_project_name() -> str:
    """Resolve the project name from package metadata, falling back to bootstrap name."""
    import importlib.metadata as _meta

    try:
        return _meta.metadata(_BOOTSTRAP_NAME)["Name"]
    except _meta.PackageNotFoundError:
        return _BOOTSTRAP_NAME


@functools.lru_cache(maxsize=1)
def _resolve_cli_name() -> str:
    """Resolve CLI name from entry points, falling back to 'mvm'."""
    import importlib.metadata as _meta

    try:
        eps = _meta.entry_points(group="console_scripts")
        for ep in eps:
            if ep.value == "mvmctl.main:app" or ep.value.endswith("main:app"):
                return ep.name
    except (OSError, ValueError):
        pass
    return "mvmctl"


def _resolve_version() -> str:
    import importlib.metadata as _meta

    try:
        return _meta.version(_BOOTSTRAP_NAME)
    except _meta.PackageNotFoundError:
        return "0.0.0"


def _format_path(path: tuple[str, ...]) -> str:
    return ".".join(path)


def _default_bridge_name(network_name: str) -> str:
    """Generate bridge name using the same logic as network manager."""
    return f"{__getattr__('CLI_NAME')}-{network_name[:10]}"


def _ipv4_gateway_subnet(ipv4_gateway: str, subnet: str) -> str:
    """Build ipv4_gateway SUBNET from ipv4_gateway IP + network prefix length."""
    try:
        prefix = ipaddress.ip_network(subnet, strict=False).prefixlen
    except ValueError as exc:
        raise RuntimeError(
            f"Invalid network.defaults.subnet value: {subnet}"
        ) from exc
    return f"{ipv4_gateway}/{prefix}"


def env_var(suffix: str) -> str:
    """
    Return the environment variable name for the given suffix.

    Args:
        suffix: The variable suffix to append after the CLI name prefix.

    Returns:
        Full environment variable name in uppercase.

    """
    return f"{__getattr__('CLI_NAME').upper()}_{suffix}"


def cache_dir_name() -> str:
    """Return the project cache directory name derived from the project name."""
    return str(__getattr__("PROJECT_NAME"))


def device_prefix() -> str:
    """Return the network device name prefix derived from the CLI name."""
    return str(__getattr__("CLI_NAME"))


def bridge_name() -> str:
    return f"{device_prefix()}-br0"


def config_filename() -> str:
    """Return the config file name for the CLI."""
    return f"{__getattr__('CLI_NAME')}.yaml"


def __getattr__(name: str) -> Any:
    if name in _LAZY_CONSTANTS:
        return _LAZY_CONSTANTS[name]

    if name == "PROJECT_NAME":
        val = _resolve_project_name()
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "PROJECT_NAME_UPPER":
        val = __getattr__("PROJECT_NAME").replace("-", "_").upper()
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "CLI_NAME":
        val = _resolve_cli_name()
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "HTTP_USER_AGENT":
        val = f"{__getattr__('CLI_NAME')}/{_resolve_version()}"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "BRIDGE_NAME":
        val = f"{__getattr__('CLI_NAME')}-br0"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "TAP_PREFIX":
        val = f"{__getattr__('CLI_NAME')}-tap"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "MVM_FORWARD_CHAIN":
        val = f"{__getattr__('CLI_NAME').upper()}-FORWARD"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "MVM_POSTROUTING_CHAIN":
        val = f"{__getattr__('CLI_NAME').upper()}-POSTROUTING"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "MVM_NOCLOUD_NET_INPUT_CHAIN":
        val = f"{__getattr__('CLI_NAME').upper()}-NOCLOUDNET-INPUT"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "MVM_UNIX_GROUP":
        val = __getattr__("CLI_NAME")
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "SUDOERS_DROP_IN_PATH":
        val = "/etc/sudoers.d/{cli_name}".format(
            cli_name=__getattr__("CLI_NAME")
        )
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "DEFAULT_BRIDGE_NAME":
        val = f"{__getattr__('CLI_NAME')}-{DEFAULT_NETWORK_NAME[:10]}"
        _LAZY_CONSTANTS[name] = val
        return val

    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")


# ===========================================================================
# 15. Archive — unused / legacy constants (kept for reference)
# ===========================================================================

# These constants are not currently referenced in non-archive source code.
# Kept here rather than deleted to preserve audit trail.

# --- Removed from section 1 (project identity) ---
DEFAULT_FIRECRACKER_VERSION: Final[str] = "v1.15.0"
DEFAULT_FC_CI_VERSION: Final[str] = "1.15"
KERNEL_TYPE_UNKNOWN: Final[str] = "unknown"

# --- Removed from section 2 (consolidated into OVERRIDABLE_DEFAULTS) ---
DEFAULT_VM_VCPU_COUNT: Final[int] = 1
DEFAULT_VM_MEM_MIB: Final[int] = 512
DEFAULT_VM_SSH_USER: Final[str] = "root"
DEFAULT_VM_ENABLE_PCI: Final[bool] = False
DEFAULT_VM_ENABLE_LOGGING: Final[bool] = True
DEFAULT_VM_ENABLE_METRICS: Final[bool] = False
DEFAULT_VM_ENABLE_CONSOLE: Final[bool] = True
DEFAULT_VM_BOOT_ARGS: Final[str] = (
    "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait"
)
DEFAULT_VM_LSM_FLAGS: Final[str] = (
    "landlock,lockdown,yama,integrity,selinux,bpf"
)
DEFAULT_NETWORK_NAME: Final[str] = "net"
DEFAULT_NETWORK_SUBNET: Final[str] = "172.27.0.0/24"
DEFAULT_NETWORK_IPV4_GATEWAY: Final[str] = "172.27.0.1"
DEFAULT_IMAGE_ARCH: Final[str] = "x86_64"
DEFAULT_IMAGE_IMPORT_FORMAT: Final[str] = "auto"
DEFAULT_KERNEL_VERSION: Final[str] = "6.19.9"
DEFAULT_KERNEL_BUILD_JOBS: Final[int] = 1
DEFAULT_VM_LOG_TYPE: Final[str] = "os"
DEFAULT_VM_LOG_LINES: Final[int] = 50
DEFAULT_VM_LOG_FOLLOW: Final[bool] = False
DEFAULT_VM_USER_PASSWORD: Final[str] = "password"

# --- Removed from section 2 (user-overridable defaults) ---
DEFAULT_NETWORK_BRIDGE_IP: Final[str] = (
    f"{DEFAULT_NETWORK_IPV4_GATEWAY}/"
    f"{ipaddress.ip_network(DEFAULT_NETWORK_SUBNET, strict=False).prefixlen}"
)
DEFAULT_IMAGE_CONVERT_TO: Final[str] = "ext4"
DEFAULT_IMAGE_IMPORT_SIZE_MIB: Final[int] = 2048

# --- Removed from section 3 (non-overridable defaults) ---
DEFAULT_FIRECRACKER_CI_VERSION: Final[str] = "v1.15"
DEFAULT_KERNEL_ARCH: Final[str] = "x86_64"
DEFAULT_FIRECRACKER_BIN_NAME: Final[str] = "firecracker"
DEFAULT_VM_ENABLE_API_SOCKET: Final[bool] = True
DEFAULT_VM_NETWORK_INTERFACE: Final[str] = "eth0"
DEFAULT_VM_DISK_SIZE: Final[str] = "2G"
DEFAULT_VM_KERNEL_FILENAME: Final[str] = "vmlinux"
DEFAULT_VM_ROOTFS_FILENAME: Final[str] = "rootfs.ext4"
DEFAULT_VM_ROOTFS_BASENAME: Final[str] = "rootfs"
DEFAULT_FIRECRACKER_BINARY_PATH: Final[str] = "/usr/local/bin/firecracker"
CONST_IP_RANGE_SIZE: Final[int] = 256
CONST_VM_NAME_MAX_LENGTH: Final[int] = 255
DEFAULT_BOOT_CONSOLE: Final[str] = "console=ttyS0"
DEFAULT_BOOT_REBOOT: Final[str] = "reboot=k"
DEFAULT_BOOT_PANIC: Final[str] = "panic=1"
DEFAULT_BOOT_PCI_OFF: Final[str] = "pci=off"
DEFAULT_FC_DRIVE_CACHE_TYPE: Final[str] = "Unsafe"
DEFAULT_FC_DRIVE_IO_ENGINE: Final[str] = "Sync"
FIRECRACKER_SIGTERM_WAIT_S: Final[int] = 1
FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S: Final[float] = 0.1
FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S: Final[float] = 2
DEFAULT_FC_EXITCODE_FILENAME: Final[str] = "firecracker.exitcode"
DEFAULT_CLOUD_INIT_SEED_PATH: Final[str] = "/var/lib/cloud/seed/nocloud"
DEFAULT_CLOUD_INIT_FINAL_MESSAGE: Final[str] = "mvm cloud-init done"
DEFAULT_CLOUD_INIT_DIRNAME: Final[str] = "cloud-init"
DEFAULT_CLOUD_INIT_DISABLE_SNAPD_CMD: Final[str] = (
    "systemctl disable --now snapd.socket 2>/dev/null || true"
)
DEFAULT_CLOUD_INIT_ISO_VOLUME_LABEL: Final[str] = "cidata"
CONST_CONSOLE_BUFFER_SIZE: Final[int] = 4096
CONST_CONSOLE_RECONNECT_DELAY_S: Final[float] = 0.5
CONST_CLOUD_INIT_TIMEOUT_S: Final[int] = 300
CONST_CLOUD_INIT_POLL_INTERVAL_S: Final[float] = 2.0
CONST_NO_CLOUD_NET_SHUTDOWN_TIMEOUT_S: Final[float] = 5.0
CONST_VM_START_WAIT_S: Final[float] = 0.5
DEFAULT_GUEST_NETWORK_IFACE: Final[str] = "eth0"
DEFAULT_GUEST_NETWORK_BOOT_MODE: Final[str] = "off"

# --- Removed from section 4 (image processing) ---
COMPRESSION_EXTENSION_MAP: Final[dict[str, str]] = {
    ".ext4": ".ext4.zst",
    ".btrfs": ".btrfs.zst",
    ".img": ".img.zst",
    ".raw": ".raw.zst",
}

# --- Removed from section 5 (network & iptables) ---
IPTABLES_CHAINS: Final[list[tuple[str, str, str]]] = [
    ("MVM-FORWARD", "filter", "FORWARD"),
    ("MVM-POSTROUTING", "nat", "POSTROUTING"),
    ("MVM-NOCLOUDNET-INPUT", "filter", "INPUT"),
]

# --- Removed from section 7 (host system paths) ---
DEFAULT_USR_SBIN_IP: Final[str] = "/usr/sbin/ip"
DEFAULT_USR_SBIN_IPTABLES: Final[str] = "/usr/sbin/iptables"
DEFAULT_USR_SBIN_IPTABLES_RESTORE: Final[str] = "/usr/sbin/iptables-restore"
DEFAULT_USR_SBIN_IPTABLES_SAVE: Final[str] = "/usr/sbin/iptables-save"
DEFAULT_USR_SBIN_SYSCTL: Final[str] = "/usr/sbin/sysctl"

# --- Removed from section 8 (libguestfs) ---
DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT: Final[int] = 4
DEFAULT_LIBGUESTFS_ROOT_DEVICE: Final[str] = "/dev/sda1"
DEFAULT_LIBGUESTFS_ROOT_INDICATORS: Final[tuple[str, ...]] = (
    "/etc/os-release",
    "/etc/fstab",
)

# --- Removed from section 9 (file permissions) ---
CONST_FILE_PERMS_METADATA: Final[int] = 0o600
CONST_FILE_PERMS_STATE_FILE: Final[int] = 0o640
CONST_FILE_PERMS_NETWORK_CONFIG: Final[int] = 0o600
CONST_FILE_PERMS_DHCP_LEASES: Final[int] = 0o600
CONST_FILE_PERMS_VM_STATE: Final[int] = 0o600

# --- Removed from section 10 (buffer & byte constants) ---
CONST_BYTE_MAX: Final[int] = 255
CONST_KIBIBYTE_BYTES: Final[int] = 1024
CONST_GIBIBYTE_BYTES: Final[int] = 1024 * 1024 * 1024
CONST_KILOBYTE_BYTES: Final[int] = 1_000
CONST_GIGABYTE_BYTES: Final[int] = 1_000_000_000

# --- Removed from section 11 (HTTP / download) ---
HTTP_TIMEOUT_FIRECRACKER_DOWNLOAD_S: Final[int] = 300
HTTP_TIMEOUT_FC_KERNEL_DOWNLOAD_S: Final[int] = 300
FIRECRACKER_GITHUB_RAW_URL: Final[str] = (
    "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main"
)
DEFAULT_MAX_PARALLEL_DOWNLOADS: Final[int] = 4
CONST_BINARY_FETCH_TIMEOUT: Final[int] = 300
CONST_HTTP_STATUS_CREATED: Final[int] = 201
CONST_HTTP_RANGE_START: Final[int] = 200

# --- Removed from section 12 (time constants) ---
CONST_SECONDS_PER_HOUR: Final[int] = 3600
CONST_SECONDS_PER_DAY: Final[int] = 86400
CONST_SECONDS_PER_WEEK: Final[int] = 604800
CONST_SECONDS_PER_MONTH: Final[int] = 2592000
CONST_SECONDS_PER_YEAR: Final[int] = 31536000

# --- Removed from section 13 (debug flags) ---
DEBUG_VERBOSE_ERRORS: Final[bool] = True
DEBUG_SHOW_TRACEBACKS: Final[bool] = False
