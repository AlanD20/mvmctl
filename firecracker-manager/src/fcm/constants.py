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
DEFAULT_NETWORK_CIDR: Final[str] = "10.10.0.0/24"
DEFAULT_NETWORK_GATEWAY: Final[str] = "10.10.0.1"
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
