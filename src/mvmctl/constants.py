"""Project identity constants derived from pyproject.toml metadata."""

import functools
import importlib.metadata
import importlib.resources
import ipaddress
from pathlib import Path
from typing import Any, Final

import yaml

_BOOTSTRAP_NAME: Final[str] = "mvmctl"


@functools.lru_cache(maxsize=1)
def _resolve_project_name() -> str:
    """Resolve the project name from package metadata, falling back to bootstrap name."""
    try:
        return importlib.metadata.metadata(_BOOTSTRAP_NAME)["Name"]
    except importlib.metadata.PackageNotFoundError:
        return _BOOTSTRAP_NAME


@functools.lru_cache(maxsize=1)
def _resolve_cli_name() -> str:
    """Resolve CLI name from entry points, falling back to 'mvm'."""
    try:
        eps = importlib.metadata.entry_points(group="console_scripts")
        for ep in eps:
            if ep.value == "mvmctl.main:app" or ep.value.endswith("main:app"):
                return ep.name
    except (IOError, ValueError):
        pass
    return "mvm"


def _format_path(path: tuple[str, ...]) -> str:
    return ".".join(path)


@functools.lru_cache(maxsize=1)
def _load_defaults_yaml() -> dict[str, Any]:
    """Load packaged defaults.yaml once for constant bootstrapping."""
    try:
        # Using importlib.resources is more robust across different install/freeze scenarios
        resource_path = importlib.resources.files("mvmctl") / "assets" / "defaults.yaml"
        with resource_path.open("r", encoding="utf-8") as f:
            loaded = yaml.safe_load(f) or {}
    except (OSError, yaml.YAMLError) as exc:
        # Fallback to Path(__file__) if importlib.resources fails (e.g., during development or very old Python)
        try:
            defaults_path = Path(__file__).parent / "assets" / "defaults.yaml"
            with defaults_path.open("r", encoding="utf-8") as f:
                loaded = yaml.safe_load(f) or {}
        except Exception:
            raise RuntimeError(f"Failed to load required defaults file: {exc}") from exc

    if not isinstance(loaded, dict):
        raise RuntimeError("defaults.yaml root must be a mapping")
    return loaded


def _get_required(path: tuple[str, ...]) -> Any:
    current: Any = _load_defaults_yaml()
    for key in path:
        if not isinstance(current, dict) or key not in current:
            raise RuntimeError(f"Missing required defaults key: {_format_path(path)}")
        current = current[key]
    return current


def _require_str(path: tuple[str, ...]) -> str:
    value = _get_required(path)
    if isinstance(value, str):
        return value
    raise RuntimeError(f"defaults key must be string: {_format_path(path)}")


def _require_int(path: tuple[str, ...]) -> int:
    value = _get_required(path)
    if isinstance(value, int):
        return value
    raise RuntimeError(f"defaults key must be int: {_format_path(path)}")


def _require_bool(path: tuple[str, ...]) -> bool:
    value = _get_required(path)
    if isinstance(value, bool):
        return value
    raise RuntimeError(f"defaults key must be bool: {_format_path(path)}")


def _require_str_list(path: tuple[str, ...]) -> list[str]:
    value = _get_required(path)
    if isinstance(value, list) and all(isinstance(item, str) for item in value):
        return list(value)
    raise RuntimeError(f"defaults key must be list[str]: {_format_path(path)}")


def _require_str_dict(path: tuple[str, ...]) -> dict[str, str]:
    value = _get_required(path)
    if isinstance(value, dict) and all(
        isinstance(k, str) and isinstance(v, str) for k, v in value.items()
    ):
        return dict(value)
    raise RuntimeError(f"defaults key must be dict[str, str]: {_format_path(path)}")


def _require_str_float_dict(path: tuple[str, ...]) -> dict[str, float]:
    value = _get_required(path)
    if isinstance(value, dict) and all(
        isinstance(k, str) and isinstance(v, (int, float)) for k, v in value.items()
    ):
        return {k: float(v) for k, v in value.items()}
    raise RuntimeError(f"defaults key must be dict[str, float]: {_format_path(path)}")


def _default_bridge_name(network_name: str) -> str:
    """Generate bridge name using the same logic as network manager."""
    return f"{CLI_NAME}-{network_name[:10]}"


def _gateway_cidr(gateway: str, cidr: str) -> str:
    """Build gateway CIDR from gateway IP + network prefix length."""
    try:
        prefix = ipaddress.ip_network(cidr, strict=False).prefixlen
    except ValueError as exc:
        raise RuntimeError(f"Invalid network.defaults.cidr value: {cidr}") from exc
    return f"{gateway}/{prefix}"


# ---------------------------------------------------------------------------
# Critical kernel configuration (keep at top for visibility)
# ---------------------------------------------------------------------------

# Default kernel version for official upstream builds.
DEFAULT_KERNEL_VERSION: Final[str] = _require_str(("kernel", "defaults", "version"))

# Default architecture for Firecracker CI kernel downloads.
DEFAULT_FC_KERNEL_ARCH: Final[str] = _require_str(("kernel", "defaults", "arch"))

# Base URL for the Firecracker CI S3 kernel bucket.
FIRECRACKER_CI_KERNEL_S3_BASE: Final[str] = _require_str(
    ("urls", "firecracker_ci_kernel", "s3_base")
)

# S3 listing URL template for kernels; fill in {ci_version} and {arch}.
FIRECRACKER_CI_KERNEL_LIST_URL: Final[str] = _require_str(
    ("urls", "firecracker_ci_kernel", "list_url_template")
)

# S3 listing URL template for Firecracker CI Ubuntu images; fill in {ci_version} and {arch}.
FIRECRACKER_CI_IMAGE_LIST_URL: Final[str] = _require_str(
    ("urls", "firecracker_ci_image", "list_url_template")
)

# Firecracker microvm kernel config URL template. The {major_minor} placeholder
# is substituted at runtime (for example, "6.1").
FIRECRACKER_KERNEL_CONFIG_URL: Final[str] = _require_str(
    ("urls", "firecracker_kernel", "config_url_template")
)

# External kernel tarball/checksum URL templates.
KERNEL_TARBALL_URL_TEMPLATE: Final[str] = _require_str(("urls", "kernel", "tarball_template"))
KERNEL_SHA256_URL_TEMPLATE: Final[str] = _require_str(("urls", "kernel", "sha256_template"))

PROJECT_NAME: Final[str] = _resolve_project_name()

PROJECT_NAME_UPPER: Final[str] = PROJECT_NAME.replace("-", "_").upper()

CLI_NAME: Final[str] = _resolve_cli_name()


def env_var(suffix: str) -> str:
    """Return the environment variable name for the given suffix.

    Args:
        suffix: The variable suffix to append after the CLI name prefix.

    Returns:
        Full environment variable name in uppercase.
    """
    return f"{CLI_NAME.upper()}_{suffix}"


def cache_dir_name() -> str:
    """Return the project cache directory name derived from the project name."""
    return PROJECT_NAME


def device_prefix() -> str:
    """Return the network device name prefix derived from the CLI name."""
    return CLI_NAME


def config_filename() -> str:
    """Return the config file name for the CLI."""
    return f"{CLI_NAME}.yaml"


BRIDGE_NAME: Final[str] = f"{device_prefix()}-br0"

TAP_PREFIX: Final[str] = f"{CLI_NAME}-tap"

# iptables chain names for MVM rules
MVM_FORWARD_CHAIN: Final[str] = f"{CLI_NAME.upper()}-FORWARD"
MVM_POSTROUTING_CHAIN: Final[str] = f"{CLI_NAME.upper()}-POSTROUTING"

PROJECT_GROUP: Final[str] = CLI_NAME
SUDOERS_DROP_IN_PATH: Final[str] = _require_str(
    ("host", "system_files", "sudoers_drop_in_template")
).format(cli_name=CLI_NAME)
DEFAULT_NETWORK_NAME: Final[str] = _require_str(("network", "defaults", "name"))
FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S: Final[int] = 5
FIRECRACKER_SIGTERM_WAIT_S: Final[int] = 1
PRIVILEGED_BINARIES: Final[list[str]] = _require_str_list(("host", "privileged_binaries"))

IPTABLES_RULES_V4: Final[str] = _require_str(("host", "system_files", "iptables_rules_v4"))
REQUIRED_BINARIES: Final[list[str]] = _require_str_list(("host", "required_binaries"))
ISO_BINARIES: Final[list[str]] = _require_str_list(("host", "iso_binaries"))

# ---------------------------------------------------------------------------
# VM instance defaults (user-facing; also referenced by VMDefaultsConfig)
# ---------------------------------------------------------------------------


DEFAULT_VM_VCPU_COUNT: Final[int] = _require_int(("vm_defaults", "vcpu_count"))
DEFAULT_VM_MEM_MIB: Final[int] = _require_int(("vm_defaults", "mem_size_mib"))
DEFAULT_VM_SSH_USER: Final[str] = _require_str(("vm_defaults", "ssh_user"))
DEFAULT_VM_ROOT_FS_TYPE: Final[str] = _require_str(("vm_defaults", "root_fs_type"))
DEFAULT_FIRECRACKER_BIN_NAME: Final[str] = _require_str(("vm", "firecracker_bin_name"))

# VM feature flags
DEFAULT_VM_ENABLE_API_SOCKET: Final[bool] = _require_bool(("vm_defaults", "enable_api_socket"))
DEFAULT_VM_ENABLE_PCI: Final[bool] = _require_bool(("vm_defaults", "enable_pci"))
DEFAULT_VM_ENABLE_LOGGING: Final[bool] = _require_bool(("vm_defaults", "enable_logging"))
DEFAULT_VM_ENABLE_METRICS: Final[bool] = _require_bool(("vm_defaults", "enable_metrics"))

# VM network defaults
DEFAULT_VM_SUBNET_MASK: Final[str] = _require_str(("vm_defaults", "subnet_mask"))
DEFAULT_VM_NETWORK_INTERFACE: Final[str] = _require_str(("vm_defaults", "network_interface"))
DEFAULT_VM_BOOT_ARGS: Final[str] = _require_str(("vm_defaults", "boot_args"))
DEFAULT_VM_LSM_FLAGS: Final[str] = _require_str(("vm_defaults", "lsm_flags"))
DEFAULT_VM_DISK_SIZE: Final[str] = _require_str(("vm_defaults", "disk_size"))

# VM model structural defaults (internal path names)
DEFAULT_VM_KERNEL_FILENAME: Final[str] = _require_str(("vm", "files", "kernel_filename"))
DEFAULT_VM_ROOTFS_FILENAME: Final[str] = _require_str(("vm", "files", "rootfs_filename"))

# Firecracker binary path default
DEFAULT_FIRECRACKER_BINARY_PATH: Final[str] = _require_str(("firecracker", "binary"))

# Network bridge defaults
DEFAULT_NETWORK_CIDR: Final[str] = _require_str(("network", "defaults", "cidr"))
DEFAULT_NETWORK_GATEWAY: Final[str] = _require_str(("network", "defaults", "gateway"))
DEFAULT_BRIDGE_NAME: Final[str] = _default_bridge_name(DEFAULT_NETWORK_NAME)
DEFAULT_NETWORK_BRIDGE_IP: Final[str] = _gateway_cidr(
    gateway=DEFAULT_NETWORK_GATEWAY,
    cidr=DEFAULT_NETWORK_CIDR,
)

# Image defaults
DEFAULT_IMAGE_CONVERT_TO: Final[str] = _require_str(("image", "defaults", "convert_to"))
DEFAULT_IMAGE_IMPORT_FORMAT: Final[str] = _require_str(("image", "defaults", "import_format"))
DEFAULT_IMAGE_IMPORT_SIZE_MIB: Final[int] = _require_int(("image", "defaults", "import_size_mib"))
SUPPORTED_IMAGE_EXTENSIONS: Final[list[str]] = _require_str_list(
    ("image", "defaults", "supported_extensions")
)

IMAGE_IMPORT_FORMAT_MAP: Final[dict[str, str]] = _require_str_dict(
    ("image", "defaults", "import_format_map")
)

# VM log defaults
DEFAULT_VM_LOG_TYPE: Final[str] = _require_str(("vm", "logging", "type"))
DEFAULT_VM_LOG_LINES: Final[int] = _require_int(("vm", "logging", "lines"))
DEFAULT_VM_LOG_FOLLOW: Final[bool] = _require_bool(("vm", "logging", "follow"))

# Snapshot defaults
DEFAULT_SNAPSHOT_RESUME: Final[bool] = _require_bool(("vm", "snapshot", "resume"))

# Binary management defaults
DEFAULT_REMOTE_VERSION_LIMIT: Final[int] = _require_int(("image", "remote", "version_limit"))

# ---------------------------------------------------------------------------
# Fallback values — last-resort runtime values when config lookup fails
# ---------------------------------------------------------------------------

DEFAULT_FC_CI_VERSION: Final[str] = _require_str(("fallbacks", "fc_ci_version"))
DEFAULT_FIRECRACKER_BIN: Final[str] = _require_str(("fallbacks", "firecracker_bin"))
DEFAULT_KERNEL_BUILD_JOBS: Final[int] = _require_int(("fallbacks", "kernel_build_jobs"))
DEFAULT_MAX_PARALLEL_DOWNLOADS: Final[int] = _require_int(("fallbacks", "max_parallel_downloads"))

# ---------------------------------------------------------------------------
# Network defaults (fallback values)
# ---------------------------------------------------------------------------

DEFAULT_DNS_NAMESERVERS: Final[list[str]] = ["8.8.8.8", "1.1.1.1"]

# ---------------------------------------------------------------------------
# Firecracker file names
# ---------------------------------------------------------------------------

DEFAULT_FC_LOG_FILENAME: Final[str] = "firecracker.log"
DEFAULT_FC_CONSOLE_LOG_FILENAME: Final[str] = "firecracker.console.log"
DEFAULT_FC_METRICS_FILENAME: Final[str] = "firecracker.metrics"
DEFAULT_FC_API_SOCKET_FILENAME: Final[str] = "firecracker.api.socket"
DEFAULT_FC_PID_FILENAME: Final[str] = "firecracker.pid"
DEFAULT_FC_CONFIG_FILENAME: Final[str] = "firecracker.json"

# ---------------------------------------------------------------------------
# Kernel type strings
# ---------------------------------------------------------------------------

KERNEL_TYPE_FIRECRACKER: Final[str] = "firecracker"
KERNEL_TYPE_OFFICIAL: Final[str] = "official"
KERNEL_TYPE_UNKNOWN: Final[str] = "unknown"

# ---------------------------------------------------------------------------
# VM cloud-init defaults (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------
DEFAULT_CLOUD_INIT_SEED_PATH: Final[str] = _require_str(("vm", "cloud_init", "seed_path"))
DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_DS: Final[str] = _require_str(
    ("vm", "cloud_init", "kernel_cmdline_ds")
)
DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_NOCLOUD: Final[str] = "ds=nocloud"
DEFAULT_CLOUD_INIT_FINAL_MESSAGE: Final[str] = _require_str(("vm", "cloud_init", "final_message"))
DEFAULT_CLOUD_INIT_DISABLE_SNAPD_CMD: Final[str] = _require_str(
    ("vm", "cloud_init", "disable_snapd_cmd")
)
DEFAULT_CLOUD_INIT_DIRNAME: Final[str] = _require_str(("vm", "cloud_init", "dirname"))
DEFAULT_CLOUD_INIT_ISO_NAME: Final[str] = "cloud-init.iso"
DEFAULT_CLOUD_INIT_ISO_VOLUME_LABEL: Final[str] = "cidata"
DEFAULT_CLOUD_INIT_DRIVE_ID: Final[str] = "cloud-init"
REQUIRED_ISO_TOOL: Final[str] = "cloud-localds"

# ---------------------------------------------------------------------------
# VM boot arg defaults (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------
DEFAULT_BOOT_CONSOLE: Final[str] = _require_str(("vm", "boot", "console"))
DEFAULT_BOOT_REBOOT: Final[str] = _require_str(("vm", "boot", "reboot"))
DEFAULT_BOOT_PANIC: Final[str] = _require_str(("vm", "boot", "panic"))
DEFAULT_BOOT_PCI_OFF: Final[str] = _require_str(("vm", "boot", "pci_off"))

# ---------------------------------------------------------------------------
# VM guest network defaults (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------
DEFAULT_GUEST_MAC_DEFAULT: Final[str] = _require_str(("vm", "network_guest", "mac_default"))
DEFAULT_GUEST_MAC_PREFIX: Final[str] = _require_str(("vm", "network_guest", "mac_prefix"))
DEFAULT_GUEST_NETWORK_IFACE: Final[str] = _require_str(("vm", "network_guest", "iface"))

# ---------------------------------------------------------------------------
# Firecracker driver defaults (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------
DEFAULT_FC_LOG_LEVEL: Final[str] = _require_str(("vm", "firecracker", "log_level"))
DEFAULT_FC_DRIVE_CACHE_TYPE: Final[str] = _require_str(("vm", "firecracker", "drive_cache_type"))
DEFAULT_FC_DRIVE_IO_ENGINE: Final[str] = _require_str(("vm", "firecracker", "drive_io_engine"))

# VM rootfs basename (no extension — extension comes from image's filesystem type)
DEFAULT_VM_ROOTFS_BASENAME: Final[str] = _require_str(("vm", "files", "rootfs_basename"))

# ---------------------------------------------------------------------------
# Rootfs detector constants (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------

DETECTOR_WEIGHTS: Final[dict[str, float]] = _require_str_float_dict(("detectors", "weights"))
DETECTOR_SCORES: Final[dict[str, float]] = _require_str_float_dict(("detectors", "scores"))
MIN_ROOT_SIZE_MB: Final[int] = _require_int(("detectors", "thresholds", "MIN_ROOT_SIZE_MB"))
SIZE_TOO_SMALL_MB: Final[int] = _require_int(("detectors", "thresholds", "SIZE_TOO_SMALL_MB"))

# ---------------------------------------------------------------------------
# Host system paths (loaded from assets/defaults.yaml)
# ---------------------------------------------------------------------------
DEFAULT_SYSCTL_CONF_DIR: Final[str] = _require_str(("host", "system_dirs", "sysctl_conf_dir"))
DEFAULT_SUDOERS_DIR: Final[str] = _require_str(("host", "system_dirs", "sudoers_dir"))
DEFAULT_USR_SBIN_IP: Final[str] = _require_str(("host", "sbin_paths", "ip"))
DEFAULT_USR_SBIN_IPTABLES: Final[str] = _require_str(("host", "sbin_paths", "iptables"))
DEFAULT_USR_SBIN_IPTABLES_RESTORE: Final[str] = _require_str(
    ("host", "sbin_paths", "iptables_restore")
)
DEFAULT_USR_SBIN_IPTABLES_SAVE: Final[str] = _require_str(("host", "sbin_paths", "iptables_save"))
DEFAULT_USR_SBIN_SYSCTL: Final[str] = _require_str(("host", "sbin_paths", "sysctl"))

# ---------------------------------------------------------------------------
# Timeouts and poll intervals (in seconds)
# ---------------------------------------------------------------------------

FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S: Final[float] = 0.1
LOG_FOLLOW_POLL_INTERVAL_S: Final[float] = 0.3

# ---------------------------------------------------------------------------
# HTTP download timeouts (in seconds)
# ---------------------------------------------------------------------------

HTTP_TIMEOUT_KERNEL_DOWNLOAD_S: Final[int] = 600
HTTP_TIMEOUT_KERNEL_CONFIG_S: Final[int] = 60
HTTP_TIMEOUT_SHA256_FETCH_S: Final[int] = 30
HTTP_TIMEOUT_FIRECRACKER_DOWNLOAD_S: Final[int] = 300
HTTP_TIMEOUT_SHA256_SIDECAR_S: Final[int] = 15
HTTP_TIMEOUT_FC_KERNEL_DOWNLOAD_S: Final[int] = 300

# ---------------------------------------------------------------------------
# Firecracker defaults
# ---------------------------------------------------------------------------

# Default Firecracker version (full semantic version)
DEFAULT_FIRECRACKER_VERSION: Final[str] = _require_str(("firecracker", "versions", "full"))

# Default Firecracker CI version (major.minor for kernel downloads)
DEFAULT_FIRECRACKER_CI_VERSION: Final[str] = _require_str(("firecracker", "versions", "ci"))


def _resolve_version() -> str:
    try:
        return importlib.metadata.version(_BOOTSTRAP_NAME)
    except importlib.metadata.PackageNotFoundError:
        return "0.0.0"


HTTP_USER_AGENT: Final[str] = f"{CLI_NAME}/{_resolve_version()}"

# ---------------------------------------------------------------------------
# Hardcoded numeric constants extracted from core layer
# ---------------------------------------------------------------------------

# Buffer sizes (in bytes)
CONST_BUFFER_SIZE_BYTES: Final[int] = 1024
CONST_SSH_KEY_SIZE_BITS: Final[int] = 384
CONST_SSH_KEY_SIZE_BYTES: Final[int] = 384
CONST_HASH_BUFFER_SIZE: Final[int] = 384
CONST_SECTOR_SIZE_BYTES: Final[int] = 512
CONST_MEBIBYTE_BYTES: Final[int] = 1024 * 1024
CONST_KIBIBYTE_BYTES: Final[int] = 1024

# Permission modes (octal)
CONST_FILE_PERMS_PRIVATE_KEY: Final[int] = 0o600
CONST_FILE_PERMS_PUBLIC_KEY: Final[int] = 0o644
CONST_FILE_PERMS_METADATA: Final[int] = 0o600
CONST_DIR_PERMS_CACHE: Final[int] = 0o700
CONST_FILE_PERMS_STATE_FILE: Final[int] = 0o640
CONST_FILE_PERMS_SUDOERS: Final[int] = 0o440
CONST_FILE_PERMS_CONFIG: Final[int] = 0o600
CONST_FILE_PERMS_EXECUTABLE: Final[int] = 0o755
CONST_FILE_PERMS_PID_FILE: Final[int] = 0o600
CONST_FILE_PERMS_NETWORK_CONFIG: Final[int] = 0o600
CONST_FILE_PERMS_DHCP_LEASES: Final[int] = 0o600
CONST_FILE_PERMS_VM_STATE: Final[int] = 0o600

# File/directory sizes (in bytes)
CONST_MIN_IMAGE_SIZE_BYTES: Final[int] = 512
CONST_MIN_BINARY_SIZE_BYTES: Final[int] = 512
CONST_CONFIG_FILE_SIZE_BYTES: Final[int] = 448
CONST_HOST_STATE_SIZE_BYTES: Final[int] = 416
CONST_HOST_PRIV_SIZE_BYTES: Final[int] = 288

# HTTP status codes
CONST_HTTP_STATUS_NO_CONTENT: Final[int] = 204
CONST_HTTP_STATUS_SUCCESS: Final[int] = 200
CONST_HTTP_STATUS_CREATED: Final[int] = 201
CONST_HTTP_RANGE_START: Final[int] = 200

# Port and network constants
CONST_FIRECRACKER_API_PORT_START: Final[int] = 493
CONST_FIRECRACKER_API_PORT_MIN: Final[int] = 493
CONST_FIRECRACKER_API_PORT_MAX: Final[int] = 1024

# VM resource limits
CONST_VM_MEM_MIN_MIB: Final[int] = 128
CONST_VM_MEM_MAX_MIB: Final[int] = 65536
CONST_VM_VCPU_MIN: Final[int] = 1
CONST_VM_VCPU_MAX: Final[int] = 256
CONST_IP_RANGE_SIZE: Final[int] = 256

# Time constants (in seconds)
CONST_SECONDS_PER_HOUR: Final[int] = 3600
CONST_SECONDS_PER_DAY: Final[int] = 86400
CONST_SECONDS_PER_WEEK: Final[int] = 604800
CONST_SECONDS_PER_MONTH: Final[int] = 2592000
CONST_SECONDS_PER_YEAR: Final[int] = 31536000
CONST_HTTP_TIMEOUT_SECONDS: Final[int] = 300

# Retry and timeout constants
CONST_RETRY_ATTEMPTS: Final[int] = 3
CONST_RETRY_DELAY_SECONDS: Final[int] = 1
CONST_DOWNLOAD_CHUNK_SIZE: Final[int] = _require_int(("http", "download_chunk_size"))
CONST_BINARY_FETCH_TIMEOUT: Final[int] = 300
CONST_SOCKET_TIMEOUT_SECONDS: Final[float] = 5.0
CONST_POLL_STEP_SECONDS: Final[float] = 0.1
CONST_NO_CLOUD_NET_SHUTDOWN_TIMEOUT_S: Final[float] = 5.0
CONST_TIMESTAMP_INITIAL: Final[float] = 0.0
MAX_VMS: Final[int] = _require_int(("vm", "limits", "max_vms"))

# External URLs
FIRECRACKER_GITHUB_RELEASES_API_URL: Final[str] = _require_str(
    ("urls", "firecracker", "github_releases_api")
)
FIRECRACKER_GITHUB_DOWNLOAD_URL: Final[str] = _require_str(
    ("urls", "firecracker", "github_download_base")
)
FIRECRACKER_GITHUB_RAW_URL: Final[str] = _require_str(("urls", "firecracker", "github_raw_base"))
