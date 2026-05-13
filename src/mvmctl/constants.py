"""Project identity constants derived from pyproject.toml metadata."""

from __future__ import annotations

import functools
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
    "settings.vm": {
        "max_vms": 1000,
        "log_lines": 50,
        "log_follow": False,
    },
    "defaults.vm": {
        "vcpu_count": 1,
        "mem_size_mib": 512,
        "ssh_user": "root",
        "user_password": "password",
        "dns_server": "1.1.1.1",
        "root_uid": 0,
        "root_gid": 0,
        "user_uid": 1000,
        "user_gid": 1000,
        "enable_pci": False,
        "enable_logging": True,
        "enable_metrics": False,
        "enable_console": True,
        "lsm_flags": "landlock,lockdown,yama,integrity,selinux,bpf",
        "boot_args": "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait quiet loglevel=3",
        "guest_mac_prefix": "02:FC",
    },
    "defaults.network": {
        "name": "net",
        "subnet": "172.27.0.0/24",
        "nat_enabled": True,
    },
    "defaults.image": {
        "arch": "x86_64",
        "import_format": "auto",
    },
    "defaults.kernel": {
        "arch": "x86_64",
        "version": "6.19.9",  # default for --type official; firecracker uses CI version
        "build_jobs": None,  # None → all available cores (os.cpu_count())
    },
    "defaults.firecracker": {
        "log_level": "Debug",
        "log_filename": "firecracker.log",
        "serial_output_filename": "firecracker.console.log",
        "metrics_filename": "firecracker.metrics",
        "api_socket_filename": "firecracker.api.socket",
        "pid_filename": "firecracker.pid",
        "config_filename": "firecracker.json",
        "console_socket_filename": "console.sock",
        "console_pid_filename": "console.pid",
    },
    "defaults.cloudinit": {
        "iso_name": "cloud-init.iso",
        "nocloud_port_range_start": 8000,
        "nocloud_port_range_end": 9000,
        "nocloud_max_port_retries": 100,
    },
    "defaults.binary": {
        "remote_version_limit": 5,
    },
    "settings": {
        "guestfs_enabled": False,
    },
}


def get_default(category: str, key: str) -> Any:
    """Return the hardcoded default value for a user-overridable setting."""
    return OVERRIDABLE_DEFAULTS[category][key]


def is_compiled_mode() -> bool:
    """Return True when running as a compiled binary (Nuitka/PyInstaller).

    In development mode (``uv run mvm``, ``python -m mvmctl``, ``pip install -e .``),
    compiled binary assets do not exist on disk. The code falls back to
    Python-based alternatives (e.g. running service binaries via
    ``sys.executable .../process.py``).

    In compiled mode (final build distributed to users), all service binaries
    are embedded in the single-file executable and extracted on first run.
    """
    import sys

    # PyInstaller sets sys.frozen
    if getattr(sys, "frozen", False):
        return True

    # Nuitka sets __compiled__ in builtins for compiled code
    import builtins

    if getattr(builtins, "__compiled__", False):
        return True

    # Detect Nuitka onefile: sys.executable points to python3 inside
    # /tmp/onefile_{PID}_{TIME}/ (verified via strace on v4.0.8)
    exe = getattr(sys, "executable", "") or ""
    if "/onefile_" in exe and exe.endswith("/python3"):
        return True

    return False


# ===========================================================================
# 3. VM constants
# ===========================================================================

# --- Limits ---
CONST_VM_MEM_MIN_MIB: Final[int] = 128
CONST_VM_MEM_MAX_MIB: Final[int] = 65536
CONST_VM_VCPU_MIN: Final[int] = 1
CONST_VM_VCPU_MAX: Final[int] = 32
CONST_SIGNAL_EXIT_CODE_BASE: Final[int] = 128

# --- Defaults ---
DEFAULT_FIRECRACKER_CI_VERSION: Final[str] = "v1.15"

# --- Lifecycle timings ---
LOG_FOLLOW_POLL_INTERVAL_S: Final[float] = 0.3


# ===========================================================================
# 4. Cloud-init & console
# ===========================================================================

# --- Cloud-init ---
REQUIRED_ISO_TOOL: Final[str] = "cloud-localds"
CONST_NO_CLOUD_NET_BIND_TIMEOUT_S: Final[float] = 5.0

# --- Console ---
CONST_CONSOLE_SOCKET_TIMEOUT_S: Final[float] = 2.0
CONST_CONSOLE_KILL_TIMEOUT_S: Final[float] = 5.0


# ===========================================================================
# 5. Image & rootfs processing
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
MIN_ROOT_SIZE_MB: Final[int] = 500
SIZE_TOO_SMALL_MB: Final[int] = 100

# --- Partition detection ---
DETECTOR_WEIGHTS: Final[dict[str, float]] = {
    "type_code": 1.0,
    "label": 0.8,
    "size": 0.5,
    "filesystem": 0.7,
}
DETECTOR_SCORES: Final[dict[str, float]] = {
    "root_score": 1.0,
    "exclude_score": -1.0,
    "neutral_score": 0.0,
    "mbr_linux_score": 0.5,
    "label_root_score": 1.0,
    "label_exclude_score": -0.5,
    "size_largest_score": 0.5,
    "size_root_score": 0.3,
    "size_too_small_score": -0.5,
}


# ===========================================================================
# 6. Network & iptables
# ===========================================================================

IPTABLES_RULES_V4: Final[str] = "/etc/iptables/rules.v4"
CONST_IPTABLES_MAX_COMMENT_LEN: Final[int] = 240


# ===========================================================================
# 7. Host system
# ===========================================================================

# --- Paths ---
DEFAULT_SYSCTL_CONF_DIR: Final[str] = "/etc/sysctl.d"
DEFAULT_SUDOERS_DIR: Final[str] = "/etc/sudoers"
DEFAULT_SYSCTL_CONF_PATH: Final[str] = "/etc/sysctl.d/mvmctl.conf"

# --- Privileged binaries ---
# Service binary names embedded in the compiled mvm binary.
# Extracted to CacheUtils.get_bin_dir() on `mvm init`.
SERVICE_BINARY_NAMES: Final[list[str]] = [
    "mvm-console-relay",
    "mvm-nocloud-server",
    "mvm-provision",
]

PRIVILEGED_BINARIES: Final[dict[str, str]] = {
    "/usr/sbin/ip": "iproute2",
    "/usr/sbin/iptables": "iptables",
    "/usr/sbin/iptables-save": "iptables",
    "/usr/sbin/sysctl": "procps",
    "/usr/sbin/modprobe": "kmod",
}

# mvmctl service binaries that need passwordless sudo access.
# Paths are resolved at sudoers generation time via CacheUtils.get_bin_dir().
PRIVILEGED_SERVICE_BINARIES: Final[list[str]] = [
    "mvm-provision",
]

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

# --- Libguestfs ---
DEFAULT_LIBGUESTFS_SEED_DIR: Final[str] = "/var/lib/cloud/seed/nocloud"
CONST_GUESTFS_OS_RELEASE_PATH: Final[str] = "/etc/os-release"


# ===========================================================================
# 8. File permissions & user IDs
# ===========================================================================

CONST_FILE_PERMS_PRIVATE_KEY: Final[int] = 0o600
CONST_FILE_PERMS_PUBLIC_KEY: Final[int] = 0o644
CONST_DIR_PERMS_CACHE: Final[int] = 0o700
CONST_FILE_PERMS_SHADOW: Final[int] = 0o640
CONST_FILE_PERMS_SUDOERS: Final[int] = 0o440
CONST_FILE_PERMS_CONFIG: Final[int] = 0o600
CONST_FILE_PERMS_DB: Final[int] = 0o640
CONST_FILE_PERMS_EXECUTABLE: Final[int] = 0o755

# --- /etc/shadow fields ---
CONST_SHADOW_DAYS_SINCE_EPOCH: Final[int] = 19700
CONST_SHADOW_MIN_DAYS: Final[int] = 0
CONST_SHADOW_MAX_DAYS: Final[int] = 99999
CONST_SHADOW_WARN_DAYS: Final[int] = 7


# ===========================================================================
# 9. Buffer & byte constants
# ===========================================================================

CONST_BUFFER_SIZE_BYTES: Final[int] = 1024
CONST_SECTOR_SIZE_BYTES: Final[int] = 512
CONST_MEBIBYTE_BYTES: Final[int] = 1024 * 1024
CONST_MEGABYTE_BYTES: Final[int] = 1_000_000
CONST_DOWNLOAD_CHUNK_SIZE: Final[int] = 1048576


# ===========================================================================
# 10. HTTP / download
# ===========================================================================

# --- Timeouts ---
HTTP_TIMEOUT_KERNEL_DOWNLOAD_S: Final[int] = 600
HTTP_TIMEOUT_KERNEL_CONFIG_S: Final[int] = 60
HTTP_TIMEOUT_SHA256_FETCH_S: Final[int] = 30
HTTP_TIMEOUT_SHA256_SIDECAR_S: Final[int] = 15
CONST_HTTP_TIMEOUT_SECONDS: Final[int] = 300

# --- Retry ---
CONST_DOWNLOAD_MAX_RETRIES: Final[int] = 3
CONST_DOWNLOAD_RETRY_DELAY: Final[float] = 1.0
CONST_DOWNLOAD_RETRY_BACKOFF: Final[float] = 2.0
CONST_SOCKET_TIMEOUT_SECONDS: Final[float] = 5.0
CONST_POLL_STEP_SECONDS: Final[float] = 0.1

# --- HTTP status codes ---
CONST_HTTP_STATUS_NO_CONTENT: Final[int] = 204
CONST_HTTP_STATUS_SUCCESS: Final[int] = 200

# --- Firecracker GitHub ---
FIRECRACKER_GITHUB_RELEASES_API_URL: Final[str] = (
    "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
)
FIRECRACKER_GITHUB_DOWNLOAD_URL: Final[str] = (
    "https://github.com/firecracker-microvm/firecracker/releases/download"
)


# ===========================================================================
# 11. Debug
# ===========================================================================

DEBUG_MODE: Final[bool] = False
CONST_TIMESTAMP_INITIAL: Final[float] = 0.0


# ===========================================================================
# 12. Lazy constants & helper functions
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


# ── Eager constants (resolved once at module load) ──────────────────────
CLI_NAME: str = _resolve_cli_name()
MVM_UNIX_GROUP: str = CLI_NAME
MVM_FORWARD_CHAIN: str = f"{CLI_NAME.upper()}-FORWARD"
MVM_POSTROUTING_CHAIN: str = f"{CLI_NAME.upper()}-POSTROUTING"
MVM_NOCLOUD_NET_INPUT_CHAIN: str = f"{CLI_NAME.upper()}-NOCLOUDNET-INPUT"


def env_var(suffix: str) -> str:
    """
    Return the environment variable name for the given suffix.

    Args:
        suffix: The variable suffix to append after the CLI name prefix.

    Returns:
        Full environment variable name in uppercase.

    """
    return f"{CLI_NAME.upper()}_{suffix}"


def __getattr__(name: str) -> Any:
    if name in _LAZY_CONSTANTS:
        return _LAZY_CONSTANTS[name]

    if name == "PROJECT_NAME":
        val = _resolve_project_name()
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "HTTP_USER_AGENT":
        val = f"{CLI_NAME}/{_resolve_version()}"
        _LAZY_CONSTANTS[name] = val
        return val
    if name == "SUDOERS_DROP_IN_PATH":
        val = "/etc/sudoers.d/{cli_name}".format(cli_name=CLI_NAME)
        _LAZY_CONSTANTS[name] = val
        return val
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
