"""Tests for BinaryResolver."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.exceptions import BinaryNotFoundError


def _seed_binary(
    db: Database,
    binary_id: str,
    name: str = "firecracker",
    version: str = "1.15.0",
    is_default: bool = False,
) -> str:
    """Insert a binary row directly and return its ID."""
    now = datetime.now(tz=UTC).isoformat()
    with db.connect() as conn:
        conn.execute(
            """
            INSERT INTO binaries (id, name, version, full_version, ci_version, path,
                                  is_default, is_present, created_at, updated_at, deleted_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                binary_id,
                name,
                version,
                f"v{version}",
                f"v{version.rsplit('.', 1)[0]}",
                f"{name}-v{version}",
                int(is_default),
                1,
                now,
                now,
                None,
            ),
        )
    return binary_id


class TestBinaryResolver:
    """Tests for BinaryResolver."""

    def test_by_id_exact(self, db: Database) -> None:
        """by_id resolves by full ID."""
        _seed_binary(db, "abc123def456")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.by_id("abc123def456")
        assert result.id == "abc123def456"

    def test_by_id_prefix(self, db: Database) -> None:
        """by_id resolves by ID prefix."""
        _seed_binary(db, "abcdef123456")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.by_id("abcdef")
        assert result.id == "abcdef123456"

    def test_by_id_not_found(self, db: Database) -> None:
        """by_id raises BinaryNotFoundError when no match."""
        resolver = BinaryResolver(BinaryRepository(db))
        with pytest.raises(BinaryNotFoundError, match="Binary not found"):
            resolver.by_id("nonexistent")

    def test_by_id_ambiguous(self, db: Database) -> None:
        """by_id raises BinaryNotFoundError when prefix is ambiguous."""
        _seed_binary(db, "abc000000001")
        _seed_binary(db, "abc000000002")
        resolver = BinaryResolver(BinaryRepository(db))
        with pytest.raises(BinaryNotFoundError, match="ambiguous"):
            resolver.by_id("abc")

    def test_by_name_version(self, db: Database) -> None:
        """by_name_version resolves by name and version."""
        _seed_binary(db, "id-1", name="firecracker", version="1.15.0")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.by_name_version("firecracker", "1.15.0")
        assert result.name == "firecracker"
        assert result.version == "1.15.0"

    def test_by_name_version_not_found(self, db: Database) -> None:
        """by_name_version raises BinaryNotFoundError when no match."""
        resolver = BinaryResolver(BinaryRepository(db))
        with pytest.raises(BinaryNotFoundError, match="Binary not found"):
            resolver.by_name_version("firecracker", "9.9.9")

    def test_get_default_found(self, db: Database) -> None:
        """get_default returns the default binary for a name."""
        _seed_binary(db, "id-1", name="firecracker", version="1.14.0")
        _seed_binary(
            db, "id-2", name="firecracker", version="1.15.0", is_default=True
        )
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.get_default("firecracker")
        assert result is not None
        assert result.version == "1.15.0"

    def test_get_default_none(self, db: Database) -> None:
        """get_default returns None when no default set."""
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.get_default("firecracker")
        assert result is None

    def test_resolve_string(self, db: Database) -> None:
        """resolve() with string delegates to by_id."""
        _seed_binary(db, "abc123def456")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.resolve("abc123")
        assert result.id == "abc123def456"

    def test_resolve_many_all_success(self, db: Database) -> None:
        """resolve_many resolves multiple identifiers."""
        _seed_binary(db, "aaa000000001", name="firecracker", version="1.15.0")
        _seed_binary(db, "bbb000000001", name="firecracker", version="1.14.0")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.resolve_many(["aaa", ["firecracker", "1.14.0"]])
        assert len(result.items) == 2
        assert len(result.errors) == 0

    def test_resolve_many_with_errors(self, db: Database) -> None:
        """resolve_many reports errors for failed resolutions."""
        _seed_binary(db, "aaa000000001", name="firecracker", version="1.15.0")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.resolve_many(["aaa", "nonexistent"])
        assert len(result.items) == 1
        assert len(result.errors) == 1
        assert "nonexistent" in result.errors[0]

    def test_resolve_many_deduplicates(self, db: Database) -> None:
        """resolve_many deduplicates identifiers."""
        _seed_binary(db, "aaa000000001")
        resolver = BinaryResolver(BinaryRepository(db))
        result = resolver.resolve_many(["aaa", "aaa"])
        assert len(result.items) == 1


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database
