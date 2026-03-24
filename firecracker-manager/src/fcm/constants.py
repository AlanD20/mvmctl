"""Project identity constants derived from pyproject.toml metadata."""

import functools
import importlib.metadata
from typing import Final

_BOOTSTRAP_NAME: Final[str] = "firecracker-manager"


@functools.lru_cache(maxsize=1)
def _resolve_project_name() -> str:
    """Resolve the project name from package metadata, falling back to bootstrap name."""
    try:
        return importlib.metadata.metadata(_BOOTSTRAP_NAME)["Name"]
    except importlib.metadata.PackageNotFoundError:
        return _BOOTSTRAP_NAME


@functools.lru_cache(maxsize=1)
def _resolve_cli_name() -> str:
    """Resolve CLI name from entry points, falling back to 'fcm'."""
    try:
        eps = importlib.metadata.entry_points(group="console_scripts")
        for ep in eps:
            if ep.value == "fcm.main:app" or ep.value.endswith("main:app"):
                return ep.name
    except (IOError, ValueError):
        pass
    return "fcm"


PROJECT_NAME: Final[str] = _resolve_project_name()

PROJECT_NAME_UPPER: Final[str] = PROJECT_NAME.replace("-", "_").upper()

CLI_NAME: Final[str] = _resolve_cli_name()


def env_var(suffix: str) -> str:
    """Return the environment variable name for the given suffix (e.g. FCM_SUFFIX).

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
    """Return the config file name for the CLI (e.g. fcm.yaml)."""
    return f"{CLI_NAME}.yaml"


BRIDGE_NAME: Final[str] = f"{device_prefix()}-br0"

TAP_PREFIX: Final[str] = f"{CLI_NAME}-tap"

PROJECT_GROUP: Final[str] = CLI_NAME  # "fcm"
SUDOERS_DROP_IN_PATH: Final[str] = f"/etc/sudoers.d/{CLI_NAME}"
DEFAULT_NETWORK_NAME: Final[str] = "default"
# Phase 5 spec proposed 10.10.0.0/24 but Phase 6 (more recent) validated 172.35.0.0/24.
# Using Phase 6 value per the "use most recent spec" policy.
DEFAULT_NETWORK_CIDR: Final[str] = "172.35.0.0/24"
DEFAULT_NETWORK_GATEWAY: Final[str] = "172.35.0.1"
DEFAULT_BRIDGE_NAME: Final[str] = "fcm-bridge"
BRIDGE_PREFIX: Final[str] = f"{CLI_NAME}-br"
FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S: Final[int] = 5
FIRECRACKER_SIGTERM_WAIT_S: Final[int] = 1
PRIVILEGED_BINARIES: Final[list[str]] = [
    "/usr/sbin/ip",
    "/usr/sbin/iptables",
    "/usr/sbin/iptables-restore",
    "/usr/sbin/iptables-save",
    "/usr/sbin/sysctl",
]
REQUIRED_BINARIES: Final[list[str]] = ["ip", "iptables", "qemu-img"]
ISO_BINARIES: Final[list[str]] = ["mkisofs", "genisoimage"]

# ---------------------------------------------------------------------------
# VM instance defaults (user-facing; also referenced by VMDefaultsConfig)
# ---------------------------------------------------------------------------

DEFAULT_VM_VCPU_COUNT: Final[int] = 2
DEFAULT_VM_MEM_MIB: Final[int] = 2048
DEFAULT_VM_SSH_USER: Final[str] = "root"
DEFAULT_FIRECRACKER_BIN_NAME: Final[str] = "firecracker"

# VM feature flags
DEFAULT_VM_ENABLE_API_SOCKET: Final[bool] = False
DEFAULT_VM_ENABLE_PCI: Final[bool] = False

# VM network defaults
DEFAULT_VM_SUBNET_MASK: Final[str] = "255.255.255.0"
DEFAULT_VM_NETWORK_INTERFACE: Final[str] = "eth0"
DEFAULT_VM_BOOT_ARGS: Final[str] = "console=ttyS0 reboot=k panic=1 pci=off"
DEFAULT_VM_LSM_FLAGS: Final[str] = "landlock,lockdown,yama,integrity,selinux,bpf"
DEFAULT_VM_DISK_SIZE: Final[str] = "2G"

# VM model structural defaults (internal path names)
DEFAULT_VM_KERNEL_FILENAME: Final[str] = "vmlinux"
DEFAULT_VM_ROOTFS_FILENAME: Final[str] = "rootfs.ext4"

# Firecracker binary path default
DEFAULT_FIRECRACKER_BINARY_PATH: Final[str] = "/usr/local/bin/firecracker"

# Network bridge defaults
DEFAULT_NETWORK_BRIDGE_IP: Final[str] = "172.35.0.1/24"

# Image defaults
DEFAULT_IMAGE_CONVERT_TO: Final[str] = "ext4"
DEFAULT_IMAGE_IMPORT_FORMAT: Final[str] = "auto"
DEFAULT_IMAGE_IMPORT_SIZE_MIB: Final[int] = 2048
SUPPORTED_IMAGE_EXTENSIONS: Final[list[str]] = [".ext4", ".btrfs", ".img", ".raw"]

IMAGE_IMPORT_FORMAT_MAP: Final[dict[str, str]] = {
    ".qcow2": "qcow2",
    ".raw": "raw",
    ".img": "raw",
    ".tar": "tar-rootfs",
    ".tar.gz": "tar-rootfs",
    ".tar.xz": "tar-rootfs",
    ".tgz": "tar-rootfs",
}

# VM log defaults
DEFAULT_VM_LOG_TYPE: Final[str] = "os"
DEFAULT_VM_LOG_LINES: Final[int] = 50
DEFAULT_VM_LOG_FOLLOW: Final[bool] = False

# Snapshot defaults
DEFAULT_SNAPSHOT_RESUME: Final[bool] = True

# Binary management defaults
DEFAULT_REMOTE_VERSION_LIMIT: Final[int] = 5

# ---------------------------------------------------------------------------
# Fallback values — last-resort runtime values when config lookup fails
# ---------------------------------------------------------------------------

FALLBACK_FC_CI_VERSION: Final[str] = "1.12"
FALLBACK_FIRECRACKER_BIN: Final[str] = "firecracker"
FALLBACK_KERNEL_BUILD_JOBS: Final[int] = 1
FALLBACK_MAX_PARALLEL_DOWNLOADS: Final[int] = 4

# ---------------------------------------------------------------------------
# Firecracker defaults
# ---------------------------------------------------------------------------

# Default Firecracker version (full semantic version)
DEFAULT_FIRECRACKER_VERSION: Final[str] = "v1.15.0"

# Default Firecracker CI version (major.minor for kernel downloads)
DEFAULT_FIRECRACKER_CI_VERSION: Final[str] = "v1.15"

# ---------------------------------------------------------------------------
# Kernel defaults
# ---------------------------------------------------------------------------

# Default kernel version for official upstream builds
DEFAULT_KERNEL_VERSION: Final[str] = "6.19.9"

# Default architecture for Firecracker CI kernel downloads
DEFAULT_FC_KERNEL_ARCH: Final[str] = "x86_64"

# Base URL for the Firecracker CI S3 kernel bucket
FIRECRACKER_CI_KERNEL_S3_BASE: Final[str] = "https://s3.amazonaws.com/spec.ccfc.min"

# S3 listing URL template; fill in {ci_version} and {arch}
FIRECRACKER_CI_KERNEL_LIST_URL: Final[str] = (
    "http://spec.ccfc.min.s3.amazonaws.com/"
    "?prefix=firecracker-ci/{ci_version}/{arch}/vmlinux-&list-type=2"
)

# Firecracker microvm kernel config URL — the {major_minor} placeholder is
# e.g. "6.1" and is substituted at runtime.
FIRECRACKER_KERNEL_CONFIG_URL: Final[str] = (
    "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main"
    "/resources/guest_configs/microvm-kernel-ci-x86_64-{major_minor}.config"
)

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

# Kernel options to disable (--disable) when building an official kernel.
KERNEL_DISABLED_CONFIGS: Final[list[str]] = [
    "CONFIG_BLK_DEV_ZONED",
    "CONFIG_VIRTIO_BLK_F_SECURE_ERASE",
    "CONFIG_VIRTIO_BLK_SCSI",
]

# Integer-valued kernel options (--set-val).  Each entry is (option, value).
KERNEL_SET_VAL_CONFIGS: Final[list[tuple[str, str]]] = [
    ("CONFIG_SERIAL_8250_NR_UARTS", "4"),
]

# Critical settings that MUST be =y for Firecracker to boot.  If any is
# missing after configuration, the build is aborted with an error.
KERNEL_REQUIRED_SETTINGS: Final[list[str]] = [
    "CONFIG_BTRFS_FS=y",
    "CONFIG_VIRTIO_BLK=y",
    "CONFIG_VIRTIO_NET=y",
    "CONFIG_SERIAL_8250_CONSOLE=y",
    "CONFIG_KVM_GUEST=y",
]


def _resolve_version() -> str:
    try:
        return importlib.metadata.version(_BOOTSTRAP_NAME)
    except importlib.metadata.PackageNotFoundError:
        return "0.0.0"


HTTP_USER_AGENT: Final[str] = f"{CLI_NAME}/{_resolve_version()}"
MAX_VMS: Final[int] = 50

# External URLs
FIRECRACKER_GITHUB_RELEASES_API_URL: Final[str] = (
    "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
)
FIRECRACKER_GITHUB_DOWNLOAD_URL: Final[str] = (
    "https://github.com/firecracker-microvm/firecracker/releases/download"
)
FIRECRACKER_GITHUB_RAW_URL: Final[str] = (
    "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main"
)
KERNEL_TARBALL_URL_TEMPLATE: Final[str] = (
    "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"
)
KERNEL_SHA256_URL_TEMPLATE: Final[str] = (
    "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz.sha256"
)
