"""Tests for MigrationRunner."""

from __future__ import annotations

import sqlite3
from contextlib import closing
from pathlib import Path

import pytest

from mvmctl.db.migrations.runner import MigrationError, MigrationRunner


@pytest.fixture
def migrations_dir(tmp_path: Path) -> Path:
    """Create a temporary migrations directory."""
    d = tmp_path / "migrations"
    d.mkdir()
    return d


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    """Temporary database path."""
    return tmp_path / "test.db"


@pytest.fixture
def runner(db_path: Path, migrations_dir: Path) -> MigrationRunner:
    return MigrationRunner(db_path=db_path, migrations_dir=migrations_dir)


class TestGetCurrentVersion:
    def test_returns_zero_for_new_db(self, runner: MigrationRunner, db_path: Path) -> None:
        db_path.parent.mkdir(parents=True, exist_ok=True)
        assert runner.get_current_version() == 0

    def test_returns_version_after_migration(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        runner.migrate()
        assert runner.get_current_version() == 1


class TestGetPendingMigrations:
    def test_returns_empty_when_no_migrations_dir(self, db_path: Path, tmp_path: Path) -> None:
        runner = MigrationRunner(db_path=db_path, migrations_dir=tmp_path / "nonexistent")
        assert runner.get_pending_migrations() == []

    def test_returns_all_when_version_is_zero(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\nPRAGMA user_version = 1;\n")
        (migrations_dir / "002_more.sql").write_text("SELECT 2;\nPRAGMA user_version = 2;\n")
        pending = runner.get_pending_migrations()
        assert len(pending) == 2

    def test_skips_already_applied(self, runner: MigrationRunner, migrations_dir: Path) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_more.sql").write_text(
            "CREATE TABLE bar (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 2;\n"
        )
        runner.migrate()
        pending = runner.get_pending_migrations()
        assert len(pending) == 0

    def test_raises_on_version_gap(self, runner: MigrationRunner, migrations_dir: Path) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\nPRAGMA user_version = 1;\n")
        (migrations_dir / "003_skip.sql").write_text("SELECT 3;\nPRAGMA user_version = 3;\n")
        with pytest.raises(MigrationError, match="Missing migration versions"):
            runner.get_pending_migrations()


class TestMigrate:
    def test_returns_zero_when_nothing_pending(self, runner: MigrationRunner) -> None:
        assert runner.migrate() == 0

    def test_applies_single_migration(
        self, runner: MigrationRunner, migrations_dir: Path, db_path: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE images (id TEXT PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        applied = runner.migrate()
        assert applied == 1
        assert runner.get_current_version() == 1

    def test_applies_multiple_migrations_in_order(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        (migrations_dir / "002_add.sql").write_text(
            "CREATE TABLE bar (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 2;\n"
        )
        applied = runner.migrate()
        assert applied == 2
        assert runner.get_current_version() == 2

    def test_idempotent_when_run_twice(self, runner: MigrationRunner, migrations_dir: Path) -> None:
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        runner.migrate()
        applied = runner.migrate()
        assert applied == 0

    def test_raises_migration_error_on_bad_sql(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_bad.sql").write_text("THIS IS NOT SQL;\nPRAGMA user_version = 1;\n")
        with pytest.raises(MigrationError, match="001_bad.sql"):
            runner.migrate()

    def test_records_migration_in_db_migrations_table(
        self, runner: MigrationRunner, migrations_dir: Path, db_path: Path
    ) -> None:
        import sqlite3

        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        runner.migrate()
        with closing(sqlite3.connect(db_path)) as conn:
            with conn:
                rows = conn.execute("SELECT version, name FROM db_migrations").fetchall()
        assert len(rows) == 1
        assert rows[0][0] == 1
        assert rows[0][1] == "001_init.sql"

    def test_creates_parent_directories(self, migrations_dir: Path, tmp_path: Path) -> None:
        db_path = tmp_path / "deep" / "nested" / "test.db"
        runner = MigrationRunner(db_path=db_path, migrations_dir=migrations_dir)
        (migrations_dir / "001_init.sql").write_text(
            "CREATE TABLE foo (id INTEGER PRIMARY KEY);\nPRAGMA user_version = 1;\n"
        )
        runner.migrate()
        assert db_path.exists()


class TestValidateMigrations:
    def test_returns_empty_for_valid_migrations(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\n")
        (migrations_dir / "002_more.sql").write_text("SELECT 2;\n")
        assert runner.validate_migrations() == []

    def test_returns_error_for_missing_dir(self, db_path: Path, tmp_path: Path) -> None:
        runner = MigrationRunner(db_path=db_path, migrations_dir=tmp_path / "nonexistent")
        errors = runner.validate_migrations()
        assert len(errors) == 1
        assert "not found" in errors[0]

    def test_returns_error_for_version_gap(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "001_init.sql").write_text("SELECT 1;\n")
        (migrations_dir / "003_skip.sql").write_text("SELECT 3;\n")
        errors = runner.validate_migrations()
        assert any("Missing" in e for e in errors)


class TestExtractVersion:
    def test_extracts_version_from_valid_filename(
        self, runner: MigrationRunner, tmp_path: Path
    ) -> None:
        f = tmp_path / "042_some_migration.sql"
        assert runner._extract_version(f) == 42

    def test_raises_on_invalid_filename(self, runner: MigrationRunner, tmp_path: Path) -> None:
        f = tmp_path / "invalid_name.sql"
        with pytest.raises(MigrationError, match="Invalid migration filename"):
            runner._extract_version(f)


class TestRollback:
    def test_raises_not_implemented(self, runner: MigrationRunner) -> None:
        with pytest.raises(NotImplementedError):
            runner.rollback()


class TestValidateMigrationsEdgeCases:
    def test_invalid_filename_in_glob_adds_error(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "1abc_name.sql").write_text("SELECT 1;\n")
        errors = runner.validate_migrations()
        assert any("Invalid migration filename" in e for e in errors)

    def test_only_invalid_filenames_no_gap_check(
        self, runner: MigrationRunner, migrations_dir: Path
    ) -> None:
        (migrations_dir / "1abc_name.sql").write_text("SELECT 1;\n")
        errors = runner.validate_migrations()
        assert len(errors) >= 1
        assert not any("Missing" in e for e in errors)
