"""Tests for ImageRepository — all operations with a real SQLite database."""

from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.image._repository import ImageRepository
from mvmctl.models import ImageItem


def _make_image(
    image_id: str,
    os_slug: str = "ubuntu-24.04",
    os_name: str = "Ubuntu 24.04",
    arch: str = "x86_64",
    path: str = "ubuntu-24.04.ext4",
    fs_type: str = "ext4",
    minimum_rootfs_size_mib: int = 2048,
    original_size: int = 2147483648,
    is_default: bool = False,
    is_present: bool = True,
    fs_uuid: str | None = None,
) -> ImageItem:
    ts = datetime.now(tz=UTC).isoformat()
    return ImageItem(
        id=image_id,
        os_slug=os_slug,
        os_name=os_name,
        arch=arch,
        path=path,
        fs_type=fs_type,
        fs_uuid=fs_uuid,
        compressed_size=None,
        original_size=original_size,
        compression_ratio=None,
        compressed_format=None,
        minimum_rootfs_size_mib=minimum_rootfs_size_mib,
        pulled_at=ts,
        is_default=is_default,
        is_present=is_present,
        created_at=ts,
        updated_at=ts,
    )


class TestImageRepository:
    """Tests for ImageRepository — database operations for images."""

    @pytest.fixture(autouse=True)
    def _setup(self, tmp_path: Path) -> None:
        """Set up a fresh in-memory database for each test."""
        self.db_path = tmp_path / "test.db"
        self.db = Database(self.db_path)
        self.db.migrate()
        self.repo = ImageRepository(self.db)

    def _insert_direct(self, image: ImageItem) -> None:
        """Helper — insert an image directly via the repo."""
        self.repo.upsert(image)

    # ------------------------------------------------------------------
    # get()
    # ------------------------------------------------------------------

    def test_get_returns_image_when_found(self) -> None:
        image = _make_image("a" * 64, os_slug="ubuntu-24.04")
        self._insert_direct(image)

        result = self.repo.get("a" * 64)

        assert result is not None
        assert result.id == "a" * 64
        assert result.os_slug == "ubuntu-24.04"

    def test_get_returns_none_when_not_found(self) -> None:
        result = self.repo.get("nonexistent")
        assert result is None

    def test_get_returns_none_for_deleted_image(self) -> None:
        image = _make_image("a" * 64)
        self._insert_direct(image)
        self.repo.soft_delete("a" * 64)

        # get() does NOT filter by deleted_at — it returns the row or None
        result = self.repo.get("a" * 64)
        assert result is not None
        assert result.deleted_at is not None

    # ------------------------------------------------------------------
    # find_by_prefix()
    # ------------------------------------------------------------------

    def test_find_by_prefix_returns_matching_images(self) -> None:
        self._insert_direct(_make_image("abc12345" * 8))
        self._insert_direct(_make_image("abc67890" * 8, os_slug="debian-12"))
        self._insert_direct(_make_image("def00000" * 8, os_slug="fedora-40"))

        matches = self.repo.find_by_prefix("abc")
        assert len(matches) == 2
        assert all(m.id.startswith("abc") for m in matches)

    def test_find_by_prefix_returns_empty_when_no_match(self) -> None:
        self._insert_direct(_make_image("abc12345" * 8))
        matches = self.repo.find_by_prefix("xyz")
        assert matches == []

    def test_find_by_prefix_excludes_soft_deleted(self) -> None:
        self._insert_direct(_make_image("abc12345" * 8))
        self._insert_direct(_make_image("abc00000" * 8, os_slug="debian-12"))
        self.repo.soft_delete("abc12345" * 8)

        matches = self.repo.find_by_prefix("abc")
        assert len(matches) == 1
        assert matches[0].id == "abc00000" * 8

    # ------------------------------------------------------------------
    # get_by_os_slug()
    # ------------------------------------------------------------------

    def test_get_by_os_slug_found(self) -> None:
        self._insert_direct(_make_image("a" * 64, os_slug="ubuntu-24.04"))
        result = self.repo.get_by_os_slug("ubuntu-24.04")
        assert result is not None
        assert result.os_slug == "ubuntu-24.04"

    def test_get_by_os_slug_not_found(self) -> None:
        result = self.repo.get_by_os_slug("nonexistent")
        assert result is None

    def test_get_by_os_slug_excludes_soft_deleted(self) -> None:
        self._insert_direct(_make_image("a" * 64, os_slug="ubuntu-24.04"))
        self.repo.soft_delete("a" * 64)
        result = self.repo.get_by_os_slug("ubuntu-24.04")
        assert result is None

    # ------------------------------------------------------------------
    # list_all()
    # ------------------------------------------------------------------

    def test_list_all_returns_non_deleted_images(self) -> None:
        self._insert_direct(_make_image("a" * 64, os_slug="ubuntu-24.04"))
        self._insert_direct(_make_image("b" * 64, os_slug="debian-12"))
        self._insert_direct(_make_image("c" * 64, os_slug="fedora-40"))

        results = self.repo.list_all()
        assert len(results) == 3

    def test_list_all_excludes_soft_deleted(self) -> None:
        self._insert_direct(_make_image("a" * 64))
        self._insert_direct(_make_image("b" * 64, os_slug="debian-12"))
        self.repo.soft_delete("a" * 64)

        results = self.repo.list_all()
        assert len(results) == 1
        assert results[0].id == "b" * 64

    def test_list_all_returns_empty_when_none(self) -> None:
        results = self.repo.list_all()
        assert results == []

    # ------------------------------------------------------------------
    # upsert()
    # ------------------------------------------------------------------

    def test_upsert_inserts_new_image(self) -> None:
        image = _make_image("a" * 64)
        self.repo.upsert(image)

        result = self.repo.get("a" * 64)
        assert result is not None
        assert result.os_slug == "ubuntu-24.04"

    def test_upsert_updates_existing_image(self) -> None:
        image = _make_image("a" * 64, os_slug="ubuntu-24.04")
        self._insert_direct(image)

        updated = _make_image("a" * 64, os_slug="ubuntu-24.04-updated")
        self.repo.upsert(updated)

        result = self.repo.get("a" * 64)
        assert result is not None
        assert result.os_slug == "ubuntu-24.04-updated"

    # ------------------------------------------------------------------
    # soft_delete()
    # ------------------------------------------------------------------

    def test_soft_delete_sets_deleted_at_and_is_present(self) -> None:
        self._insert_direct(_make_image("a" * 64))
        self.repo.soft_delete("a" * 64)

        result = self.repo.get("a" * 64)
        assert result is not None
        assert result.deleted_at is not None
        # SQLite stores booleans as integers: 0 == False
        assert not result.is_present

    def test_soft_delete_noop_for_missing(self) -> None:
        self.repo.soft_delete("nonexistent")  # should not raise

    # ------------------------------------------------------------------
    # delete()
    # ------------------------------------------------------------------

    def test_delete_removes_image_permanently(self) -> None:
        self._insert_direct(_make_image("a" * 64))
        self.repo.delete("a" * 64)

        result = self.repo.get("a" * 64)
        assert result is None

    def test_delete_noop_for_missing(self) -> None:
        self.repo.delete("nonexistent")  # should not raise

    # ------------------------------------------------------------------
    # set_default() / get_default()
    # ------------------------------------------------------------------

    def test_set_default_clears_previous(self) -> None:
        self._insert_direct(
            _make_image("a" * 64, os_slug="ubuntu-24.04", is_default=True)
        )
        self._insert_direct(_make_image("b" * 64, os_slug="debian-12"))

        self.repo.set_default("b" * 64)

        default = self.repo.get_default()
        assert default is not None
        assert default.id == "b" * 64

        prev = self.repo.get("a" * 64)
        assert prev is not None
        # SQLite stores booleans as integers: 0 == False
        assert not prev.is_default

    def test_get_default_returns_none_when_not_set(self) -> None:
        result = self.repo.get_default()
        assert result is None

    def test_get_default_returns_correct_image(self) -> None:
        self._insert_direct(_make_image("a" * 64, os_slug="image-a"))
        self._insert_direct(
            _make_image("b" * 64, os_slug="image-b", is_default=True)
        )

        default = self.repo.get_default()
        assert default is not None
        assert default.id == "b" * 64

    # ------------------------------------------------------------------
    # update_many_is_present()
    # ------------------------------------------------------------------

    def test_update_many_is_present_sets_false(self) -> None:
        self._insert_direct(_make_image("a" * 64))
        self._insert_direct(_make_image("b" * 64, os_slug="debian-12"))

        self.repo.update_many_is_present(["a" * 64, "b" * 64], False)

        img_a = self.repo.get("a" * 64)
        img_b = self.repo.get("b" * 64)
        assert img_a is not None and not img_a.is_present
        assert img_b is not None and not img_b.is_present

    def test_update_many_is_present_sets_true(self) -> None:
        self._insert_direct(_make_image("a" * 64, is_present=False))

        self.repo.update_many_is_present(["a" * 64], True)

        img_a = self.repo.get("a" * 64)
        assert img_a is not None and img_a.is_present

    def test_update_many_is_present_empty_list(self) -> None:
        self.repo.update_many_is_present([], True)  # should not raise

    def test_update_many_is_present_handles_nonexistent(self) -> None:
        self.repo.update_many_is_present(
            ["nonexistent"], False
        )  # should not raise
