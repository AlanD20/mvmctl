"""Host controller — stateful host state management."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

from mvmctl.core.host._repository import HostRepository
from mvmctl.models.host import HostStateChangeItem


class HostController:
    """Stateful singleton controller for host state."""

    def __init__(self, repo: HostRepository) -> None:
        self._repo = repo

    def record_changes(self, changes: list[HostStateChangeItem]) -> None:
        """Persist host state changes to the database.

        Uses an atomic bulk insert, then deletes all prior sessions so only
        the latest backup remains.
        """
        session_id = str(uuid.uuid4())
        now = datetime.now(timezone.utc).isoformat()
        for order, change in enumerate(changes):
            change.session_id = session_id
            change.init_timestamp = now
            change.change_order = order
            change.created_at = now
        self._repo.add_changes(changes)
        self._repo.delete_changes_except_session(session_id)

    def mark_initialized(self, timestamp: str) -> None:
        """Mark host as fully initialized."""
        self._repo.initialize_state()
        self._repo.set_initialized(timestamp)

    def reset_state(self) -> None:
        """Reset all host state flags to False."""
        self._repo.reset_state()


__all__ = ["HostController"]
