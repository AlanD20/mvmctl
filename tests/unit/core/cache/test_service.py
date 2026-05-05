"""Tests for CacheService — cache cleanup operations."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core.cache._service import CacheService


class TestPruneWarmImages:
    """Tests for CacheService.prune_warm_images()."""

    def test_no_warm_dir(self, monkeypatch):
        """prune_warm_images returns False when warm dir does not exist."""
        monkeypatch.setattr(
            "mvmctl.core.cache._service.CacheUtils.get_warm_image_dir",
            lambda: Path("/nonexistent"),
        )
        assert CacheService.prune_warm_images(dry_run=False) is False
        assert CacheService.prune_warm_images(dry_run=True) is False

    def test_empty_warm_dir(self, monkeypatch, tmp_path):
        """prune_warm_images returns False when warm dir is empty."""
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        monkeypatch.setattr(
            "mvmctl.core.cache._service.CacheUtils.get_warm_image_dir",
            lambda: warm_dir,
        )
        assert CacheService.prune_warm_images(dry_run=False) is False

    def test_dry_run_returns_true_without_deleting(self, monkeypatch, tmp_path):
        """prune_warm_images returns True in dry_run but does not delete files."""
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / "image1.ext4").write_text("data")
        (warm_dir / "image2.ext4").write_text("data")
        monkeypatch.setattr(
            "mvmctl.core.cache._service.CacheUtils.get_warm_image_dir",
            lambda: warm_dir,
        )

        result = CacheService.prune_warm_images(dry_run=True)

        assert result is True
        assert (warm_dir / "image1.ext4").exists()
        assert (warm_dir / "image2.ext4").exists()

    def test_actual_prune_deletes_files(self, monkeypatch, tmp_path):
        """prune_warm_images deletes files when dry_run=False."""
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / "image1.ext4").write_text("data")
        (warm_dir / "subdir").mkdir()
        (warm_dir / "subdir" / "nested").write_text("nested")
        monkeypatch.setattr(
            "mvmctl.core.cache._service.CacheUtils.get_warm_image_dir",
            lambda: warm_dir,
        )

        result = CacheService.prune_warm_images(dry_run=False)

        assert result is True
        assert not (warm_dir / "image1.ext4").exists()
        assert not (warm_dir / "subdir").exists()

    def test_prune_ignores_oserror(self, monkeypatch, tmp_path):
        """prune_warm_images handles OSError gracefully during deletion."""
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        bad_file = warm_dir / "bad.img"
        bad_file.write_text("data")

        # Make the file unlinkable
        import os

        original_unlink = os.unlink

        def _fail_unlink(path, *args, **kwargs):
            if "bad.img" in str(path):
                raise OSError("Permission denied")
            return original_unlink(path, *args, **kwargs)

        monkeypatch.setattr(os, "unlink", _fail_unlink)

        monkeypatch.setattr(
            "mvmctl.core.cache._service.CacheUtils.get_warm_image_dir",
            lambda: warm_dir,
        )

        # Should not raise despite OSError
        result = CacheService.prune_warm_images(dry_run=False)
        assert result is True
