"""Database migration runner.

Manages schema migrations using SQL files in the migrations/ folder.
Tracks schema version via PRAGMA user_version.
Records migration history in db_migrations table.

Migration files follow the pattern: {version:03d}_{description}.sql
Example: 001_initial_schema.sql, 002_add_index.sql
"""

from __future__ import annotations

import re
import sqlite3
from datetime import datetime
from pathlib import Path


class MigrationError(Exception):
    """Raised when a migration fails."""


class MigrationRunner:
    """Manages database schema migrations.

    Uses PRAGMA user_version to track current schema version.
    Migration files are SQL files in the migrations/ folder.

    Usage:
        runner = MigrationRunner(
            db_path=Path("~/.cache/mvmctl/mvmdb.db"),
            migrations_dir=Path("src/mvmctl/db/migrations"),
        )
        applied = runner.migrate()
        print(f"Applied {applied} migrations")
    """

    def __init__(self, db_path: Path, migrations_dir: Path) -> None:
        self.db_path = db_path
        self.migrations_dir = migrations_dir

    def _extract_version(self, migration_file: Path) -> int:
        """Extract version number from filename like 001_initial_schema.sql."""
        match = re.match(r"^(\d+)_.*\.sql$", migration_file.name)
        if not match:
            raise MigrationError(
                f"Invalid migration filename: {migration_file.name}. "
                f"Expected format: '{{version:03d}}_{{description}}.sql'"
            )
        return int(match.group(1))

    def get_current_version(self) -> int:
        """Get current schema version from PRAGMA user_version.

        Returns:
            Current schema version (0 if database is new/empty).
        """
        with sqlite3.connect(self.db_path) as conn:
            result = conn.execute("PRAGMA user_version").fetchone()
            return result[0] if result else 0

    def get_pending_migrations(self) -> list[Path]:
        """Get list of migration files not yet applied.

        Returns:
            Sorted list of Path objects for pending migrations.

        Raises:
            MigrationError: If version sequence has gaps.
        """
        current_version = self.get_current_version()
        if not self.migrations_dir.exists():
            return []

        all_migrations = sorted(self.migrations_dir.glob("[0-9]*_*.sql"))

        if all_migrations:
            versions = [self._extract_version(m) for m in all_migrations]
            expected = list(range(1, max(versions) + 1))
            missing = set(expected) - set(versions)
            if missing:
                raise MigrationError(
                    f"Missing migration versions: {sorted(missing)}. "
                    f"Cannot have gaps in version sequence."
                )

        return [m for m in all_migrations if self._extract_version(m) > current_version]

    def _ensure_migrations_table(self, conn: sqlite3.Connection) -> None:
        """Ensure db_migrations tracking table exists."""
        conn.execute("""
            CREATE TABLE IF NOT EXISTS db_migrations (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                version INTEGER NOT NULL UNIQUE,
                name TEXT NOT NULL,
                applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                checksum TEXT
            )
        """)

    def migrate(self) -> int:
        """Run all pending migrations.

        Each migration SQL file is executed via conn.executescript().
        Migration history is recorded in db_migrations table.

        Returns:
            Number of migrations applied (0 if none pending).

        Raises:
            MigrationError: If a migration fails or version sequence has gaps.
        """
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        pending = self.get_pending_migrations()
        if not pending:
            return 0

        applied_count = 0

        with sqlite3.connect(self.db_path) as conn:
            conn.execute("PRAGMA foreign_keys = ON")
            conn.execute("PRAGMA busy_timeout = 5000")
            self._ensure_migrations_table(conn)

            for migration_file in pending:
                version = self._extract_version(migration_file)
                sql = migration_file.read_text()

                try:
                    conn.executescript(sql)
                except sqlite3.Error as exc:
                    raise MigrationError(
                        f"Migration {migration_file.name} (version {version}) failed: {exc}"
                    ) from exc

                conn.execute(
                    "INSERT INTO db_migrations (version, name, applied_at) VALUES (?, ?, ?)",
                    (version, migration_file.name, datetime.now().isoformat()),
                )
                applied_count += 1

        return applied_count

    def validate_migrations(self) -> list[str]:
        """Validate all migration files without applying them.

        Returns:
            List of validation error messages (empty if all valid).
        """
        errors: list[str] = []

        if not self.migrations_dir.exists():
            return [f"Migrations directory not found: {self.migrations_dir}"]

        all_migrations = sorted(self.migrations_dir.glob("[0-9]*_*.sql"))
        versions: list[int] = []

        for m in all_migrations:
            try:
                versions.append(self._extract_version(m))
            except MigrationError as exc:
                errors.append(str(exc))

        if versions:
            expected = list(range(1, max(versions) + 1))
            missing = set(expected) - set(versions)
            if missing:
                errors.append(f"Missing migration versions: {sorted(missing)}")

        return errors

    def rollback(self, steps: int = 1) -> None:
        """Rollback last N migrations.

        Not implemented in v1. Restore from backup or create a new migration
        to reverse changes.

        Raises:
            NotImplementedError: Always raised in v1.
        """
        raise NotImplementedError(
            "Rollback not implemented in v1. "
            "Restore from backup or create a new migration to reverse changes."
        )
