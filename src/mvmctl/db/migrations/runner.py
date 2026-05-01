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
from contextlib import closing
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
        with closing(sqlite3.connect(self.db_path)) as conn:
            with conn:
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
                checksum TEXT,
                snapshot_path TEXT
            )
        """)
        # Migrate existing tables that don't have snapshot_path
        try:
            conn.execute(
                "ALTER TABLE db_migrations ADD COLUMN snapshot_path TEXT"
            )
        except sqlite3.OperationalError:
            pass  # Column already exists

    def _take_snapshot(self, version: int) -> Path:
        """Create an online snapshot of the database before a migration.

        Snapshot is saved as a ``.snap`` file next to the database:
        ``{db_path}.v{version}.snap``.  Uses SQLite's backup API to create a
        transactionally consistent snapshot while other connections may be
        active.  Only one snapshot is kept per version — a re-migration
        overwrites the previous snapshot for that version.

        Args:
            version: The migration version this snapshot is for.

        Returns:
            Path to the snapshot file.
        """
        snap_path = self.db_path.with_name(
            f"{self.db_path.name}.v{version}.snap",
        )

        # SQLite online backup — safe even with concurrent connections
        with closing(sqlite3.connect(self.db_path)) as src:
            with closing(sqlite3.connect(snap_path)) as dst:
                src.backup(dst)

        return snap_path

    def _restore_from_snapshot(self, snapshot_path: Path) -> None:
        """Restore the database from a snapshot.

        Uses SQLite's backup API to restore from a snapshot file.
        The backup API is safe even with concurrent connections.

        Args:
            snapshot_path: Path to the snapshot file.

        Raises:
            MigrationError: If the snapshot file does not exist or restore fails.
        """
        if not snapshot_path.exists():
            raise MigrationError(f"Snapshot not found: {snapshot_path}")

        try:
            with closing(sqlite3.connect(snapshot_path)) as src:
                with closing(sqlite3.connect(self.db_path)) as dst:
                    src.backup(dst)
        except sqlite3.Error as exc:
            raise MigrationError(
                f"Failed to restore from snapshot: {exc}"
            ) from exc

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

        with closing(sqlite3.connect(self.db_path)) as conn:
            conn.execute("PRAGMA foreign_keys = ON")
            conn.execute("PRAGMA busy_timeout = 5000")
            self._ensure_migrations_table(conn)

            for migration_file in pending:
                version = self._extract_version(migration_file)
                sql = migration_file.read_text()

                # Take online snapshot before applying migration
                snapshot_path = self._take_snapshot(version)

                try:
                    conn.executescript(sql)
                except sqlite3.Error as exc:
                    raise MigrationError(
                        f"Migration {migration_file.name} (version {version}) failed: {exc}"
                    ) from exc

                conn.execute(
                    "INSERT INTO db_migrations (version, name, applied_at, snapshot_path) VALUES (?, ?, ?, ?)",
                    (
                        version,
                        migration_file.name,
                        datetime.now().isoformat(),
                        str(snapshot_path),
                    ),
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
        """Rollback the last N migrations by restoring from snapshots.

        Finds the snapshot taken before the first rolled-back migration,
        restores the database to that state, and removes the rolled-back
        migration records.

        Args:
            steps: Number of migrations to roll back (default: 1).

        Raises:
            MigrationError: If no snapshot is available or rollback fails.
            ValueError: If steps is less than 1.
        """
        if steps < 1:
            raise ValueError("steps must be >= 1")

        with closing(sqlite3.connect(self.db_path)) as conn:
            conn.execute("PRAGMA foreign_keys = ON")

            # Get the last N applied migrations
            rows = conn.execute(
                "SELECT version, snapshot_path FROM db_migrations ORDER BY version DESC LIMIT ?",
                (steps,),
            ).fetchall()

            if not rows:
                raise MigrationError("No migrations to roll back")

            if len(rows) < steps:
                raise MigrationError(
                    f"Cannot roll back {steps} migrations: only {len(rows)} applied"
                )

            # Find the snapshot from the oldest rolled-back migration
            # We need to restore to the state BEFORE this migration
            oldest_rollback = rows[-1]
            target_version = oldest_rollback["version"] - 1

            # Find the snapshot to restore from
            # If target_version is 0, we restore from the snapshot of version 1
            # (which was taken before migration 1 was applied)
            snapshot_row = conn.execute(
                "SELECT snapshot_path FROM db_migrations WHERE version = ?",
                (oldest_rollback["version"],),
            ).fetchone()

            if not snapshot_row or not snapshot_row["snapshot_path"]:
                raise MigrationError(
                    f"No snapshot available for rollback to version {target_version}. "
                    "Snapshots were not taken for these migrations."
                )

            snapshot_path = Path(snapshot_row["snapshot_path"])

        # Restore from snapshot (outside the read transaction)
        self._restore_from_snapshot(snapshot_path)

        # Update migration tracking
        with closing(sqlite3.connect(self.db_path)) as conn:
            conn.execute("PRAGMA foreign_keys = ON")

            # Remove rolled-back migration records
            min_version = rows[-1]["version"]
            conn.execute(
                "DELETE FROM db_migrations WHERE version >= ?",
                (min_version,),
            )

            # Update user_version
            conn.execute(f"PRAGMA user_version = {target_version}")
            conn.commit()
