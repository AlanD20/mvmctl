"""Project identity constants derived from pyproject.toml metadata."""

from __future__ import annotations

import importlib.metadata


def _resolve_project_name() -> str:
    try:
        return importlib.metadata.metadata("firecracker-manager")["Name"]
    except importlib.metadata.PackageNotFoundError:
        return "firecracker-manager"


PROJECT_NAME: str = _resolve_project_name()

PROJECT_NAME_UPPER: str = PROJECT_NAME.replace("-", "_").upper()

CLI_NAME: str = "fcm"


def env_var(suffix: str) -> str:
    return f"{CLI_NAME.upper()}_{suffix}"


def cache_dir_name() -> str:
    return PROJECT_NAME


def device_prefix() -> str:
    return CLI_NAME


def config_filename() -> str:
    return f"{CLI_NAME}.yaml"


BRIDGE_NAME: str = f"{device_prefix()}-br0"

TAP_PREFIX: str = device_prefix()
