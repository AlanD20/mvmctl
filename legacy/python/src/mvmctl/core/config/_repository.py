"""Settings repository — database operations for user_settings table."""

from __future__ import annotations

import json
from typing import Any

from mvmctl.core._shared._db import Database, _graceful_read


class SettingsRepository:
    """
    Database operations for user config overrides.

    Uses category.key namespace:
    - category: 'defaults.vm', 'defaults.network', 'defaults.image', etc.
    - key: 'vcpu_count', 'subnet', 'arch', etc.
    """

    def __init__(self, db: Database) -> None:
        self._db = db

    @_graceful_read(default=None)
    def get(self, category: str, key: str) -> Any | None:
        """
        Get a setting value by category and key.

        Returns:
            The parsed JSON value, or None if not found.

        """
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT value FROM user_settings WHERE category = ? AND key = ?",
                (category, key),
            ).fetchone()
        if row is None:
            return None
        return json.loads(row["value"])

    def set(self, category: str, key: str, value: Any) -> None:
        """Set a setting value."""
        with self._db.connect() as conn:
            conn.execute(
                """
                INSERT INTO user_settings (category, key, value, updated_at)
                VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(category, key) DO UPDATE SET
                    value = excluded.value,
                    updated_at = CURRENT_TIMESTAMP
                """,
                (category, key, json.dumps(value)),
            )

    def delete(self, category: str, key: str) -> bool:
        """Delete a setting. Returns True if a row was deleted."""
        with self._db.connect() as conn:
            cursor = conn.execute(
                "DELETE FROM user_settings WHERE category = ? AND key = ?",
                (category, key),
            )
            return cursor.rowcount > 0

    def delete_by_category(self, category: str) -> int:
        """Delete all settings in a category. Returns number of rows deleted."""
        with self._db.connect() as conn:
            cursor = conn.execute(
                "DELETE FROM user_settings WHERE category = ?",
                (category,),
            )
            return cursor.rowcount

    def delete_all(self) -> int:
        """Delete ALL user settings. Returns number of rows deleted."""
        with self._db.connect() as conn:
            cursor = conn.execute("DELETE FROM user_settings")
            return cursor.rowcount

    @_graceful_read(default=0)
    def count(self) -> int:
        """Return total count of all user settings."""
        with self._db.connect() as conn:
            result = conn.execute(
                "SELECT COUNT(*) FROM user_settings"
            ).fetchone()
        return result[0] if result else 0

    @_graceful_read(factory=dict)
    def list_by_category(
        self, category: str | None = None
    ) -> dict[str, dict[str, Any]]:
        """
        List all settings, optionally filtered by category.

        Returns:
            Nested dict: {category: {key: value}}

        """
        query = "SELECT category, key, value FROM user_settings"
        params: tuple[Any, ...] = ()
        if category is not None:
            query += " WHERE category = ?"
            params = (category,)
        query += " ORDER BY category, key"

        with self._db.connect() as conn:
            rows = conn.execute(query, params).fetchall()

        result: dict[str, dict[str, Any]] = {}
        for row in rows:
            cat = row["category"]
            if cat not in result:
                result[cat] = {}
            result[cat][row["key"]] = json.loads(row["value"])
        return result
