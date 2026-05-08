"""Volume database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._shared._db import Database, _graceful_read
from mvmctl.models import VolumeItem


class VolumeRepository:
    """Database operations for volumes."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    @_graceful_read(default=None)
    def get(self, volume_id: str) -> VolumeItem | None:
        """Return a volume by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM volumes WHERE id = ?", (volume_id,)
            ).fetchone()
        if row is None:
            return None
        return VolumeItem(**dict(row))

    @_graceful_read(factory=list)
    def find_by_prefix(self, prefix: str) -> list[VolumeItem]:
        """Return all volumes whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM volumes WHERE id LIKE ?",
                (f"{prefix}%",),
            ).fetchall()
        return [VolumeItem(**dict(row)) for row in rows]

    @_graceful_read(default=None)
    def get_by_name(self, name: str) -> VolumeItem | None:
        """Return a volume by its name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM volumes WHERE name = ?", (name,)
            ).fetchone()
        if row is None:
            return None
        return VolumeItem(**dict(row))

    @_graceful_read(factory=list)
    def list_all(self) -> list[VolumeItem]:
        """Return all volumes."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM volumes ORDER BY created_at"
            ).fetchall()
        return [VolumeItem(**dict(row)) for row in rows]

    def upsert(self, volume: VolumeItem) -> None:
        """Insert or replace a volume record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO volumes (
                    id, name, size_bytes, format, path, status, vm_id,
                    created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    size_bytes = excluded.size_bytes,
                    format = excluded.format,
                    path = excluded.path,
                    status = excluded.status,
                    vm_id = excluded.vm_id,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    volume.id,
                    volume.name,
                    volume.size_bytes,
                    volume.format,
                    volume.path,
                    volume.status,
                    volume.vm_id,
                    volume.created_at,
                    volume.updated_at,
                ),
            )

    def delete(self, volume_id: str) -> None:
        """Delete a volume by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM volumes WHERE id = ?", (volume_id,))

    def find_by_ids(self, volume_ids: list[str]) -> list[VolumeItem]:
        """Return all volumes matching the given IDs.

        Uses a single ``WHERE id IN (?,?,...)`` query.

        Args:
            volume_ids: List of full 64-char volume IDs.

        Returns:
            List of matching VolumeItem records.

        """
        if not volume_ids:
            return []
        placeholders = ",".join("?" for _ in volume_ids)
        with self._db.connect() as conn:
            rows = conn.execute(
                f"SELECT * FROM volumes WHERE id IN ({placeholders})",
                volume_ids,
            ).fetchall()
        return [VolumeItem(**dict(row)) for row in rows]

    @_graceful_read(default=0)
    def count(self) -> int:
        """Return the total number of volumes."""
        with self._db.connect() as conn:
            row = conn.execute("SELECT COUNT(*) FROM volumes").fetchone()
        return row[0] if row else 0
