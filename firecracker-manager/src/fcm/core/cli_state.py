"""CLI state persistence — tracks global settings like active CI_VERSION."""

from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


def _state_path() -> Path:
    from fcm.utils.fs import get_cache_dir

    return get_cache_dir() / "cli-state.json"


def get_cli_state() -> dict[str, Any]:
    path = _state_path()
    if not path.exists():
        return {}
    try:
        data: dict[str, Any] = json.loads(path.read_text())
        return data
    except (json.JSONDecodeError, OSError):
        logger.warning("Corrupt CLI state at %s — returning empty state", path)
        return {}


def set_cli_state_value(key: str, value: Any) -> None:
    state = get_cli_state()
    state[key] = value
    path = _state_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(state, indent=2))
    path.chmod(0o600)


def get_cli_state_value(key: str, default: Any = None) -> Any:
    return get_cli_state().get(key, default)
