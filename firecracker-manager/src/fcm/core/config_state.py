from __future__ import annotations

import json
import logging
import random
import string
from pathlib import Path
from typing import Any

from fcm.constants import (
    DEFAULT_FIRECRACKER_CI_VERSION,
    DEFAULT_FIRECRACKER_VERSION,
)

logger = logging.getLogger(__name__)

_FIRECRACKER_KEY = "firecracker"
_ASSETS_KEY = "assets"


def _config_path() -> Path:
    from fcm.utils.fs import get_config_file

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
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(state, indent=2))
    path.chmod(0o600)


def _rand_suffix(n: int = 3) -> str:
    return "".join(random.choices(string.ascii_lowercase + string.digits, k=n))


def get_config() -> dict[str, Any]:
    return _read_raw()


def set_config_value(key: str, value: Any) -> None:
    state = _read_raw()
    state[key] = value
    _write_raw(state)


def get_config_value(key: str, default: Any = None) -> Any:
    return _read_raw().get(key, default)


def get_firecracker_config() -> dict[str, str]:
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

    Creates the config file with default Firecracker version settings.
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

    if changed:
        _write_raw(state)

    return state


def update_firecracker_config(**fields: str) -> None:
    state = _read_raw()
    section: dict[str, str] = {}
    existing = state.get(_FIRECRACKER_KEY)
    if isinstance(existing, dict):
        section = {k: v for k, v in existing.items() if isinstance(v, str)}
    section.update(fields)
    state[_FIRECRACKER_KEY] = section
    _write_raw(state)


def get_assets_config() -> dict[str, str]:
    from fcm.constants import CLI_NAME
    from fcm.utils.fs import (
        get_bin_dir,
        get_images_dir,
        get_kernels_dir,
        get_keys_dir,
        get_logs_dir,
        get_networks_dir,
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
    _default("networks_dir", str(get_networks_dir()))
    _default("vms_dir", str(get_vms_dir()))
    _default("keys_dir", str(get_keys_dir()))
    _default("logs_dir", str(get_logs_dir()))

    for key, prefix in (
        ("kernel_build_dir", f"/tmp/{CLI_NAME}-kernel-build-"),
        ("image_import_dir", f"/tmp/{CLI_NAME}-image-import-"),
    ):
        if key not in section:
            section[key] = prefix + _rand_suffix()
            changed = True

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
