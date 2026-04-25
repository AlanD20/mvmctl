"""SSH key database operations - Repository Pattern implementation."""

from __future__ import annotations

from mvmctl.core._internal._db import Database
from mvmctl.models.key import SSHKeyItem


class KeyRepository:
    """Database operations for SSH keys."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    @property
    def db(self) -> Database:
        """Return the database instance."""
        return self._db

    def get(self, key_id: str) -> SSHKeyItem | None:
        """Return an SSH key by its ID (fingerprint), or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM ssh_keys WHERE id = ?", (key_id,)
            ).fetchone()
        if row is None:
            return None
        return SSHKeyItem(**dict(row))

    def get_by_name(self, name: str) -> SSHKeyItem | None:
        """Return an SSH key by name, or None if not found."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM ssh_keys WHERE name = ?", (name,)
            ).fetchone()
        if row is None:
            return None
        return SSHKeyItem(**dict(row))

    def find_by_prefix(self, prefix: str) -> list[SSHKeyItem]:
        """Return all SSH keys whose ID starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE id LIKE ?", (f"{prefix}%",)
            ).fetchall()
        return [SSHKeyItem(**dict(row)) for row in rows]

    def find_by_fingerprint_prefix(self, prefix: str) -> list[SSHKeyItem]:
        """Return all SSH keys whose fingerprint starts with prefix."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE fingerprint LIKE ?",
                (f"{prefix}%",),
            ).fetchall()
        return [SSHKeyItem(**dict(row)) for row in rows]

    def list_all(self) -> list[SSHKeyItem]:
        """Return all SSH keys."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys ORDER BY created_at"
            ).fetchall()
        return [SSHKeyItem(**dict(row)) for row in rows]

    def upsert(self, key: SSHKeyItem) -> None:
        """Insert or replace an SSH key record."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO ssh_keys (
                    id, name, fingerprint, algorithm, comment,
                    private_key_path, public_key_path, is_default, is_present, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    name = excluded.name,
                    fingerprint = excluded.fingerprint,
                    algorithm = excluded.algorithm,
                    comment = excluded.comment,
                    private_key_path = excluded.private_key_path,
                    public_key_path = excluded.public_key_path,
                    is_default = excluded.is_default,
                    is_present = excluded.is_present,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (
                    key.id,
                    key.name,
                    key.fingerprint,
                    key.algorithm,
                    key.comment,
                    key.private_key_path,
                    key.public_key_path,
                    int(key.is_default),
                    int(key.is_present),
                    key.created_at,
                    key.updated_at,
                ),
            )

    def update_many_is_present(
        self, key_ids: list[str], is_present: bool
    ) -> None:
        """Bulk update is_present flag for multiple keys."""
        if not key_ids:
            return
        placeholders = ",".join(["?"] * len(key_ids))
        with self._db.connect() as conn:
            conn.execute(
                f"""UPDATE ssh_keys SET is_present = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id IN ({placeholders})""",
                [int(is_present)] + list(key_ids),
            )

    def delete(self, key_id: str) -> None:
        """Delete an SSH key by ID. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM ssh_keys WHERE id = ?", (key_id,))

    def delete_by_name(self, name: str) -> None:
        """Delete an SSH key by name. No-op if not found."""
        with self._db.connect() as conn:
            conn.execute("DELETE FROM ssh_keys WHERE name = ?", (name,))

    def set_default(self, key_id: str) -> None:
        """Set one SSH key as default, clearing all others atomically."""
        with self._db.connect() as conn:
            conn.execute("BEGIN")
            conn.execute("UPDATE ssh_keys SET is_default = 0")
            conn.execute(
                "UPDATE ssh_keys SET is_default = 1 WHERE id = ?", (key_id,)
            )
            conn.execute("COMMIT")

    def get_default(self) -> SSHKeyItem | None:
        """Return the default SSH key entry, or None if not set."""
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM ssh_keys WHERE is_default = 1 LIMIT 1"
            ).fetchone()
        if row is None:
            return None
        return SSHKeyItem(**dict(row))

    def get_defaults(self) -> list[SSHKeyItem]:
        """Return all SSH keys marked as default."""
        with self._db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM ssh_keys WHERE is_default = 1 ORDER BY created_at"
            ).fetchall()
        return [SSHKeyItem(**dict(row)) for row in rows]

    def clear_defaults(self) -> None:
        """Clear all default SSH keys."""
        with self._db.connect() as conn:
            conn.execute("UPDATE ssh_keys SET is_default = 0")
