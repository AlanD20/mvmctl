"""Host configuration state management using SQLite."""

from __future__ import annotations

import logging
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.constants import (
    CONST_FILE_PERMS_CONFIG,
    DEFAULT_SUDOERS_DIR,
    DEFAULT_SYSCTL_CONF_DIR,
    IPTABLES_RULES_V4,
    PROJECT_NAME,
    SUDOERS_DROP_IN_PATH,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import HostStateChange as DBHostStateChange
from mvmctl.exceptions import HostError
from mvmctl.models.host import HostState as HostState
from mvmctl.models.host import HostStateChange as HostStateChange

logger = logging.getLogger(__name__)

SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(f"{DEFAULT_SYSCTL_CONF_DIR}/{PROJECT_NAME}.conf")

RESTORABLE_SYSCTL_KEYS: frozenset[str] = frozenset({"net.ipv4.ip_forward"})
RESTORABLE_FILE_PATHS: frozenset[Path] = frozenset({Path(SUDOERS_DROP_IN_PATH), SYSCTL_CONF})


def _state_dir(cache_dir: Path) -> Path:
    """Return the host state subdirectory within the cache directory."""
    return cache_dir / "host"


def _state_file(cache_dir: Path) -> Path:
    """Return the path to the JSON host state snapshot file (legacy, now unused)."""
    return _state_dir(cache_dir) / "state.json"


def get_host_state(db: MVMDatabase) -> HostState | None:
    """Load the saved host state changes, or return None if none exist.

    Args:
        db: MVMDatabase instance for querying host state.

    Returns:
        HostState object with init_timestamp and changes, or None if no state is saved.
    """
    db.initialize_host_state()

    host_state_row = db.get_host_state()
    if host_state_row is None:
        return None

    # Get unreverted changes
    changes = db.list_host_changes(include_reverted=False)
    if not changes:
        return None

    host_changes = [
        HostStateChange(
            setting=change.setting,
            original_value=change.original_value,
            applied_value=change.applied_value,
            mechanism=change.mechanism,
        )
        for change in changes
    ]

    # Use initialized_at as init_timestamp if available
    init_timestamp = host_state_row.initialized_at or datetime.now(timezone.utc).isoformat()

    return HostState(init_timestamp=init_timestamp, changes=host_changes)


def _generate_session_id() -> str:
    """Generate a unique session ID for host init."""
    return datetime.now(timezone.utc).isoformat()


def _save_state(db: MVMDatabase, changes: list[HostStateChange]) -> None:
    """Persist host changes to the SQLite database.

    Args:
        db: MVMDatabase instance for persisting host state.
        changes: List of host changes to record.
    """

    db.initialize_host_state()

    session_id = _generate_session_id()
    init_timestamp = datetime.now(timezone.utc).isoformat()

    for idx, change in enumerate(changes):
        # Skip noop entries - they represent no actual change and have no rollback action
        if change.mechanism == "noop":
            continue

        db_change = DBHostStateChange(
            id=None,
            session_id=session_id,
            init_timestamp=init_timestamp,
            setting=change.setting,
            mechanism=change.mechanism,
            original_value=change.original_value,
            applied_value=change.applied_value,
            reverted=False,
            reverted_at=None,
            revert_mechanism=None,
            change_order=idx,
            created_at=init_timestamp,
        )
        db.add_host_change(db_change)


def restore_host(db: MVMDatabase) -> list[HostStateChange]:
    """Revert host changes recorded in the database.

    Processes changes in reverse order, restoring sysctl values and removing
    files that were created during host init.

    Args:
        db: MVMDatabase instance for querying and updating host state.

    Returns:
        List of HostChange records describing each reverted change.

    Raises:
        HostError: If no saved host state exists, or if a revert operation fails.
    """
    db.initialize_host_state()

    # Get unreverted changes
    changes = db.list_host_changes(include_reverted=False)
    if not changes:
        raise HostError("No saved host state to restore")

    reverted: list[HostStateChange] = []
    reverted_at = datetime.now(timezone.utc).isoformat()

    for change in reversed(changes):
        if change.mechanism == "sysctl" and change.original_value is not None:
            if change.setting not in RESTORABLE_SYSCTL_KEYS:
                logger.warning("Skipping disallowed sysctl key '%s' from state", change.setting)
                continue
            try:
                subprocess.run(
                    ["sysctl", "-w", f"{change.setting}={change.original_value}"],
                    capture_output=True,
                    text=True,
                    check=True,
                )
                reverted.append(
                    HostStateChange(
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

        elif change.mechanism == "iptables_save":
            rules_path = Path(IPTABLES_RULES_V4)
            try:
                if change.original_value is not None:
                    rules_path.write_text(change.original_value)
                    rules_path.chmod(CONST_FILE_PERMS_CONFIG)
                    logger.info("Restored original iptables rules to %s", rules_path)
                elif rules_path.exists():
                    rules_path.unlink()
                    logger.info("Removed %s (did not exist before host init)", rules_path)
                reverted.append(
                    HostStateChange(
                        setting=change.setting,
                        original_value=change.applied_value,
                        applied_value=change.original_value or "(removed)",
                        mechanism="iptables_restore",
                    )
                )
            except OSError as e:
                raise HostError(f"Failed to restore iptables rules: {e}") from e

        elif change.mechanism == "file_create":
            target = Path(change.applied_value).resolve()
            if not any(target == allowed.resolve() for allowed in RESTORABLE_FILE_PATHS):
                logger.warning("Skipping disallowed file path '%s' from state", target)
                continue
            if target.exists():
                try:
                    if change.original_value is not None:
                        # Validate sudoers content before writing to sudoers dir
                        if str(target).startswith(DEFAULT_SUDOERS_DIR):
                            result = subprocess.run(
                                ["visudo", "-c", "-f", "-"],
                                input=change.original_value,
                                capture_output=True,
                                text=True,
                            )
                            if result.returncode != 0:
                                raise HostError(
                                    f"Sudoers content from state failed visudo validation: "
                                    f"{result.stderr}"
                                )
                        target.write_text(change.original_value)
                    else:
                        target.unlink()
                    reverted.append(
                        HostStateChange(
                            setting=change.setting,
                            original_value=change.applied_value,
                            applied_value=change.original_value or "(removed)",
                            mechanism="file_remove",
                        )
                    )
                except OSError as e:
                    raise HostError(f"Failed to revert file {target}: {e}") from e

        # Mark change as reverted in database
        if change.id is not None:
            db.mark_change_reverted(change.id, reverted_at)

    return reverted
