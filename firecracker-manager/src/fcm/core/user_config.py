from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

import yaml

from fcm.constants import CLI_NAME

logger = logging.getLogger(__name__)


def _user_config_path() -> Path:
    import os

    override = os.environ.get(f"{CLI_NAME.upper()}_CONFIG")
    if override:
        return Path(override)
    return Path.home() / ".config" / CLI_NAME / "config.yaml"


def _load_user_config() -> dict[str, Any]:
    path = _user_config_path()
    if not path.exists():
        return {}
    try:
        data = yaml.safe_load(path.read_text()) or {}
        if not isinstance(data, dict):
            return {}
        return data
    except yaml.YAMLError as exc:
        logger.warning("Could not parse user config at %s: %s", path, exc)
        return {}


def _save_user_config(data: dict[str, Any]) -> None:
    path = _user_config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump(data, default_flow_style=False))
    path.chmod(0o600)


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
