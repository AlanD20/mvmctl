"""Database class for core layer - connection management and migrations."""

from __future__ import annotations

import re
import sqlite3
from contextlib import closing, contextmanager
from datetime import datetime
from pathlib import Path
from typing import Generator

from mvmctl.exceptions import MigrationError
from mvmctl.utils.common import CacheUtils


class Database:
    """Database connection manager with migration support.

    Provides connection management and schema migrations for domain repositories.
    Each Database instance is tied to a specific database file path.

    Example:
        db = Database()  # Uses default path
        with db.connect() as conn:
            conn.execute("SELECT * FROM vm_instances")

        # Run migrations
        db.migrate()
    """

    def __init__(self, db_path: Path | str | None = None) -> None:
        """Initialize Database with optional custom path.

        Args:
            db_path: Custom database path. Uses default if not provided.
        """
        self._db_path = (
            Path(db_path) if db_path else CacheUtils.get_mvm_db_path()
        )
        self._db_path.parent.mkdir(parents=True, exist_ok=True)

    @property
    def db_path(self) -> Path:
        """Return the database file path."""
        return self._db_path

    @contextmanager
    def connect(self) -> Generator[sqlite3.Connection, None, None]:
        """Context manager for database connections.

        Sets required PRAGMAs:
        - foreign_keys = ON
        - journal_mode = WAL
        - synchronous = NORMAL
        - busy_timeout = 5000

        Yields:
            sqlite3.Connection with row_factory set to sqlite3.Row
        """
        conn = sqlite3.connect(
            self._db_path,
            check_same_thread=False,
            isolation_level=None,
        )
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute("PRAGMA journal_mode = WAL")
        conn.execute("PRAGMA synchronous = NORMAL")
        conn.execute("PRAGMA busy_timeout = 5000")
        conn.execute("PRAGMA wal_autocheckpoint = 1000")
        conn.execute("PRAGMA cache_size = -64000")

        try:
            yield conn
        finally:
            conn.close()

    def _get_migrations_dir(self) -> Path:
        """Return the migrations directory bundled with the package."""
        import mvmctl.db

        return Path(mvmctl.db.__file__).parent / "migrations"

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
        with closing(sqlite3.connect(self._db_path)) as conn:
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
        migrations_dir = self._get_migrations_dir()

        if not migrations_dir.exists():
            return []

        all_migrations = sorted(migrations_dir.glob("[0-9]*_*.sql"))

        if all_migrations:
            versions = [self._extract_version(m) for m in all_migrations]
            expected = list(range(1, max(versions) + 1))
            missing = set(expected) - set(versions)
            if missing:
                raise MigrationError(
                    f"Missing migration versions: {sorted(missing)}. "
                    f"Cannot have gaps in version sequence."
                )

        return [
            m
            for m in all_migrations
            if self._extract_version(m) > current_version
        ]

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
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        pending = self.get_pending_migrations()
        if not pending:
            return 0

        applied_count = 0

        with closing(sqlite3.connect(self._db_path)) as conn:
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
                conn.commit()
                applied_count += 1

        return applied_count

    def validate_migrations(self) -> list[str]:
        """Validate all migration files without applying them.

        Returns:
            List of validation error messages (empty if all valid).
        """
        errors: list[str] = []
        migrations_dir = self._get_migrations_dir()

        if not migrations_dir.exists():
            return [f"Migrations directory not found: {migrations_dir}"]

        all_migrations = sorted(migrations_dir.glob("[0-9]*_*.sql"))
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
