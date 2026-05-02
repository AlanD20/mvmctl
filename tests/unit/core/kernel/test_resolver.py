"""Unit tests for KernelResolver — resolution by ID prefix, version/type.

Tests use a real SQLite database (migrated by autouse conftest fixtures).
"""

from __future__ import annotations

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.exceptions import KernelNotFoundError
from mvmctl.models import KernelItem

_TIMESTAMP = "2026-01-01T00:00:00+00:00"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _seed_kernel(
    repo: KernelRepository,
    id: str = "a" * 64,
    name: str = "vmlinux",
    version: str = "6.1.0",
    type: str = "official",
    arch: str = "x86_64",
    path: str = "vmlinux",
    is_default: bool = False,
) -> None:
    repo.upsert(
        KernelItem(
            id=id,
            name=name,
            base_name=name.split("-")[0],
            version=version,
            arch=arch,
            type=type,
            path=path,
            is_default=is_default,
            is_present=True,
            created_at=_TIMESTAMP,
            updated_at=_TIMESTAMP,
        )
    )


@pytest.fixture
def repo() -> KernelRepository:
    return KernelRepository(Database())


@pytest.fixture
def resolver(repo: KernelRepository) -> KernelResolver:
    return KernelResolver(repo)


# ======================================================================
# by_id()
# ======================================================================


class TestById:
    """Tests for KernelResolver.by_id()."""

    def test_exact_full_id(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64)
        result = resolver.by_id("a" * 64)
        assert result.id == "a" * 64

    def test_prefix(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="abcdef1234567890" + "0" * 48)
        result = resolver.by_id("abcdef")
        assert result.id == "abcdef1234567890" + "0" * 48

    def test_not_found(self, resolver: KernelResolver) -> None:
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.by_id("nonexistent")

    def test_ambiguous_prefix(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" + "b" * 63, name="k1")
        _seed_kernel(repo, id="a" + "c" * 63, name="k2")
        with pytest.raises(KernelNotFoundError, match="ambiguous"):
            resolver.by_id("a")

    def test_soft_deleted_not_found(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64)
        repo.soft_delete("a" * 64)
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.by_id("a" * 64)


# ======================================================================
# by_version_type()
# ======================================================================


class TestByVersionType:
    """Tests for KernelResolver.by_version_type()."""

    def test_found(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, version="6.1.0", type="official")
        result = resolver.by_version_type("6.1.0", "official")
        assert result.version == "6.1.0"
        assert result.type == "official"

    def test_not_found_version(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, version="6.1.0", type="official")
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.by_version_type("6.6.0", "official")

    def test_not_found_type(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, version="6.1.0", type="official")
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.by_version_type("6.1.0", "firecracker")

    def test_soft_deleted_not_found(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, version="6.1.0", type="official")
        repo.soft_delete("a" * 64)
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.by_version_type("6.1.0", "official")


# ======================================================================
# resolve() — alias for by_id()
# ======================================================================


class TestResolve:
    """Tests for KernelResolver.resolve()."""

    def test_resolve_by_id(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64)
        result = resolver.resolve("a" * 64)
        assert result.id == "a" * 64

    def test_resolve_by_prefix(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="abcdef1234567890" + "0" * 48)
        result = resolver.resolve("abcdef")
        assert result.id == "abcdef1234567890" + "0" * 48

    def test_resolve_not_found(self, resolver: KernelResolver) -> None:
        with pytest.raises(KernelNotFoundError, match="Kernel not found"):
            resolver.resolve("nonexistent")


# ======================================================================
# get_default()
# ======================================================================


class TestGetDefault:
    """Tests for KernelResolver.get_default()."""

    def test_default_exists(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, is_default=True)
        result = resolver.get_default()
        assert result is not None
        assert result.id == "a" * 64
        assert result.is_default

    def test_no_default(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, is_default=False)
        assert resolver.get_default() is None

    def test_empty_database(self, resolver: KernelResolver) -> None:
        assert resolver.get_default() is None


# ======================================================================
# resolve_many()
# ======================================================================


class TestResolveMany:
    """Tests for KernelResolver.resolve_many()."""

    def test_multiple_ids(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, name="k1")
        _seed_kernel(repo, id="b" * 64, name="k2")
        result = resolver.resolve_many(["a" * 64, "b" * 64])
        assert len(result.items) == 2
        assert {k.name for k in result.items} == {"k1", "k2"}
        assert result.errors == []
        assert result.exit_code == 0

    def test_deduplicates(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, name="k1")
        result = resolver.resolve_many(["a" * 64, "a" * 64])
        assert len(result.items) == 1
        assert result.items[0].name == "k1"

    def test_with_errors(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, name="k1")
        result = resolver.resolve_many(["a" * 64, "nonexistent"])
        assert len(result.items) == 1
        assert result.items[0].name == "k1"
        assert len(result.errors) == 1
        assert "nonexistent" in result.errors[0]
        assert result.exit_code == 0

    def test_all_errors(self, resolver: KernelResolver) -> None:
        result = resolver.resolve_many(["nope1", "nope2"])
        assert result.items == []
        assert len(result.errors) == 2
        assert result.exit_code == 1

    def test_version_type_pairs(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(
            repo, id="a" * 64, name="k1", version="6.1.0", type="official"
        )
        _seed_kernel(
            repo, id="b" * 64, name="k2", version="1.15", type="firecracker"
        )
        result = resolver.resolve_many(
            [["6.1.0", "official"], ["1.15", "firecracker"]]
        )
        assert len(result.items) == 2
        names = {k.name for k in result.items}
        assert names == {"k1", "k2"}

    def test_mixed_ids_and_pairs(
        self, resolver: KernelResolver, repo: KernelRepository
    ) -> None:
        _seed_kernel(repo, id="a" * 64, name="k1")
        _seed_kernel(
            repo, id="b" * 64, name="k2", version="6.6.0", type="custom"
        )

        result = resolver.resolve_many(["a" * 64, ["6.6.0", "custom"]])
        assert len(result.items) == 2
        names = {k.name for k in result.items}
        assert names == {"k1", "k2"}

    def test_empty_list(self, resolver: KernelResolver) -> None:
        result = resolver.resolve_many([])
        assert result.items == []
        assert result.errors == []
        assert result.exit_code == 0
