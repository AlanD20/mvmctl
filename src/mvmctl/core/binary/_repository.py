"""BinaryItem database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.models.binary import BinaryItem


class BinaryRepository:
    """Database operations for binaries."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def get(self, binary_id: str) -> BinaryItem | None:
        """Return a binary by its full 64-char ID, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM binaries WHERE id = ?", (binary_id,)
            ).fetchone()
        if row is None:
            return None
        return BinaryItem(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[BinaryItem]:
        """Return all binaries whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [BinaryItem(**dict(row)) for row in rows]

    def list_all(self) -> list[BinaryItem]:
        """Return all binaries."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries ORDER BY created_at"
            ).fetchall()
        return [BinaryItem(**dict(row)) for row in rows]

    def list_by_name(self, name: str) -> list[BinaryItem]:
        """Return all binaries with a given name."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries WHERE name = ? ORDER BY created_at",
                (name,),
            ).fetchall()
        return [BinaryItem(**dict(row)) for row in rows]

    def get_by_name_and_version(
        self, name: str, version: str
    ) -> BinaryItem | None:
        """Return a binary by its name and version, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM binaries WHERE name = ? AND version = ? LIMIT 1",
                (name, version),
            ).fetchone()
        if row is None:
            return None
        return BinaryItem(**dict(row))

    def upsert(self, binary: BinaryItem) -> None:
        """Insert or replace a binary record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO binaries (
                    id, name, version, full_version, ci_version, path,
                    is_default, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    version = excluded.version,
                    full_version = excluded.full_version,
                    ci_version = excluded.ci_version,
                    path = excluded.path,
                    is_default = excluded.is_default,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    binary.id,
                    binary.name,
                    binary.version,
                    binary.full_version,
                    binary.ci_version,
                    binary.path,
                    int(binary.is_default),
                    binary.created_at,
                    binary.updated_at,
                ),
            )

    def delete(self, binary_id: str) -> None:
        """Delete a binary by ID."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM binaries WHERE id = ?", (binary_id,))

    def delete_by_name_and_version(self, name: str, version: str) -> None:
        """Delete the binary row matching name AND version."""
        normalized = version.removeprefix("v")
        prefixed = f"v{normalized}"
        with self._db.connect() as conn:
            conn.execute(
                "DELETE FROM binaries WHERE name = ? AND (version = ? OR version = ?)",
                (name, normalized, prefixed),
            )

    def set_default(self, name: str, version: str, path: str) -> None:
        """Set a binary as default, clearing all others with the same name atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute(
                "UPDATE binaries SET is_default = 0 WHERE name = ?", (name,)
            )
            conn.execute(
                """
                UPDATE binaries SET is_default = 1, updated_at = CURRENT_TIMESTAMP
                WHERE name = ? AND version = ?
                """,
                (name, version),
            )
            conn.execute("COMMIT")

    def get_default(self, name: str) -> BinaryItem | None:
        """Return the default binary entry for a given name, or None."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM binaries WHERE name = ? AND is_default = 1 LIMIT 1",
                (name,),
            ).fetchone()
        if row is None:
            return None
        return BinaryItem(**dict(row))
