"""Host database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._shared._db import Database, _graceful_read
from mvmctl.models import HostStateChangeItem, HostStateItem


class HostRepository:
    """Database operations for host state."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    @_graceful_read(default=0)
    def count(self) -> int:
        """Return total count of all host state changes."""
        with self._db.connect() as conn:
            result = conn.execute(
                "SELECT COUNT(*) FROM host_state_changes"
            ).fetchone()
        return result[0] if result else 0

    @_graceful_read(default=None)
    def get_state(self) -> HostStateItem | None:
        """Return the singleton host state row, or None if not yet initialized."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM host_state WHERE id = 1"
            ).fetchone()
        if row is None:
            return None
        return HostStateItem(**dict(row))

    def initialize_state(self) -> HostStateItem:
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
        allowed = {
            "mvm_group_created",
            "sudoers_configured",
            "default_network_created",
        }
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

    def save_capacity(
        self,
        hostname: str,
        cpu_model: str,
        cpu_vendor: str,
        cpu_cores: int,
        cpu_architecture: str,
        numa_nodes: int,
        memory_total_mib: int,
        storage_total_bytes: int,
        kernel_version: str,
        os_release: str,
        pid_max: int,
        fd_max: int,
        conntrack_max: int,
        tap_devices_max: int,
        ip_local_port_range: tuple[int, int],
        detected_at: str,
        cpu_has_vmx: bool = False,
        cpu_hypervisor: bool = False,
        nested_virt_available: bool = False,
        ept_available: bool = False,
        hugepage_count_2mb: int = 0,
        ksm_disabled: bool = True,
        cgroup_version: int = 1,
        swap_total_mib: int = 0,
        kernel_minimum_met: bool = False,
    ) -> None:
        """Upsert host capacity detection results into host_state row id=1.

        Updates capacity columns while preserving existing init-state columns.
        If the row doesn't exist yet (pre-init detection), it is inserted.
        """
        port_range_str = f"{ip_local_port_range[0]},{ip_local_port_range[1]}"
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            try:
                # Ensure row exists (singleton id=1)
                conn.execute(
                    """
                    INSERT OR IGNORE INTO host_state
                    (id, initialized, initialized_at, updated_at)
                    VALUES (1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
                    """
                )
                conn.execute(
                    """
                    UPDATE host_state SET
                        hostname = ?,
                        cpu_model = ?,
                        cpu_vendor = ?,
                        cpu_cores = ?,
                        cpu_architecture = ?,
                        numa_nodes = ?,
                        memory_total_mib = ?,
                        storage_total_bytes = ?,
                        kernel_version = ?,
                        os_release = ?,
                        pid_max = ?,
                        fd_max = ?,
                        conntrack_max = ?,
                        tap_devices_max = ?,
                        ip_local_port_range = ?,
                        detected_at = ?,
                        cpu_has_vmx = ?,
                        cpu_hypervisor = ?,
                        nested_virt_available = ?,
                        ept_available = ?,
                        hugepage_count_2mb = ?,
                        ksm_disabled = ?,
                        cgroup_version = ?,
                        swap_total_mib = ?,
                        kernel_minimum_met = ?,
                        updated_at = CURRENT_TIMESTAMP
                    WHERE id = 1
                    """,
                    (
                        hostname,
                        cpu_model,
                        cpu_vendor,
                        cpu_cores,
                        cpu_architecture,
                        numa_nodes,
                        memory_total_mib,
                        storage_total_bytes,
                        kernel_version,
                        os_release,
                        pid_max,
                        fd_max,
                        conntrack_max,
                        tap_devices_max,
                        port_range_str,
                        detected_at,
                        int(cpu_has_vmx),
                        int(cpu_hypervisor),
                        int(nested_virt_available),
                        int(ept_available),
                        hugepage_count_2mb,
                        int(ksm_disabled),
                        cgroup_version,
                        swap_total_mib,
                        int(kernel_minimum_met),
                    ),
                )
                conn.execute("COMMIT")
            except Exception:
                conn.execute("ROLLBACK")
                raise

    def add_change(self, change: HostStateChangeItem) -> None:
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

    def add_changes(self, changes: list[HostStateChangeItem]) -> None:
        """Bulk insert host state changes atomically in a single transaction."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            try:
                for change in changes:
                    conn.execute(
                        """
                        INSERT INTO host_state_changes (
                            session_id, init_timestamp, setting, mechanism,
                            original_value, applied_value, reverted, reverted_at,
                            revert_mechanism, change_order, created_at
                        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
                            change.created_at,
                        ),
                    )
                conn.execute("COMMIT")
            except Exception:
                conn.execute("ROLLBACK")
                raise

    def delete_changes_except_session(self, session_id: str) -> None:
        """Delete all host state changes except those for the given session."""
        with self._db.connect() as conn:
            conn.execute(
                "DELETE FROM host_state_changes WHERE session_id != ?",
                (session_id,),
            )

    @_graceful_read(factory=list)
    def list_changes(
        self, session_id: str | None = None, include_reverted: bool = True
    ) -> list[HostStateChangeItem]:
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
        return [HostStateChangeItem(**dict(row)) for row in rows]

    def mark_change_reverted(
        self,
        change_id: int,
        reverted_at: str,
        revert_mechanism: str | None = None,
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

    def revert_changes(
        self, session_id: str, reverted_at: str
    ) -> list[HostStateChangeItem]:
        """Mark all unreverted changes for a session as reverted (LIFO order)."""
        changes = self.list_changes(
            session_id=session_id, include_reverted=False
        )
        for change in reversed(changes):
            if change.id is not None:
                self.mark_change_reverted(change.id, reverted_at)
        return list(reversed(changes))
