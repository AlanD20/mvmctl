"""Project identity constants derived from pyproject.toml metadata."""

import functools
import importlib.metadata
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
    defaults_path = Path(__file__).parent / "assets" / "defaults.yaml"
    try:
        with defaults_path.open("r", encoding="utf-8") as f:
            loaded = yaml.safe_load(f) or {}
    except (OSError, yaml.YAMLError) as exc:
        raise RuntimeError(f"Failed to load required defaults file: {defaults_path}") from exc
    if not isinstance(loaded, dict):
        raise RuntimeError(f"defaults.yaml root must be a mapping: {defaults_path}")
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

# S3 listing URL template; fill in {ci_version} and {arch}.
FIRECRACKER_CI_KERNEL_LIST_URL: Final[str] = _require_str(
    ("urls", "firecracker_ci_kernel", "list_url_template")
)

# Firecracker microvm kernel config URL template. The {major_minor} placeholder
# is substituted at runtime (for example, "6.1").
FIRECRACKER_KERNEL_CONFIG_URL: Final[str] = _require_str(
    ("urls", "firecracker_kernel", "config_url_template")
)

# External kernel tarball/checksum URL templates.
KERNEL_TARBALL_URL_TEMPLATE: Final[str] = _require_str(("urls", "kernel", "tarball_template"))
KERNEL_SHA256_URL_TEMPLATE: Final[str] = _require_str(("urls", "kernel", "sha256_template"))

# Kernel options to enable (--enable) when building an official kernel.
# Applied BEFORE any user-supplied --kernel-config override so users can
# reverse individual items if they know what they are doing.
KERNEL_ENABLED_CONFIGS: Final[list[str]] = [
    # Filesystems
    "CONFIG_BTRFS_FS",
    "CONFIG_BTRFS_FS_POSIX_ACL",
    "CONFIG_EXT4_FS",
    "CONFIG_EXT4_FS_POSIX_ACL",
    "CONFIG_XFS_FS",
    "CONFIG_SQUASHFS",
    # VirtIO (all must be built-in for Firecracker)
    "CONFIG_VIRTIO",
    "CONFIG_VIRTIO_MENU",
    "CONFIG_VIRTIO_PCI",
    "CONFIG_VIRTIO_BLK",
    "CONFIG_VIRTIO_NET",
    "CONFIG_VIRTIO_CONSOLE",
    # Serial console
    "CONFIG_SERIAL_8250",
    "CONFIG_SERIAL_8250_CONSOLE",
    # Network
    "CONFIG_NET",
    "CONFIG_INET",
    "CONFIG_IPV6",
    # KVM guest optimisations
    "CONFIG_KVM_GUEST",
    "CONFIG_PARAVIRT",
    # Security / BPF / cgroups
    "CONFIG_SECURITY_LANDLOCK",
    "CONFIG_BPF_SYSCALL",
    "CONFIG_CGROUPS",
    "CONFIG_MEMCG",
    # PCI (required for some upstream kernels)
    "CONFIG_PCI",
]

# Integer-valued kernel options (--set-val). Each entry is (option, value).
KERNEL_SET_VAL_CONFIGS: Final[list[tuple[str, str]]] = [
    ("CONFIG_SERIAL_8250_NR_UARTS", "4"),
]

# Kernel options to disable (--disable) when building an official kernel.
KERNEL_DISABLED_CONFIGS: Final[list[str]] = [
    "CONFIG_BLK_DEV_ZONED",
    "CONFIG_VIRTIO_BLK_F_SECURE_ERASE",
    "CONFIG_VIRTIO_BLK_SCSI",
]

# Critical settings that MUST be =y for Firecracker to boot. If any is
# missing after configuration, the build is aborted with an error.
KERNEL_REQUIRED_SETTINGS: Final[list[str]] = [
    "CONFIG_BTRFS_FS=y",
    "CONFIG_VIRTIO_BLK=y",
    "CONFIG_VIRTIO_NET=y",
    "CONFIG_SERIAL_8250_CONSOLE=y",
    "CONFIG_KVM_GUEST=y",
]


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
DEFAULT_FIRECRACKER_BIN_NAME: Final[str] = _require_str(("vm", "firecracker_bin_name"))

# VM feature flags
DEFAULT_VM_ENABLE_API_SOCKET: Final[bool] = _require_bool(("vm_defaults", "enable_api_socket"))
DEFAULT_VM_ENABLE_PCI: Final[bool] = _require_bool(("vm_defaults", "enable_pci"))

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

FALLBACK_FC_CI_VERSION: Final[str] = _require_str(("fallbacks", "fc_ci_version"))
FALLBACK_FIRECRACKER_BIN: Final[str] = _require_str(("fallbacks", "firecracker_bin"))
FALLBACK_KERNEL_BUILD_JOBS: Final[int] = _require_int(("fallbacks", "kernel_build_jobs"))
FALLBACK_MAX_PARALLEL_DOWNLOADS: Final[int] = _require_int(("fallbacks", "max_parallel_downloads"))

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
MAX_VMS: Final[int] = _require_int(("vm", "limits", "max_vms"))

# External URLs
FIRECRACKER_GITHUB_RELEASES_API_URL: Final[str] = _require_str(
    ("urls", "firecracker", "github_releases_api")
)
FIRECRACKER_GITHUB_DOWNLOAD_URL: Final[str] = _require_str(
    ("urls", "firecracker", "github_download_base")
)
FIRECRACKER_GITHUB_RAW_URL: Final[str] = _require_str(("urls", "firecracker", "github_raw_base"))
