"""Tests for Database migration runner (core/_shared/_db.py).

Verifies:
- get_current_version() — reads PRAGMA user_version
- get_pending_migrations() — detects pending migrations
- migrate() — applies migrations with snapshot support
- validate_migrations() — checks without applying
- rollback() — reverts migration using snapshots
- Error handling — corrupt migration, missing file, version gaps
"""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from mvmctl.core._shared._db import Database
from mvmctl.exceptions import MigrationError

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def migrations_dir(tmp_path: Path) -> Path:
    """Create a temporary migrations directory."""
    d = tmp_path / "migrations"
    d.mkdir(parents=True, exist_ok=True)
    return d


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    """Temporary database path."""
    return tmp_path / "test.db"


@pytest.fixture
def db(db_path: Path, migrations_dir: Path) -> Database:
    """Create a Database instance for testing.

    We override the migrations dir by monkeypatching _get_migrations_dir.
    """
    d = Database(db_path=db_path)

    # Monkey-patch the migrations dir lookup
    d._get_migrations_dir = lambda: migrations_dir  # type: ignore[method-assign]
    return d


# ---------------------------------------------------------------------------
# get_current_version
# ---------------------------------------------------------------------------


class TestGetCurrentVersion:
    """Tests for get_current_version()."""

    def test_returns_zero_for_new_db(self, db_path: Path) -> None:
        db_path.parent.mkdir(parents=True, exist_ok=True)
        db_path.touch()
        d = Database(db_path=db_path)
        assert d.get_current_version() == 0

    def test_returns_version_after_migration(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        db.migrate()
        assert db.get_current_version() == 1


# ---------------------------------------------------------------------------
# get_pending_migrations
# ---------------------------------------------------------------------------


class TestGetPendingMigrations:
    """Tests for get_pending_migrations()."""

    def test_returns_empty_when_no_migrations_dir(self, db_path: Path) -> None:
        d = Database(db_path=db_path)
        original = d._get_migrations_dir
        d._get_migrations_dir = lambda: Path("/nonexistent/migrations")  # type: ignore[method-assign]
        assert d.get_pending_migrations() == []
        d._get_migrations_dir = original  # type: ignore[method-assign]

    def test_returns_all_when_version_is_zero(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "SELECT 1;\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_more.sql").write_text(
            "SELECT 2;\nPRAGMA user_version = 2;\n"
        )
        pending = db.get_pending_migrations()
        assert len(pending) == 2

    def test_skips_already_applied(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_more.sql").write_text(
            "CREATE TABLE bar (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 2;\n"
        )
        db.migrate()
        pending = db.get_pending_migrations()
        assert len(pending) == 0

    def test_raises_on_version_gap(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "SELECT 1;\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "003_skip.sql").write_text(
            "SELECT 3;\nPRAGMA user_version = 3;\n"
        )
        with pytest.raises(MigrationError, match="Missing migration versions"):
            db.get_pending_migrations()


# ---------------------------------------------------------------------------
# migrate
# ---------------------------------------------------------------------------


class TestMigrate:
    """Tests for migrate()."""

    def test_returns_zero_when_nothing_pending(self, db: Database) -> None:
        assert db.migrate() == 0

    def test_applies_single_migration(
        self, db: Database, migrations_dir: Path, db_path: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE images (id TEXT PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        applied = db.migrate()
        assert applied == 1
        assert db.get_current_version() == 1

    def test_applies_multiple_migrations_in_order(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_add.sql").write_text(
            "CREATE TABLE bar (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 2;\n"
        )
        applied = db.migrate()
        assert applied == 2
        assert db.get_current_version() == 2

    def test_idempotent_when_run_twice(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        db.migrate()
        applied = db.migrate()
        assert applied == 0

    def test_raises_migration_error_on_bad_sql(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_bad.sql").write_text(
            "THIS IS NOT SQL;\nPRAGMA user_version = 1;\n"
        )
        with pytest.raises(MigrationError, match="001_bad.sql"):
            db.migrate()

    def test_records_migration_in_db_migrations_table(
        self, db: Database, migrations_dir: Path, db_path: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        db.migrate()
        with sqlite3.connect(db_path) as conn:
            conn.row_factory = sqlite3.Row
            rows = conn.execute(
                "SELECT version, name FROM db_migrations"
            ).fetchall()
        assert len(rows) == 1
        assert rows[0]["version"] == 1
        assert rows[0]["name"] == "001_init.sql"

    def test_creates_parent_directories(
        self, migrations_dir: Path, tmp_path: Path
    ) -> None:
        db_path = tmp_path / "deep" / "nested" / "test.db"
        d = Database(db_path=db_path)
        d._get_migrations_dir = lambda: migrations_dir  # type: ignore[method-assign]
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        d.migrate()
        assert db_path.exists()

    def test_takes_snapshot_for_version_gt_one(
        self, db: Database, migrations_dir: Path, db_path: Path
    ) -> None:
        """Snapshot should be created for migrations with version > 1."""
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_add.sql").write_text(
            "CREATE TABLE bar (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 2;\n"
        )
        db.migrate()

        # Snapshot for version 2 should exist
        snap_path = db_path.with_name(f"{db_path.name}.v2.snap")
        assert snap_path.exists()


# ---------------------------------------------------------------------------
# validate_migrations
# ---------------------------------------------------------------------------


class TestValidateMigrations:
    """Tests for validate_migrations()."""

    def test_returns_empty_for_valid_migrations(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\n")
        (migrations_dir / "002_more.sql").write_text("SELECT 2;\n")
        assert db.validate_migrations() == []

    def test_returns_error_for_missing_dir(
        self, db_path: Path, tmp_path: Path
    ) -> None:
        d = Database(db_path=db_path)
        original = d._get_migrations_dir
        d._get_migrations_dir = lambda: tmp_path / "nonexistent"  # type: ignore[method-assign]
        errors = d.validate_migrations()
        assert len(errors) >= 1
        assert "not found" in errors[0].lower()
        d._get_migrations_dir = original  # type: ignore[method-assign]

    def test_returns_error_for_version_gap(
        self, db: Database, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\n")
        (migrations_dir / "003_skip.sql").write_text("SELECT 3;\n")
        errors = db.validate_migrations()
        assert any("missing" in e.lower() for e in errors)

    def test_returns_error_for_invalid_filename(
        self, db: Database, migrations_dir: Path
    ) -> None:
        # File matches glob [0-9]*_*.sql but not regex ^(\d+)_.*\.sql$
        (migrations_dir / "001a_test.sql").write_text("SELECT 1;\n")
        errors = db.validate_migrations()
        assert any("invalid" in e.lower() for e in errors)


# ---------------------------------------------------------------------------
# _extract_version
# ---------------------------------------------------------------------------


class TestExtractVersion:
    """Tests for _extract_version()."""

    def test_extracts_version_from_valid_filename(self, tmp_path: Path) -> None:
        f = tmp_path / "042_some_migration.sql"
        d = Database(db_path=tmp_path / "test.db")
        assert d._extract_version(f) == 42

    def test_raises_on_invalid_filename(self, tmp_path: Path) -> None:
        f = tmp_path / "invalid_name.sql"
        d = Database(db_path=tmp_path / "test.db")
        with pytest.raises(MigrationError, match="Invalid migration filename"):
            d._extract_version(f)


# ---------------------------------------------------------------------------
# rollback
# ---------------------------------------------------------------------------


class TestRollback:
    """Tests for rollback()."""

    def test_raises_when_no_migrations_applied(
        self, db: Database, db_path: Path
    ) -> None:
        # Ensure db_migrations table exists (empty) before calling rollback
        from contextlib import closing

        with closing(sqlite3.connect(str(db_path))) as conn:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS db_migrations (
                    version INTEGER PRIMARY KEY,
                    name TEXT NOT NULL,
                    applied_at TEXT NOT NULL DEFAULT (datetime('now')),
                    checksum TEXT,
                    snapshot_path TEXT
                )
            """)
        with pytest.raises(MigrationError, match="No migrations to roll back"):
            db.rollback()

    def test_raises_on_invalid_steps(self, db: Database) -> None:
        with pytest.raises(ValueError, match="steps must be >= 1"):
            db.rollback(steps=0)
