"""Tests for BinaryRepository with real SQLite database."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.models import BinaryItem


def _seed_binary(
    db: Database,
    name: str = "firecracker",
    version: str = "1.15.0",
    is_default: bool = False,
    is_present: bool = True,
    deleted_at: str | None = None,
) -> str:
    """Insert a binary row directly and return its ID."""
    now = datetime.now(tz=UTC).isoformat()
    binary_id = f"test-{name}-{version}"
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
                int(is_present),
                now,
                now,
                deleted_at,
            ),
        )
    return binary_id


class TestBinaryRepository:
    """Tests for BinaryRepository."""

    def test_get_returns_binary(self, db: Database) -> None:
        """get() returns a BinaryItem when found."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        binary = repo.get(bid)
        assert binary is not None
        assert binary.name == "firecracker"
        assert binary.version == "1.15.0"

    def test_get_returns_none_when_missing(self, db: Database) -> None:
        """get() returns None when binary does not exist."""
        repo = BinaryRepository(db)
        assert repo.get("nonexistent-id") is None

    def test_get_returns_none_when_deleted(self, db: Database) -> None:
        """get() returns None for soft-deleted binaries."""
        bid = _seed_binary(db, deleted_at=datetime.now(tz=UTC).isoformat())
        repo = BinaryRepository(db)
        assert repo.get(bid) is None

    def test_find_by_prefix_exact(self, db: Database) -> None:
        """find_by_prefix finds binary by ID prefix."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        results = repo.find_by_prefix(bid[:8])
        assert len(results) == 1
        assert results[0].id == bid

    def test_find_by_prefix_no_match(self, db: Database) -> None:
        """find_by_prefix returns empty list when prefix doesn't match."""
        repo = BinaryRepository(db)
        assert repo.find_by_prefix("ZZZZZZZZ") == []

    def test_find_by_prefix_skips_deleted(self, db: Database) -> None:
        """find_by_prefix excludes soft-deleted binaries."""
        _seed_binary(db, deleted_at=datetime.now(tz=UTC).isoformat())
        repo = BinaryRepository(db)
        assert repo.find_by_prefix("test-") == []

    def test_list_all_empty(self, db: Database) -> None:
        """list_all() returns empty list when no binaries exist."""
        repo = BinaryRepository(db)
        assert repo.list_all() == []

    def test_list_all_returns_binaries(self, db: Database) -> None:
        """list_all() returns all non-deleted binaries."""
        _seed_binary(db, name="firecracker", version="1.15.0")
        _seed_binary(db, name="firecracker", version="1.14.0")
        repo = BinaryRepository(db)
        results = repo.list_all()
        assert len(results) == 2

    def test_list_all_skips_deleted(self, db: Database) -> None:
        """list_all() excludes soft-deleted binaries."""
        _seed_binary(db, name="ok", version="1.0.0")
        _seed_binary(
            db,
            name="gone",
            version="2.0.0",
            deleted_at=datetime.now(tz=UTC).isoformat(),
        )
        repo = BinaryRepository(db)
        results = repo.list_all()
        assert len(results) == 1

    def test_list_by_name(self, db: Database) -> None:
        """list_by_name returns only binaries with matching name."""
        _seed_binary(db, name="firecracker", version="1.15.0")
        _seed_binary(db, name="jailer", version="1.15.0")
        repo = BinaryRepository(db)
        results = repo.list_by_name("firecracker")
        assert len(results) == 1
        assert results[0].name == "firecracker"

    def test_get_by_name_and_version_found(self, db: Database) -> None:
        """get_by_name_and_version returns matching binary."""
        _seed_binary(db, name="firecracker", version="1.15.0")
        repo = BinaryRepository(db)
        binary = repo.get_by_name_and_version("firecracker", "1.15.0")
        assert binary is not None
        assert binary.version == "1.15.0"

    def test_get_by_name_and_version_not_found(self, db: Database) -> None:
        """get_by_name_and_version returns None when no match."""
        repo = BinaryRepository(db)
        assert repo.get_by_name_and_version("firecracker", "9.9.9") is None

    def test_upsert_creates_new(self, db: Database) -> None:
        """upsert inserts a new binary record."""
        repo = BinaryRepository(db)
        binary = BinaryItem(
            id="new-id",
            name="firecracker",
            version="1.16.0",
            full_version="v1.16.0",
            ci_version="v1.16",
            path="firecracker-v1.16.0",
            is_default=False,
            is_present=True,
            created_at=datetime.now(tz=UTC).isoformat(),
            updated_at=datetime.now(tz=UTC).isoformat(),
        )
        repo.upsert(binary)
        retrieved = repo.get("new-id")
        assert retrieved is not None
        assert retrieved.version == "1.16.0"

    def test_upsert_updates_existing(self, db: Database) -> None:
        """upsert updates an existing record."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        binary = BinaryItem(
            id=bid,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
            path="firecracker-v1.15.0",
            is_default=True,
            is_present=True,
            created_at=datetime.now(tz=UTC).isoformat(),
            updated_at=datetime.now(tz=UTC).isoformat(),
        )
        repo.upsert(binary)
        retrieved = repo.get(bid)
        assert retrieved is not None
        assert retrieved.is_default is True or retrieved.is_default == 1

    def test_delete_removes_record(self, db: Database) -> None:
        """delete() removes the binary record entirely."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        repo.delete(bid)
        assert repo.get(bid) is None

    def test_delete_by_name_and_version(self, db: Database) -> None:
        """delete_by_name_and_version removes matching record."""
        _seed_binary(db, name="firecracker", version="1.15.0")
        repo = BinaryRepository(db)
        repo.delete_by_name_and_version("firecracker", "1.15.0")
        assert repo.get_by_name_and_version("firecracker", "1.15.0") is None

    def test_soft_delete_sets_deleted_at(self, db: Database) -> None:
        """soft_delete sets deleted_at and is_present=0."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        repo.soft_delete(bid)
        binary = repo.get(bid)
        assert binary is None  # filtered out by get()
        # Verify it still exists in DB with deleted_at set
        with db.connect() as conn:
            row = conn.execute(
                "SELECT deleted_at, is_present FROM binaries WHERE id = ?",
                (bid,),
            ).fetchone()
        assert row is not None
        assert row["deleted_at"] is not None
        assert row["is_present"] == 0

    def test_set_default_and_get_default(self, db: Database) -> None:
        """set_default marks one binary as default, get_default retrieves it."""
        _seed_binary(db, name="firecracker", version="1.14.0")
        _seed_binary(db, name="firecracker", version="1.15.0")
        repo = BinaryRepository(db)
        repo.set_default("firecracker", "1.15.0", "firecracker-v1.15.0")
        default = repo.get_default("firecracker")
        assert default is not None
        assert default.version == "1.15.0"
        # Ensure only one default
        with db.connect() as conn:
            rows = conn.execute(
                "SELECT * FROM binaries WHERE name = ? AND is_default = 1 AND deleted_at IS NULL",
                ("firecracker",),
            ).fetchall()
        assert len(rows) == 1

    def test_get_default_returns_none_when_not_set(self, db: Database) -> None:
        """get_default returns None when no default binary exists."""
        repo = BinaryRepository(db)
        assert repo.get_default("firecracker") is None

    def test_count(self, db: Database) -> None:
        """COUNT query works correctly."""
        _seed_binary(db, name="firecracker", version="1.15.0")
        _seed_binary(db, name="jailer", version="1.15.0")
        with db.connect() as conn:
            count = conn.execute(
                "SELECT COUNT(*) FROM binaries WHERE deleted_at IS NULL"
            ).fetchone()[0]
        assert count == 2

    def test_update_many_is_present(self, db: Database) -> None:
        """update_many_is_present updates multiple binaries."""
        bid_1 = _seed_binary(db, name="firecracker", version="1.15.0")
        bid_2 = _seed_binary(db, name="jailer", version="1.15.0")
        repo = BinaryRepository(db)
        repo.update_many_is_present([bid_1, bid_2], False)
        b1 = repo.get(bid_1)
        b2 = repo.get(bid_2)
        assert b1 is None or not b1.is_present
        assert b2 is None or not b2.is_present

    def test_query_vms_by_binary_empty(self, db: Database) -> None:
        """query_vms_by_binary returns empty list when no VMs reference binary."""
        bid = _seed_binary(db)
        repo = BinaryRepository(db)
        assert repo.query_vms_by_binary(bid) == []


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database
