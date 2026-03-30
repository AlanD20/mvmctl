from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Any

from mvmctl.constants import CLI_NAME, CONST_FILE_PERMS_CONFIG

logger = logging.getLogger(__name__)


def _user_config_path() -> Path:
    import os

    override = os.environ.get(f"{CLI_NAME.upper()}_CONFIG")
    if override:
        return Path(override)
    return Path.home() / ".config" / CLI_NAME / "config.json"


def _load_user_config() -> dict[str, Any]:
    path = _user_config_path()
    if not path.exists():
        return {}
    try:
        data = json.loads(path.read_text()) or {}
        if not isinstance(data, dict):
            return {}
        return data
    except json.JSONDecodeError as exc:
        logger.warning("Could not parse user config at %s: %s", path, exc)
        return {}


def _save_user_config(data: dict[str, Any]) -> None:
    path = _user_config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data))
    path.chmod(CONST_FILE_PERMS_CONFIG)


def get_config_value(key: str) -> Any:
    parts = key.replace("-", "_").split(".")
    data: Any = _load_user_config()
    for part in parts:
        if not isinstance(data, dict):
            return None
        data = data.get(part)
    return data


def set_config_value(key: str, value: str) -> None:
    parts = key.replace("-", "_").split(".")
    config = _load_user_config()
    node: dict[str, Any] = config
    for part in parts[:-1]:
        if part not in node or not isinstance(node[part], dict):
            node[part] = {}
        node = node[part]
    node[parts[-1]] = _coerce_value(value)
    _save_user_config(config)


def _coerce_value(value: str) -> Any:
    if value.lower() in ("true", "yes"):
        return True
    if value.lower() in ("false", "no"):
        return False
    try:
        return int(value)
    except ValueError:
        pass
    try:
        return float(value)
    except ValueError:
        pass
    return value


def get_full_user_config() -> dict[str, Any]:
    return _load_user_config()
