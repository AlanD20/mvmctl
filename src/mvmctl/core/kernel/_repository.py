"""Kernel database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._shared import Database
from mvmctl.models.kernel import KernelItem
from mvmctl.models.vm import VMInstanceItem


class KernelRepository:
    """Database operations for kernels."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def get(self, kernel_id: str) -> KernelItem | None:
        """Return a kernel by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE id = ? AND deleted_at IS NULL",
                (kernel_id,),
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[KernelItem]:
        """Return all kernels whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM kernels WHERE id LIKE ? AND deleted_at IS NULL",
                (f"{prefix}%",),
            ).fetchall()
        return [KernelItem(**dict(row)) for row in rows]

    def list_all(self) -> list[KernelItem]:
        """Return all non-deleted kernels."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM kernels WHERE deleted_at IS NULL ORDER BY created_at"
            ).fetchall()
        return [KernelItem(**dict(row)) for row in rows]

    def upsert(self, kernel: KernelItem) -> None:
        """Insert or replace a kernel record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO kernels (
                    id, name, base_name, version, arch, type, path,
                    is_default, is_present, created_at, updated_at, deleted_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    base_name = excluded.base_name,
                    version = excluded.version,
                    arch = excluded.arch,
                    type = excluded.type,
                    path = excluded.path,
                    is_default = excluded.is_default,
                    is_present = excluded.is_present,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    kernel.id,
                    kernel.name,
                    kernel.base_name,
                    kernel.version,
                    kernel.arch,
                    kernel.type,
                    kernel.path,
                    int(kernel.is_default),
                    int(kernel.is_present),
                    kernel.created_at,
                    kernel.updated_at,
                    kernel.deleted_at,
                ),
            )

    def soft_delete(self, kernel_id: str) -> None:
        """Soft-delete a kernel by setting deleted_at and is_present=0."""
        from datetime import datetime, timezone

        now = datetime.now(tz=timezone.utc).isoformat()
        with self._db.connect() as conn:
            conn.execute(
                "UPDATE kernels SET deleted_at = ?, is_present = 0 WHERE id = ?",
                (now, kernel_id),
            )

    def delete(self, kernel_id: str) -> None:
        """Hard-delete a kernel by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM kernels WHERE id = ?", (kernel_id,))

    def update_many_is_present(
        self, kernel_ids: list[str], is_present: bool
    ) -> None:
        """Bulk update is_present flag for multiple kernels."""
        if not kernel_ids:
            return
        placeholders = ",".join("?" * len(kernel_ids))
        with self._db.connect() as conn:
            conn.execute(
                f"UPDATE kernels SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN ({placeholders})",
                [int(is_present)] + list(kernel_ids),
            )

    def set_default(self, kernel_id: str) -> None:
        """Set one kernel as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "UPDATE kernels SET is_default = 0 WHERE deleted_at IS NULL"
            )
            conn.execute(
                "UPDATE kernels SET is_default = 1 WHERE id = ? AND deleted_at IS NULL",
                (kernel_id,),
            )
            conn.execute("COMMIT")

    def get_default(self) -> KernelItem | None:
        """Return the default kernel entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE is_default = 1 AND deleted_at IS NULL LIMIT 1"
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def get_by_name(self, name: str) -> KernelItem | None:
        """Return a kernel by its name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE name = ? AND deleted_at IS NULL LIMIT 1",
                (name,),
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def get_by_type(self, type: str) -> KernelItem | None:
        """Return a kernel by its version and type, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE type = ? AND deleted_at IS NULL LIMIT 1",
                (type,),
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def get_by_version_and_type(
        self, version: str, type: str
    ) -> KernelItem | None:
        """Return a kernel by its version and type, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE version = ? AND type = ? AND deleted_at IS NULL LIMIT 1",
                (version, type),
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def query_vms_by_kernel(self, kernel_id: str) -> list[VMInstanceItem]:
        """Return all VMs that reference the given kernel ID.

        Args:
            kernel_id: Full kernel ID to query.

        Returns:
            List of VMInstanceItem records referencing this kernel.
        """
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM vm_instances WHERE kernel_id = ?",
                (kernel_id,),
            ).fetchall()
        return [VMInstanceItem(**dict(row)) for row in rows]
