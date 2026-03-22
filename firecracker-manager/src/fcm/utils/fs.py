"""Filesystem path helpers for FCM cache directories."""

import os
from pathlib import Path

_PROJECT_NAME = "firecracker-manager"


def get_cache_dir() -> Path:
    """Return the FCM cache root directory.

    Checks FCM_CACHE_DIR env var first, then falls back to
    ~/.cache/firecracker-manager.
    """
    override = os.environ.get("FCM_CACHE_DIR")
    if override:
        return Path(override)
    return Path.home() / ".cache" / _PROJECT_NAME


def get_vms_dir() -> Path:
    """Return the directory that holds VM state and per-VM dirs."""
    return get_cache_dir() / "vms"


def get_vm_dir(name: str) -> Path:
    """Return the directory for a specific VM."""
    return get_vms_dir() / name


def get_images_dir() -> Path:
    """Return the directory for cached images."""
    return get_cache_dir() / "images"


def get_kernels_dir() -> Path:
    """Return the directory for cached kernels."""
    return get_cache_dir() / "kernels"


def get_state_file() -> Path:
    """Return the path to the VM state JSON file."""
    return get_vms_dir() / "state.json"


def get_assets_dir() -> Path:
    """Return the path to the bundled assets directory inside the package."""
    return Path(__file__).parent.parent / "assets"
