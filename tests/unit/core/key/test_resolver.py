"""Tests for KeyResolver — SSH key entity resolution."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.core.key._resolver import KeyResolver
from mvmctl.exceptions import KeyNotFoundError, MVMKeyError
from mvmctl.models import SSHKeyItem


def _make_key(name="mykey", kid="SHA256:abc123", **kw):
    defaults = dict(
        id=kid,
        name=name,
        fingerprint=kid,
        algorithm="ed25519",
        comment="test@example.com",
        public_key_path=f"/keys/{name}.pub",
        private_key_path=None,
        is_default=False,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )
    defaults.update(kw)
    return SSHKeyItem(**defaults)


class TestKeyResolverById:
    """Tests for by_id() — SHA256 prefix auto-prepend logic."""

    def test_exact_id(self, mocker):
        key = _make_key()
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = [key]
        resolver = KeyResolver(mock_repo)
        result = resolver.by_id("SHA256:abc123")
        assert result.id == "SHA256:abc123"

    def test_auto_prepends_sha256(self, mocker):
        """by_id() auto-prepends SHA256: when prefix is bare."""
        key = _make_key()
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.side_effect = [[], [key]]
        resolver = KeyResolver(mock_repo)
        result = resolver.by_id("abc123")
        assert result.id == "SHA256:abc123"
        # First call: bare "abc123", second call: "SHA256:abc123"
        assert mock_repo.find_by_prefix.call_count == 2

    def test_raises_on_not_found(self, mocker):
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = []
        resolver = KeyResolver(mock_repo)
        with pytest.raises(KeyNotFoundError):
            resolver.by_id("nonexistent")

    def test_raises_on_ambiguous(self, mocker):
        mock_repo = MagicMock()
        mock_repo.find_by_prefix.return_value = [
            _make_key(kid="SHA256:abc111"),
            _make_key(kid="SHA256:abc222"),
        ]
        resolver = KeyResolver(mock_repo)
        with pytest.raises(KeyNotFoundError, match="ambiguous"):
            resolver.by_id("abc")


class TestKeyResolverByName:
    def test_finds_by_name(self, mocker):
        key = _make_key(name="mykey")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = key
        resolver = KeyResolver(mock_repo)
        result = resolver.by_name("mykey")
        assert result.name == "mykey"

    def test_raises_on_not_found(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        resolver = KeyResolver(mock_repo)
        with pytest.raises(KeyNotFoundError):
            resolver.by_name("nonexistent")


class TestKeyResolverGetDefaults:
    def test_returns_defaults(self, mocker):
        keys = [_make_key(is_default=True)]
        mock_repo = MagicMock()
        mock_repo.get_defaults.return_value = keys
        resolver = KeyResolver(mock_repo)
        result = resolver.get_defaults()
        assert len(result) == 1

    def test_returns_empty(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_defaults.return_value = []
        resolver = KeyResolver(mock_repo)
        assert resolver.get_defaults() == []


class TestKeyResolverResolve:
    """Tests for resolve() — three-stage fallback: name → ID → .pub file."""

    def test_resolve_by_name(self, mocker):
        key = _make_key(name="mykey")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = key
        resolver = KeyResolver(mock_repo)
        result = resolver.resolve("mykey")
        assert result.name == "mykey"

    def test_resolve_fallback_to_id(self, mocker):
        key = _make_key(kid="SHA256:abc123")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None  # name fails
        mock_repo.find_by_prefix.side_effect = [[], [key]]  # id succeeds on 2nd try
        resolver = KeyResolver(mock_repo)
        result = resolver.resolve("abc123")
        assert result.id == "SHA256:abc123"

    def test_resolve_pub_file_raises_if_not_in_db(self, mocker):
        """resolve() raises when .pub file exists but key not in cache."""
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mock_repo.find_by_prefix.return_value = []
        resolver = KeyResolver(mock_repo)

        mocker.patch.object(Path, "exists", return_value=True)
        with pytest.raises(MVMKeyError, match="Import it first"):
            resolver.resolve("/keys/mykey.pub")

    def test_resolve_raises_on_no_match(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mock_repo.find_by_prefix.return_value = []
        resolver = KeyResolver(mock_repo)
        with pytest.raises(KeyNotFoundError):
            resolver.resolve("unknown")


class TestKeyResolverResolveMany:
    def test_resolves_multiple(self, mocker):
        key1 = _make_key(name="k1", kid="SHA256:id1")
        key2 = _make_key(name="k2", kid="SHA256:id2")
        mock_repo = MagicMock()
        mock_repo.get_by_name.side_effect = [key1, key2]
        resolver = KeyResolver(mock_repo)
        result = resolver.resolve_many(["k1", "k2"])
        assert len(result.items) == 2
        assert result.exit_code == 0

    def test_deduplicates(self, mocker):
        key = _make_key(name="k1")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = key
        resolver = KeyResolver(mock_repo)
        result = resolver.resolve_many(["k1", "k1"])
        assert len(result.items) == 1

    def test_partial_errors(self, mocker):
        key1 = _make_key(name="k1")
        mock_repo = MagicMock()
        mock_repo.get_by_name.side_effect = [key1, KeyNotFoundError("not found")]
        resolver = KeyResolver(mock_repo)
        result = resolver.resolve_many(["k1", "bad"])
        assert len(result.items) == 1
        assert len(result.errors) == 1
