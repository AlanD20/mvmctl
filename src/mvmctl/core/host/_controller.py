"""Host controller — stateful host state management."""

from __future__ import annotations

import uuid
from datetime import UTC, datetime

from mvmctl.core.host._repository import HostRepository
from mvmctl.models import HostStateChangeItem


class HostController:
    """Stateful singleton controller for host state."""

    def __init__(self, repo: HostRepository) -> None:
        self._repo = repo

    def record_changes(
        self,
        changes: list[HostStateChangeItem],
        session_id: str | None = None,
        change_order_offset: int = 0,
    ) -> str:
        """
        Persist host state changes to the database.

        Uses an atomic bulk insert, then deletes all prior sessions so only
        the latest backup remains.

        Args:
            changes: List of changes to persist.
            session_id: Optional session ID to use; generated if not provided.
            change_order_offset: Starting value for change_order enumeration.

        Returns:
            The session ID used.

        """
        sid = session_id or str(uuid.uuid4())
        now = datetime.now(UTC).isoformat()
        for order, change in enumerate(changes, start=change_order_offset):
            change.session_id = sid
            change.init_timestamp = now
            change.change_order = order
            change.created_at = now
        self._repo.add_changes(changes)
        self._repo.delete_changes_except_session(sid)
        return sid

    def mark_initialized(self, timestamp: str) -> None:
        """Mark host as fully initialized."""
        self._repo.initialize_state()
        self._repo.set_initialized(timestamp)

    def reset_state(self) -> None:
        """Reset all host state flags to False."""
        self._repo.reset_state()


__all__ = ["HostController"]
