"""Tests for VolumeResolver — volume resolution logic."""

from __future__ import annotations

import json
from unittest.mock import MagicMock

import pytest

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver, VolumeResolveResult
from mvmctl.exceptions import VolumeNotFoundError
from mvmctl.models import VolumeItem


def _make_volume(name: str = "test-vol", vid: str | None = None) -> VolumeItem:
    vol_id = vid or f"{name}-id-" + "x" * 55
    return VolumeItem(
        id=vol_id,
        name=name,
        size_bytes=1073741824,
        format="raw",
        path=f"/volumes/{name}.raw",
        status="available",
        vm_id=None,
        created_at="2026-01-01T00:00:00+00:00",
        updated_at="2026-01-01T00:00:00+00:00",
    )


class TestVolumeResolverById:
    def test_by_id_found(self):
        vol = _make_volume()
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_prefix.return_value = [vol]
        resolver = VolumeResolver(repo)
        result = resolver.by_id(vol.id)
        assert result is vol
        repo.find_by_prefix.assert_called_once_with(vol.id)

    def test_by_id_not_found_raises(self):
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_prefix.return_value = []
        resolver = VolumeResolver(repo)
        with pytest.raises(VolumeNotFoundError, match="not found"):
            resolver.by_id("nonexistent")

    def test_by_id_ambiguous_raises(self):
        vol1 = _make_volume("vol-1")
        vol2 = _make_volume("vol-2")
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_prefix.return_value = [vol1, vol2]
        resolver = VolumeResolver(repo)
        with pytest.raises(VolumeNotFoundError, match="ambiguous"):
            resolver.by_id("prefix")


class TestVolumeResolverByName:
    def test_by_name_found(self):
        vol = _make_volume("my-vol")
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = vol
        resolver = VolumeResolver(repo)
        result = resolver.by_name("my-vol")
        assert result is vol
        repo.get_by_name.assert_called_once_with("my-vol")

    def test_by_name_not_found_raises(self):
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = None
        resolver = VolumeResolver(repo)
        with pytest.raises(VolumeNotFoundError, match="not found by name"):
            resolver.by_name("nonexistent")


class TestVolumeResolverResolve:
    def test_resolve_by_name_first(self):
        """resolve() tries name first."""
        vol = _make_volume("my-vol")
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = vol
        resolver = VolumeResolver(repo)
        result = resolver.resolve("my-vol")
        assert result is vol
        repo.get_by_name.assert_called_once_with("my-vol")
        repo.find_by_prefix.assert_not_called()

    def test_resolve_falls_back_to_id(self):
        """resolve() falls back to by_id when by_name fails."""
        vol = _make_volume("my-vol")
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = None
        repo.find_by_prefix.return_value = [vol]
        resolver = VolumeResolver(repo)
        result = resolver.resolve("some-id-prefix")
        assert result is vol
        repo.get_by_name.assert_called_once_with("some-id-prefix")
        repo.find_by_prefix.assert_called_once_with("some-id-prefix")

    def test_resolve_falls_back_raises_when_both_fail(self):
        """resolve() raises when both by_name and by_id fail."""
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = None
        repo.find_by_prefix.return_value = []
        resolver = VolumeResolver(repo)
        with pytest.raises(VolumeNotFoundError, match="not found"):
            resolver.resolve("ghost")


class TestVolumeResolverResolveMany:
    def test_resolve_many_all_found(self):
        vols = [_make_volume("vol-a"), _make_volume("vol-b")]
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.side_effect = lambda n: next(v for v in vols if v.name == n)
        resolver = VolumeResolver(repo)
        result = resolver.resolve_many(["vol-a", "vol-b"])
        assert len(result.items) == 2
        assert len(result.errors) == 0
        assert result.exit_code == 0

    def test_resolve_many_deduplicates_ids(self):
        vol = _make_volume("vol-a")
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = vol
        resolver = VolumeResolver(repo)
        result = resolver.resolve_many(["vol-a", "vol-a"])
        assert len(result.items) == 1
        assert result.exit_code == 0

    def test_resolve_many_partial_failure(self):
        vol = _make_volume("vol-a")
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.side_effect = lambda n: vol if n == "vol-a" else None
        repo.find_by_prefix.side_effect = lambda p: [] if p == "bad-vol" else [vol]
        resolver = VolumeResolver(repo)

        result = resolver.resolve_many(["vol-a", "bad-vol"])
        assert len(result.items) == 1
        assert len(result.errors) == 1
        # partial failure: items exist + errors exist = exit_code 0
        assert result.exit_code == 0

    def test_resolve_many_all_fail(self):
        repo = MagicMock(spec=VolumeRepository)
        repo.get_by_name.return_value = None
        repo.find_by_prefix.return_value = []
        resolver = VolumeResolver(repo)
        result = resolver.resolve_many(["bad-one", "bad-two"])
        assert len(result.items) == 0
        assert len(result.errors) == 2
        assert result.exit_code == 1

    def test_resolve_many_empty_list(self):
        repo = MagicMock(spec=VolumeRepository)
        resolver = VolumeResolver(repo)
        result = resolver.resolve_many([])
        assert len(result.items) == 0
        assert len(result.errors) == 0
        assert result.exit_code == 0

    def test_resolve_many_deduplicates_by_id(self):
        """Same volume resolved via different identifiers should be deduplicated."""
        vol = _make_volume("my-vol")
        repo = MagicMock(spec=VolumeRepository)
        # First call by_name finds it, second call by_name should also find it
        repo.get_by_name.return_value = vol
        resolver = VolumeResolver(repo)
        result = resolver.resolve_many(["my-vol", "my-vol"])
        assert len(result.items) == 1
        assert result.exit_code == 0


class TestVolumeResolverDefaultRepo:
    def test_default_repo_creation(self, _setup_database):
        """Resolver creates its own VolumeRepository if none provided."""
        resolver = VolumeResolver()
        assert resolver._repo is not None
        assert resolver._repo.count() == 0

    def test_repository_db_property(self, _setup_database):
        """VolumeRepository.db property should return the Database instance."""
        repo = VolumeRepository()
        db = repo.db
        assert db is not None
        assert db == repo._db

    def test_resolve_result_dataclass(self):
        """VolumeResolveResult should work as a dataclass."""
        vol = _make_volume()
        result = VolumeResolveResult(
            items=[vol],
            errors=[],
            exit_code=0,
        )
        assert result.items == [vol]
        assert result.errors == []
        assert result.exit_code == 0


class TestVolumeResolverResolveByIds:
    """Tests for resolve_by_ids()."""

    def test_resolve_by_ids_found(self):
        vol1 = _make_volume("vol-a")
        vol2 = _make_volume("vol-b")
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_ids.return_value = [vol1, vol2]
        resolver = VolumeResolver(repo)
        result = resolver.resolve_by_ids([vol1.id, vol2.id])
        assert result[vol1.id] is vol1
        assert result[vol2.id] is vol2
        repo.find_by_ids.assert_called_once()

    def test_resolve_by_ids_empty(self):
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_ids.return_value = []
        resolver = VolumeResolver(repo)
        result = resolver.resolve_by_ids(["nonexistent"])
        assert result == {}


class TestVolumeResolverResolveByVmVolumeIds:
    """Tests for resolve_by_vm_volume_ids()."""

    def test_resolve_by_vm_volume_ids_success(self):
        vol1 = _make_volume("vol-a")
        vol2 = _make_volume("vol-b")
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_ids.return_value = [vol1, vol2]
        resolver = VolumeResolver(repo)
        json_input = json.dumps([vol1.id, vol2.id])
        result = resolver.resolve_by_vm_volume_ids([json_input])
        assert json_input in result
        assert len(result[json_input]) == 2
        assert result[json_input][0] is vol1
        assert result[json_input][1] is vol2

    def test_resolve_by_vm_volume_ids_empty_json(self):
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_ids.return_value = []
        resolver = VolumeResolver(repo)
        json_input = "[]"
        result = resolver.resolve_by_vm_volume_ids([json_input])
        assert json_input in result
        assert result[json_input] == []

    def test_resolve_by_vm_volume_ids_invalid_json(self):
        repo = MagicMock(spec=VolumeRepository)
        resolver = VolumeResolver(repo)
        json_input = "not-json"
        result = resolver.resolve_by_vm_volume_ids([json_input])
        assert json_input in result
        assert result[json_input] == []

    def test_resolve_by_vm_volume_ids_not_a_list(self):
        repo = MagicMock(spec=VolumeRepository)
        resolver = VolumeResolver(repo)
        json_input = '{"not": "a list"}'
        result = resolver.resolve_by_vm_volume_ids([json_input])
        assert json_input in result
        assert result[json_input] == []

    def test_resolve_by_vm_volume_ids_mixed(self):
        vol1 = _make_volume("vol-a")
        repo = MagicMock(spec=VolumeRepository)
        repo.find_by_ids.return_value = [vol1]
        resolver = VolumeResolver(repo)
        good_json = json.dumps([vol1.id])
        bad_json = "invalid"
        empty_json = "[]"
        result = resolver.resolve_by_vm_volume_ids([good_json, bad_json, empty_json])
        assert len(result[good_json]) == 1
        assert result[bad_json] == []
        assert result[empty_json] == []


class TestVolumeRepositoryFindByIds:
    """Tests for VolumeRepository.find_by_ids()."""

    def test_find_by_ids_with_values(self, _setup_database):
        """find_by_ids with IDs should return matching volumes."""
        vol = _make_volume("test-vol")
        repo = VolumeRepository()
        repo.upsert(vol)
        result = repo.find_by_ids([vol.id])
        assert len(result) == 1
        assert result[0].name == "test-vol"

    def test_find_by_ids_empty_list_returns_empty(self, _setup_database):
        """find_by_ids with empty list should return empty list."""
        repo = VolumeRepository()
        result = repo.find_by_ids([])
        assert result == []

    def test_find_by_ids_no_match_returns_empty(self, _setup_database):
        """find_by_ids with non-matching IDs should return empty list."""
        repo = VolumeRepository()
        result = repo.find_by_ids(["nonexistent-id"])
        assert result == []

    def test_find_by_ids_some_match(self, _setup_database):
        """find_by_ids should return only matching volumes."""
        vol1 = _make_volume("vol-a")
        vol2 = _make_volume("vol-b")
        repo = VolumeRepository()
        repo.upsert(vol1)
        repo.upsert(vol2)
        result = repo.find_by_ids([vol1.id, "nonexistent"])
        assert len(result) == 1
        assert result[0].id == vol1.id
