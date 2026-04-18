"""Kernel database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.models.kernel import KernelItem


class KernelRepository:
    """Database operations for kernels."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get(self, kernel_id: str) -> KernelItem | None:
        """Return a kernel by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE id = ?", (kernel_id,)
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[KernelItem]:
        """Return all kernels whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM kernels WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [KernelItem(**dict(row)) for row in rows]

    def list_all(self) -> list[KernelItem]:
        """Return all kernels."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM kernels ORDER BY created_at"
            ).fetchall()
        return [KernelItem(**dict(row)) for row in rows]

    def upsert(self, kernel: KernelItem) -> None:
        """Insert or replace a kernel record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO kernels (
                    id, name, base_name, version, arch, type, path,
                    is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    base_name = excluded.base_name,
                    version = excluded.version,
                    arch = excluded.arch,
                    type = excluded.type,
                    path = excluded.path,
                    is_default = excluded.is_default,
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
                    kernel.created_at,
                    kernel.updated_at,
                ),
            )

    def delete(self, kernel_id: str) -> None:
        """Delete a kernel by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM kernels WHERE id = ?", (kernel_id,))

    def set_default(self, kernel_id: str) -> None:
        """Set one kernel as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE kernels SET is_default = 0")
            conn.execute(
                "UPDATE kernels SET is_default = 1 WHERE id = ?", (kernel_id,)
            )
            conn.execute("COMMIT")

    def get_default(self) -> KernelItem | None:
        """Return the default kernel entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE is_default = 1 LIMIT 1"
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))

    def get_by_name(self, name: str) -> KernelItem | None:
        """Return a kernel by its name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM kernels WHERE name = ? LIMIT 1", (name,)
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
                "SELECT * FROM kernels WHERE version = ? AND type = ? LIMIT 1",
                (version, type),
            ).fetchone()
        if row is None:
            return None
        return KernelItem(**dict(row))
