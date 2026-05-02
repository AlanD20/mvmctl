"""Tests for KeyRepository with real SQLite database."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.key._repository import KeyRepository
from mvmctl.models import SSHKeyItem


def _seed_key(
    db: Database,
    name: str = "testkey",
    fingerprint: str = "SHA256:abc123",
    is_default: bool = False,
    is_present: bool = True,
) -> str:
    """Insert a key row directly and return its fingerprint (used as ID)."""
    now = datetime.now(tz=UTC).isoformat()
    # Generate unique fingerprint based on name if not explicitly provided
    effective_fp = (
        fingerprint if fingerprint != "SHA256:abc123" else f"SHA256:abc_{name}"
    )
    with db.connect() as conn:
        conn.execute(
            """
            INSERT INTO ssh_keys (id, name, fingerprint, algorithm, comment,
                                  private_key_path, public_key_path, is_default,
                                  is_present, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                effective_fp,
                name,
                effective_fp,
                "ssh-ed25519",
                "test@host",
                None,
                f"/tmp/keys/{name}.pub",
                int(is_default),
                int(is_present),
                now,
                now,
            ),
        )
    return effective_fp


class TestKeyRepository:
    """Tests for KeyRepository."""

    def test_get_by_name_found(self, db: Database) -> None:
        """get_by_name returns a key when found."""
        _seed_key(db, name="mykey")
        repo = KeyRepository(db)
        key = repo.get_by_name("mykey")
        assert key is not None
        assert key.name == "mykey"

    def test_get_by_name_not_found(self, db: Database) -> None:
        """get_by_name returns None when key doesn't exist."""
        repo = KeyRepository(db)
        assert repo.get_by_name("nonexistent") is None

    def test_find_by_prefix(self, db: Database) -> None:
        """find_by_prefix finds keys by fingerprint prefix."""
        fp = _seed_key(db, fingerprint="SHA256:abcdef123456")
        repo = KeyRepository(db)
        results = repo.find_by_prefix("SHA256:abc")
        assert len(results) == 1
        assert results[0].id == fp

    def test_find_by_prefix_no_match(self, db: Database) -> None:
        """find_by_prefix returns empty list when no match."""
        repo = KeyRepository(db)
        assert repo.find_by_prefix("ZZZZZZZ") == []

    def test_list_all_empty(self, db: Database) -> None:
        """list_all returns empty list when no keys exist."""
        repo = KeyRepository(db)
        assert repo.list_all() == []

    def test_list_all_returns_keys(self, db: Database) -> None:
        """list_all returns all keys."""
        _seed_key(db, name="key1")
        _seed_key(db, name="key2", fingerprint="SHA256:def456")
        repo = KeyRepository(db)
        results = repo.list_all()
        assert len(results) == 2

    def test_upsert_creates_new(self, db: Database) -> None:
        """upsert inserts a new key record."""
        repo = KeyRepository(db)
        now = datetime.now(tz=UTC).isoformat()
        key = SSHKeyItem(
            id="SHA256:newfp",
            name="newkey",
            fingerprint="SHA256:newfp",
            algorithm="ssh-ed25519",
            comment="new@host",
            private_key_path=None,
            public_key_path="/tmp/keys/newkey.pub",
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(key)
        retrieved = repo.get_by_name("newkey")
        assert retrieved is not None
        assert retrieved.fingerprint == "SHA256:newfp"

    def test_upsert_updates_existing(self, db: Database) -> None:
        """upsert updates an existing key record."""
        fp = _seed_key(db, name="updatekey")
        repo = KeyRepository(db)
        now = datetime.now(tz=UTC).isoformat()
        key = SSHKeyItem(
            id=fp,
            name="updatekey",
            fingerprint=fp,
            algorithm="ssh-rsa",
            comment="updated@host",
            private_key_path=None,
            public_key_path="/tmp/keys/updatekey.pub",
            is_default=True,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(key)
        retrieved = repo.get_by_name("updatekey")
        assert retrieved is not None
        assert retrieved.algorithm == "ssh-rsa"
        assert retrieved.is_default is True or retrieved.is_default == 1

    def test_delete_removes_key(self, db: Database) -> None:
        """delete removes a key by ID."""
        fp = _seed_key(db, name="deletekey")
        repo = KeyRepository(db)
        repo.delete(fp)
        assert repo.get_by_name("deletekey") is None

    def test_delete_is_idempotent(self, db: Database) -> None:
        """delete is a no-op when key doesn't exist."""
        repo = KeyRepository(db)
        repo.delete("nonexistent")  # Should not raise

    def test_set_default(self, db: Database) -> None:
        """set_default marks a key as default."""
        fp = _seed_key(db, name="mykey")
        repo = KeyRepository(db)
        repo.set_default(fp)
        defaults = repo.get_defaults()
        assert len(defaults) == 1
        assert defaults[0].name == "mykey"

    def test_get_defaults_returns_all_defaults(self, db: Database) -> None:
        """get_defaults returns all keys marked as default."""
        _seed_key(db, name="key1", fingerprint="fp1", is_default=True)
        _seed_key(db, name="key2", fingerprint="fp2", is_default=True)
        repo = KeyRepository(db)
        defaults = repo.get_defaults()
        assert len(defaults) == 2

    def test_get_defaults_empty(self, db: Database) -> None:
        """get_defaults returns empty list when no defaults."""
        repo = KeyRepository(db)
        assert repo.get_defaults() == []

    def test_clear_defaults(self, db: Database) -> None:
        """clear_defaults removes all default marks."""
        _seed_key(db, name="key1", is_default=True)
        _seed_key(db, name="key2", is_default=True)
        repo = KeyRepository(db)
        repo.clear_defaults()
        assert repo.get_defaults() == []

    def test_update_many_is_present(self, db: Database) -> None:
        """update_many_is_present updates multiple keys."""
        fp1 = _seed_key(db, name="key1", fingerprint="fp1")
        fp2 = _seed_key(db, name="key2", fingerprint="fp2")
        repo = KeyRepository(db)
        repo.update_many_is_present([fp1, fp2], False)
        result = repo.get_by_name("key1")
        assert result is not None
        assert result.is_present is False or result.is_present == 0

    def test_update_many_is_present_empty(self, db: Database) -> None:
        """update_many_is_present handles empty list."""
        repo = KeyRepository(db)
        repo.update_many_is_present([], True)  # Should not raise


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database
