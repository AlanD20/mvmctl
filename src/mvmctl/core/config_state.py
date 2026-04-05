from __future__ import annotations

import json
import logging
import sqlite3
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_CONFIG,
)
from mvmctl.core.metadata import (
    get_default_image_entry,
    set_default_image_by_os_slug,
    set_default_image_entry,
    set_default_kernel_by_filename,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import AssetNotFoundError
from mvmctl.utils.fs import get_cache_dir

logger = logging.getLogger(__name__)

_FIRECRACKER_KEY = "firecracker"
_ASSETS_KEY = "assets"
_DEFAULTS_KEY = "defaults"


def _config_path() -> Path:
    from mvmctl.utils.fs import get_config_file

    return get_config_file()


def _read_raw() -> dict[str, Any]:
    path = _config_path()
    if not path.exists():
        return {}
    try:
        data: dict[str, Any] = json.loads(path.read_text())
        return data
    except json.JSONDecodeError:
        logger.warning("Corrupt config at %s — returning empty state", path)
        return {}


def _write_raw(state: dict[str, Any]) -> None:
    path = _config_path()
    path.parent.mkdir(parents=True, exist_ok=True, mode=CONST_DIR_PERMS_CACHE)
    path.parent.chmod(CONST_DIR_PERMS_CACHE)
    path.write_text(json.dumps(state))
    path.chmod(CONST_FILE_PERMS_CONFIG)


def get_firecracker_config() -> dict[str, str]:
    db = MVMDatabase()
    binary_default = db.get_default_binary("firecracker")
    if binary_default is None:
        raise AssetNotFoundError(
            "No active binary for 'firecracker' found — "
            "run 'mvm bin fetch <version>' to download one."
        )
    full_version = binary_default.full_version or f"v{binary_default.version}"
    return {
        "full_version": full_version,
        "ci_version": binary_default.ci_version or f"v{binary_default.version}",
        "default_version": full_version,
        "active_version": full_version,
        "binary_path": binary_default.path or "",
    }


def initialize_default_config() -> dict[str, Any]:
    """Initialize config file with default values if not present.

    Creates the config file with default Firecracker version settings and
    assets paths.
    Returns the initialized config.

    Returns:
        The config dictionary with defaults applied.
    """
    state = _read_raw()

    if _FIRECRACKER_KEY in state:
        del state[_FIRECRACKER_KEY]
        _write_raw(state)

    get_assets_config()
    state = _read_raw()

    changed = False

    if _DEFAULTS_KEY in state:
        del state[_DEFAULTS_KEY]
        changed = True

    if "default_image" in state:
        del state["default_image"]
        changed = True

    if changed:
        _write_raw(state)

    return state


def get_assets_config() -> dict[str, str]:
    from mvmctl.utils.fs import (
        get_bin_dir,
        get_images_dir,
        get_kernels_dir,
        get_keys_dir,
        get_logs_dir,
        get_vms_dir,
    )

    state = _read_raw()
    section: dict[str, str] = {}
    existing = state.get(_ASSETS_KEY)
    if isinstance(existing, dict):
        section = {k: v for k, v in existing.items() if isinstance(v, str)}

    changed = False

    def _default(key: str, fallback: str) -> str:
        nonlocal changed
        if key not in section:
            section[key] = fallback
            changed = True
        return section[key]

    _default("kernels_dir", str(get_kernels_dir()))
    _default("images_dir", str(get_images_dir()))
    _default("bin_dir", str(get_bin_dir()))
    _default("vms_dir", str(get_vms_dir()))
    _default("keys_dir", str(get_keys_dir()))
    _default("logs_dir", str(get_logs_dir()))

    if changed:
        state[_ASSETS_KEY] = section
        _write_raw(state)

    return dict(section)


def get_defaults_config() -> dict[str, Any]:
    cache_dir = get_cache_dir()
    defaults: dict[str, Any] = {"image": None, "kernel": None}

    # Try SQLite first for image default
    try:
        db = MVMDatabase()
        images = db.list_images()
        for image in images:
            if image.is_default:
                defaults["image"] = image.os_slug or image.id
                break
    except sqlite3.OperationalError:
        pass

    # Fall back to JSON for image
    if defaults["image"] is None:
        default_image = get_default_image_entry(cache_dir)
        if default_image is not None:
            image_id, image_meta = default_image
            defaults["image"] = image_meta.get("os_slug") or image_id

    # Try SQLite first for kernel default
    try:
        db = MVMDatabase()
        kernels = db.list_kernels()
        for kernel in kernels:
            if kernel.is_default:
                defaults["kernel"] = kernel.path
                break
    except sqlite3.OperationalError:
        pass

    # Fall back to JSON for kernel
    if defaults["kernel"] is None:
        from mvmctl.core.metadata import get_default_kernel_entry

        default_kernel = get_default_kernel_entry(cache_dir)
        if default_kernel is not None:
            _kernel_id, kernel_meta = default_kernel
            defaults["kernel"] = kernel_meta.get("path")

    return defaults


def set_defaults_value(key: str, value: Any) -> None:
    cache_dir = get_cache_dir()
    if key == "image":
        if not isinstance(value, str):
            raise ValueError("Default image must be a string image identifier")
        try:
            set_default_image_entry(cache_dir, value)
        except KeyError:
            set_default_image_by_os_slug(cache_dir, value)

        try:
            db = MVMDatabase()
            db.set_default_image(value)
        except sqlite3.OperationalError:
            pass
        return

    if key == "kernel":
        if not isinstance(value, str):
            raise ValueError("Default kernel must be a string kernel filename")
        set_default_kernel_by_filename(cache_dir, value)

        try:
            db = MVMDatabase()
            kernels = db.list_kernels()
            for kernel in kernels:
                if kernel.path == value:
                    db.set_default_kernel(kernel.id)
                    break
        except sqlite3.OperationalError:
            pass
        return

    state = _read_raw()
    state[key] = value
    _write_raw(state)
