"""Host configuration state management."""

from __future__ import annotations

import json
import logging
import os
import subprocess
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path

from fcm.constants import CLI_NAME, PROJECT_NAME, SUDOERS_DROP_IN_PATH
from fcm.exceptions import HostError

logger = logging.getLogger(__name__)

SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(f"/etc/sysctl.d/{PROJECT_NAME}.conf")

RESTORABLE_SYSCTL_KEYS: frozenset[str] = frozenset({"net.ipv4.ip_forward"})
RESTORABLE_FILE_PATHS: frozenset[Path] = frozenset({Path(SUDOERS_DROP_IN_PATH), SYSCTL_CONF})

@dataclass
class HostChange:
    setting: str
    original_value: str | None
    applied_value: str
    mechanism: str

@dataclass
class HostState:
    init_timestamp: str
    changes: list[HostChange]

def _state_dir(cache_dir: Path) -> Path:
    return cache_dir / "host"

def _state_file(cache_dir: Path) -> Path:
    return _state_dir(cache_dir) / "state.json"

def get_host_state(cache_dir: Path) -> HostState | None:
    path = _state_file(cache_dir)
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text())
        return HostState(
            init_timestamp=data["init_timestamp"],
            changes=[HostChange(**c) for c in data["changes"]],
        )
    except (json.JSONDecodeError, KeyError, TypeError) as e:
        raise HostError(f"Corrupt state file {path}: {e}") from e

def _save_state(cache_dir: Path, changes: list[HostChange]) -> None:
    state_dir = _state_dir(cache_dir)
    state_dir.mkdir(parents=True, exist_ok=True)
    state = HostState(
        init_timestamp=datetime.now(timezone.utc).isoformat(),
        changes=changes,
    )
    data = {
        "init_timestamp": state.init_timestamp,
        "changes": [asdict(c) for c in state.changes],
    }
    sf = _state_file(cache_dir)
    sf.write_text(json.dumps(data, indent=2) + "\n")
    os.chmod(sf, 0o600)

def restore_host(cache_dir: Path) -> list[HostChange]:
    state = get_host_state(cache_dir)
    if not state:
        raise HostError("No saved host state to restore")

    reverted: list[HostChange] = []
    for change in reversed(state.changes):
        if change.mechanism == "sysctl" and change.original_value is not None:
            if change.setting not in RESTORABLE_SYSCTL_KEYS:
                logger.warning(
                    "Skipping disallowed sysctl key '%s' from state file", change.setting
                )
                continue
            try:
                subprocess.run(
                    ["sysctl", "-w", f"{change.setting}={change.original_value}"],
                    capture_output=True,
                    text=True,
                    check=True,
                )
                reverted.append(
                    HostChange(
                        setting=change.setting,
                        original_value=change.applied_value,
                        applied_value=change.original_value,
                        mechanism="sysctl",
                    )
                )
            except subprocess.CalledProcessError as e:
                raise HostError(f"Failed to revert {change.setting}: {e}") from e
            except FileNotFoundError as e:
                raise HostError("sysctl command not found") from e

        elif change.mechanism == "file_create":
            target = Path(change.applied_value).resolve()
            if not any(target == allowed.resolve() for allowed in RESTORABLE_FILE_PATHS):
                logger.warning("Skipping disallowed file path '%s' from state file", target)
                continue
            if target.exists():
                try:
                    if change.original_value is not None:
                        target.write_text(change.original_value)
                    else:
                        target.unlink()
                    reverted.append(
                        HostChange(
                            setting=change.setting,
                            original_value=change.applied_value,
                            applied_value=change.original_value or "(removed)",
                            mechanism="file_remove",
                        )
                    )
                except OSError as e:
                    raise HostError(f"Failed to revert file {target}: {e}") from e

    state_file = _state_file(cache_dir)
    if state_file.exists():
        state_file.unlink()

    return reverted
