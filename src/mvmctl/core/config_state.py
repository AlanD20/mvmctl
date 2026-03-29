from __future__ import annotations

import json
import logging
import random
import string
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_CONFIG,
    DEFAULT_FIRECRACKER_CI_VERSION,
    DEFAULT_FIRECRACKER_VERSION,
)
from mvmctl.core.metadata import (
    get_default_binary_entry,
    get_default_image_entry,
    read_metadata,
    set_default_binary_entry,
    set_default_image_by_internal_id,
    set_default_image_entry,
    set_default_kernel_by_filename,
    update_binary_entry,
)
from mvmctl.utils.fs import get_bin_dir, get_cache_dir

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
    path.write_text(json.dumps(state, indent=2))
    path.chmod(CONST_FILE_PERMS_CONFIG)


def _rand_suffix(n: int = 3) -> str:
    return "".join(random.choices(string.ascii_lowercase + string.digits, k=n))


def get_config() -> dict[str, Any]:
    return _read_raw()


def set_config_value(key: str, value: Any) -> None:
    state = _read_raw()
    state[key] = value
    _write_raw(state)


def get_config_value(key: str, default: Any = None) -> Any:
    state = _read_raw()
    if key in state:
        return state[key]
    if key == "default_image":
        defaults = get_defaults_config()
        return defaults.get("image", default)
    return default


def get_firecracker_config() -> dict[str, str]:
    cache_dir = get_cache_dir()
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    defaults = binaries.get("defaults", {}) if isinstance(binaries, dict) else {}

    # Read default paths from new defaults section
    fc_defaults = defaults.get("firecracker", {}) if isinstance(defaults, dict) else {}
    default_binary_path = fc_defaults.get("binary_path") if isinstance(fc_defaults, dict) else None
    if not default_binary_path:
        default_binary_path = str(Path(get_bin_dir()) / "firecracker")

    default_binary = get_default_binary_entry(cache_dir)
    if default_binary is not None:
        version_key, entry = default_binary
        result: dict[str, str] = {k: v for k, v in entry.items() if isinstance(v, str)}
        full_version = result.get("full_version") or f"v{version_key.removeprefix('v')}"
        ci_version = result.get("ci_version")
        if not ci_version:
            normalized = version_key.removeprefix("v")
            parts = normalized.split(".")
            ci_version = f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{normalized}"

        merged = {
            "full_version": full_version,
            "ci_version": ci_version,
            "default_version": full_version,
            "default_binary_path": default_binary_path,
        }
        merged.update(result)
        return merged

    raw = _read_raw()
    section = raw.get(_FIRECRACKER_KEY, {})
    if not isinstance(section, dict):
        section = {}
    result = {k: v for k, v in section.items() if isinstance(v, str)}
    if "full_version" not in result:
        result["full_version"] = DEFAULT_FIRECRACKER_VERSION
    if "ci_version" not in result:
        result["ci_version"] = DEFAULT_FIRECRACKER_CI_VERSION
    return result


def initialize_default_config() -> dict[str, Any]:
    """Initialize config file with default values if not present.

    Creates the config file with default Firecracker version settings and
    assets paths.
    Returns the initialized config.

    Returns:
        The config dictionary with defaults applied.
    """
    state = _read_raw()
    changed = False

    if _FIRECRACKER_KEY not in state or not isinstance(state.get(_FIRECRACKER_KEY), dict):
        state[_FIRECRACKER_KEY] = {}
        changed = True

    fc_section = state[_FIRECRACKER_KEY]
    if "full_version" not in fc_section:
        fc_section["full_version"] = DEFAULT_FIRECRACKER_VERSION
        changed = True
    if "ci_version" not in fc_section:
        fc_section["ci_version"] = DEFAULT_FIRECRACKER_CI_VERSION
        changed = True

    # Write firecracker section BEFORE calling get_assets_config so it isn't
    # lost when we re-read the file after get_assets_config() writes assets.
    if changed:
        _write_raw(state)
        changed = False

    get_assets_config()
    state = _read_raw()

    if _DEFAULTS_KEY in state:
        del state[_DEFAULTS_KEY]
        changed = True

    if "default_image" in state:
        del state["default_image"]
        changed = True

    if changed:
        _write_raw(state)

    return state


def update_firecracker_config(**fields: str) -> None:
    normalized_fields = {k: v for k, v in fields.items() if isinstance(v, str)}
    existing = get_firecracker_config()
    full_version = normalized_fields.get(
        "full_version", existing.get("full_version", DEFAULT_FIRECRACKER_VERSION)
    )
    ci_version = normalized_fields.get(
        "ci_version", existing.get("ci_version", DEFAULT_FIRECRACKER_CI_VERSION)
    )
    normalized_version = full_version.removeprefix("v")
    default_binary_path = normalized_fields.get(
        "default_binary_path", str(Path(get_bin_dir()) / "firecracker")
    )

    cache_dir = get_cache_dir()
    payload = dict(normalized_fields)
    payload["full_version"] = full_version
    payload["ci_version"] = ci_version
    payload["default_version"] = payload.get("default_version", full_version)
    payload["default_binary_path"] = payload.get("default_binary_path", default_binary_path)

    update_binary_entry(
        cache_dir,
        normalized_version,
        **payload,
    )
    set_default_binary_entry(cache_dir, normalized_version)


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


def update_assets_config(**fields: str) -> None:
    state = _read_raw()
    section: dict[str, str] = {}
    existing = state.get(_ASSETS_KEY)
    if isinstance(existing, dict):
        section = {k: v for k, v in existing.items() if isinstance(v, str)}
    section.update(fields)
    state[_ASSETS_KEY] = section
    _write_raw(state)


def get_defaults_config() -> dict[str, Any]:
    cache_dir = get_cache_dir()
    defaults: dict[str, Any] = {"image": None, "kernel": None}

    default_image = get_default_image_entry(cache_dir)
    if default_image is not None:
        image_id, image_meta = default_image
        defaults["image"] = image_meta.get("internal_id") or image_id

    from mvmctl.core.metadata import get_default_kernel_entry

    default_kernel = get_default_kernel_entry(cache_dir)
    if default_kernel is not None:
        _kernel_id, kernel_meta = default_kernel
        defaults["kernel"] = kernel_meta.get("filename")

    return defaults


def set_defaults_value(key: str, value: Any) -> None:
    cache_dir = get_cache_dir()
    if key == "image":
        if not isinstance(value, str):
            raise ValueError("Default image must be a string image identifier")
        try:
            set_default_image_entry(cache_dir, value)
            return
        except KeyError:
            set_default_image_by_internal_id(cache_dir, value)
            return

    if key == "kernel":
        if not isinstance(value, str):
            raise ValueError("Default kernel must be a string kernel filename")
        set_default_kernel_by_filename(cache_dir, value)
        return

    state = _read_raw()
    state[key] = value
    _write_raw(state)


def get_defaults_value(key: str, default: Any = None) -> Any:
    """Get a single value from the defaults section."""
    return get_defaults_config().get(key, default)
