"""Host database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.db.models import HostState, HostStateChange


class HostRepository:
    """Database operations for host state."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get_state(self) -> HostState | None:
        """Return the singleton host state row, or None if not yet initialized."""
        with self._db.connect() as conn:
            row = conn.execute("SELECT * FROM host_state WHERE id = 1").fetchone()
        if row is None:
            return None
        return HostState(**dict(row))

    def initialize_state(self) -> HostState:
        """Insert the singleton host state row (id=1) if it doesn't exist."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT OR IGNORE INTO host_state
                (id, initialized, mvm_group_created, sudoers_configured, default_network_created, initialized_at, updated_at)
                VALUES (1, 0, 0, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
                """
            )
        host_state = self.get_state()
        assert host_state is not None
        return host_state

    def set_initialized(self, initialized_at: str) -> None:
        """Mark host as fully initialized."""
        with self._db.connect() as conn:
            conn.execute(
                """
                UPDATE host_state
                SET initialized = 1, initialized_at = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id = 1
                """,
                (initialized_at,),
            )

    def update_component(self, component: str, value: bool) -> None:
        """Update a single host initialization component flag."""
        allowed = {"mvm_group_created", "sudoers_configured", "default_network_created"}
        if component not in allowed:
            raise ValueError(f"Unknown host state component: {component!r}")
        with self._db.connect() as conn:
            conn.execute(
                f"UPDATE host_state SET {component} = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
                (int(value),),
            )

    def reset_state(self) -> None:
        """Reset all host state flags to False (for mvm host reset)."""
        with self._db.connect() as conn:
            conn.execute(
                """
                UPDATE host_state SET
                    initialized = 0,
                    mvm_group_created = 0,
                    sudoers_configured = 0,
                    default_network_created = 0,
                    updated_at = CURRENT_TIMESTAMP
                WHERE id = 1
                """
            )

    def add_change(self, change: HostStateChange) -> None:
        """Record a host configuration change made during mvm host init."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO host_state_changes (
                    session_id, init_timestamp, setting, mechanism,
                    original_value, applied_value, reverted, reverted_at,
                    revert_mechanism, change_order, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                """,
                (
                    change.session_id,
                    change.init_timestamp,
                    change.setting,
                    change.mechanism,
                    change.original_value,
                    change.applied_value,
                    int(change.reverted),
                    change.reverted_at,
                    change.revert_mechanism,
                    change.change_order,
                ),
            )

    def list_changes(
        self, session_id: str | None = None, include_reverted: bool = True
    ) -> list[HostStateChange]:
        """Return host state changes, optionally filtered by session."""
        query = "SELECT * FROM host_state_changes"
        params: list[object] = []
        conditions: list[str] = []

        if session_id is not None:
            conditions.append("session_id = ?")
            params.append(session_id)
        if not include_reverted:
            conditions.append("reverted = 0")

        if conditions:
            query += " WHERE " + " AND ".join(conditions)
        query += " ORDER BY change_order ASC"

        with self._db.connect() as conn:
            rows = conn.execute(query, params).fetchall()
        return [HostStateChange(**dict(row)) for row in rows]

    def mark_change_reverted(
        self, change_id: int, reverted_at: str, revert_mechanism: str | None = None
    ) -> None:
        """Mark a single host change as reverted."""
        with self._db.connect() as conn:
            conn.execute(
                """
                UPDATE host_state_changes
                SET reverted = 1, reverted_at = ?, revert_mechanism = ?
                WHERE id = ?
                """,
                (reverted_at, revert_mechanism, change_id),
            )

    def revert_changes(self, session_id: str, reverted_at: str) -> list[HostStateChange]:
        """Mark all unreverted changes for a session as reverted (LIFO order)."""
        changes = self.list_changes(session_id=session_id, include_reverted=False)
        for change in reversed(changes):
            if change.id is not None:
                self.mark_change_reverted(change.id, reverted_at)
        return list(reversed(changes))
