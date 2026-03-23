"""Project identity constants derived from pyproject.toml metadata."""

from __future__ import annotations

import importlib.metadata


def _resolve_project_name() -> str:
    try:
        return importlib.metadata.metadata("firecracker-manager")["Name"]
    except importlib.metadata.PackageNotFoundError:
        return "firecracker-manager"


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


PROJECT_NAME: str = _resolve_project_name()

PROJECT_NAME_UPPER: str = PROJECT_NAME.replace("-", "_").upper()

CLI_NAME: str = _resolve_cli_name()


def env_var(suffix: str) -> str:
    return f"{CLI_NAME.upper()}_{suffix}"


def cache_dir_name() -> str:
    return PROJECT_NAME


def device_prefix() -> str:
    return CLI_NAME


def config_filename() -> str:
    return f"{CLI_NAME}.yaml"


BRIDGE_NAME: str = f"{device_prefix()}-br0"

TAP_PREFIX: str = f"{CLI_NAME}-tap"

PROJECT_GROUP: str = CLI_NAME  # "fcm"
SUDOERS_DROP_IN_PATH: str = f"/etc/sudoers.d/{CLI_NAME}"
DEFAULT_NETWORK_NAME: str = "default"
DEFAULT_NETWORK_CIDR: str = "10.10.0.0/24"
DEFAULT_NETWORK_GATEWAY: str = "10.10.0.1"
BRIDGE_PREFIX: str = f"{CLI_NAME}-br"
FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S: int = 5
FIRECRACKER_SIGTERM_WAIT_S: int = 1
PRIVILEGED_BINARIES: list[str] = [
    "/usr/sbin/ip",
    "/usr/sbin/iptables",
    "/usr/sbin/iptables-restore",
    "/usr/sbin/iptables-save",
    "/usr/sbin/sysctl",
]
