"""Tests for ImageResolver — resolution by ID prefix, OS slug, and defaults."""

from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import ImageNotFoundError
from mvmctl.models import ImageItem


def _make_image(
    image_id: str,
    os_slug: str = "ubuntu-24.04",
    os_name: str = "Ubuntu 24.04",
) -> ImageItem:
    ts = datetime.now(tz=UTC).isoformat()
    return ImageItem(
        id=image_id,
        os_slug=os_slug,
        os_name=os_name,
        arch="x86_64",
        path=f"{os_slug}.ext4",
        fs_type="ext4",
        fs_uuid=None,
        compressed_size=None,
        original_size=2147483648,
        compression_ratio=None,
        compressed_format=None,
        minimum_rootfs_size_mib=2048,
        pulled_at=ts,
        is_default=False,
        is_present=True,
        created_at=ts,
        updated_at=ts,
    )


class TestImageResolver:
    """Tests for ImageResolver — entity resolution logic."""

    @pytest.fixture(autouse=True)
    def _setup(self, tmp_path: Path) -> None:
        """Set up a fresh in-memory database for each test."""
        self.db_path = tmp_path / "test.db"
        self.db = Database(self.db_path)
        self.db.migrate()
        self.repo = ImageRepository(self.db)
        self.resolver = ImageResolver(self.repo)

    def _seed_images(self) -> None:
        """Insert test images into the database."""
        images = [
            _make_image("abc12345" * 8, os_slug="ubuntu-24.04"),
            _make_image("def67890" * 8, os_slug="debian-12"),
            _make_image("ghi00000" * 8, os_slug="fedora-40"),
        ]
        for img in images:
            self.repo.upsert(img)

    # ------------------------------------------------------------------
    # by_id()
    # ------------------------------------------------------------------

    def test_by_id_resolves_exact_match(self) -> None:
        self._seed_images()
        result = self.resolver.by_id("abc12345" * 8)
        assert result is not None
        assert result.id == "abc12345" * 8
        assert result.os_slug == "ubuntu-24.04"

    def test_by_id_resolves_by_prefix(self) -> None:
        self._seed_images()
        result = self.resolver.by_id("abc")
        assert result.id == "abc12345" * 8

    def test_by_id_raises_when_not_found(self) -> None:
        self._seed_images()
        with pytest.raises(ImageNotFoundError, match="Image not found"):
            self.resolver.by_id("nonexistent")

    def test_by_id_raises_when_ambiguous(self) -> None:
        self.repo.upsert(_make_image("abc11111" * 8, os_slug="img-1"))
        self.repo.upsert(_make_image("abc22222" * 8, os_slug="img-2"))
        with pytest.raises(ImageNotFoundError, match="ambiguous"):
            self.resolver.by_id("abc")

    # ------------------------------------------------------------------
    # by_os_slug()
    # ------------------------------------------------------------------

    def test_by_os_slug_resolves(self) -> None:
        self._seed_images()
        result = self.resolver.by_os_slug("ubuntu-24.04")
        assert result.os_slug == "ubuntu-24.04"

    def test_by_os_slug_raises_when_not_found(self) -> None:
        self._seed_images()
        with pytest.raises(ImageNotFoundError, match="Image not found"):
            self.resolver.by_os_slug("nonexistent")

    def test_by_os_slug_excludes_soft_deleted(self) -> None:
        self._seed_images()
        self.repo.soft_delete("abc12345" * 8)
        with pytest.raises(ImageNotFoundError, match="Image not found"):
            self.resolver.by_os_slug("ubuntu-24.04")

    # ------------------------------------------------------------------
    # resolve()
    # ------------------------------------------------------------------

    def test_resolve_by_os_slug_first(self) -> None:
        """resolve() tries os_slug before ID prefix."""
        self._seed_images()
        result = self.resolver.resolve("ubuntu-24.04")
        assert result.os_slug == "ubuntu-24.04"

    def test_resolve_by_id_prefix_fallback(self) -> None:
        """resolve() falls back to ID prefix when os_slug not found."""
        self._seed_images()
        result = self.resolver.resolve("abc")
        assert result.id == "abc12345" * 8

    def test_resolve_raises_when_both_fail(self) -> None:
        self._seed_images()
        with pytest.raises(ImageNotFoundError):
            self.resolver.resolve("nonexistent")

    def test_resolve_prefers_os_slug_over_prefix(self) -> None:
        """An os_slug that's also an ID prefix should match os_slug first."""
        self.repo.upsert(
            _make_image("ubuntu-24.04" * 4, os_slug="custom-ubuntu")
        )
        self.repo.upsert(
            _make_image("otherid12345" * 4, os_slug="ubuntu-24.04")
        )
        result = self.resolver.resolve("ubuntu-24.04")
        assert result.os_slug == "ubuntu-24.04"

    # ------------------------------------------------------------------
    # resolve_many()
    # ------------------------------------------------------------------

    def test_resolve_many_all_found(self) -> None:
        self._seed_images()
        result = self.resolver.resolve_many(["ubuntu-24.04", "debian-12"])
        assert len(result.items) == 2
        assert len(result.errors) == 0
        assert result.exit_code == 0

    def test_resolve_many_partial_failures(self) -> None:
        self._seed_images()
        result = self.resolver.resolve_many(["ubuntu-24.04", "nonexistent"])
        assert len(result.items) == 1
        assert len(result.errors) == 1
        assert "nonexistent" in result.errors[0]

    def test_resolve_many_all_failures(self) -> None:
        self._seed_images()
        result = self.resolver.resolve_many(["nonexistent1", "nonexistent2"])
        assert len(result.items) == 0
        assert len(result.errors) == 2
        assert result.exit_code == 1

    def test_resolve_many_deduplicates(self) -> None:
        self._seed_images()
        result = self.resolver.resolve_many(["ubuntu-24.04", "ubuntu-24.04"])
        assert len(result.items) == 1

    def test_resolve_many_empty_list(self) -> None:
        result = self.resolver.resolve_many([])
        assert result.items == []
        assert result.errors == []
        assert result.exit_code == 0

    # ------------------------------------------------------------------
    # get_default()
    # ------------------------------------------------------------------

    def test_get_default_returns_none_when_no_default(self) -> None:
        self._seed_images()
        result = self.resolver.get_default()
        assert result is None

    def test_get_default_returns_default_image(self) -> None:
        self._seed_images()
        self.repo.set_default("abc12345" * 8)
        result = self.resolver.get_default()
        assert result is not None
        assert result.id == "abc12345" * 8
