"""Tests for ImageController — image lifecycle management."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.image._controller import ImageController
from mvmctl.exceptions import ImageError
from mvmctl.models import ImageItem
from mvmctl.models.vm import VMInstanceItem


def _make_vm(**overrides: object) -> VMInstanceItem:
    """Build a minimal VMInstanceItem with all required fields."""
    defaults: dict[str, object] = {
        "id": "b" * 64,
        "name": "test-vm",
        "status": "running",
        "pid": 1234,
        "ipv4": "10.0.0.1",
        "mac": "00:00:00:00:00:01",
        "network_id": "net-1",
        "tap_device": "tap0",
        "image_id": "a" * 64,
        "kernel_id": "c" * 64,
        "binary_id": "d" * 64,
        "api_socket_path": "/tmp/vm.sock",
        "config_path": "/tmp/vm.json",
        "cloud_init_mode": "off",
        "vcpu_count": 2,
        "mem_size_mib": 512,
        "disk_size_mib": 1024,
        "rootfs_path": "/tmp/rootfs.ext4",
        "rootfs_suffix": "ext4",
        "enable_pci": False,
        "enable_logging": False,
        "enable_metrics": False,
        "enable_console": False,
        "created_at": "2026-01-01T00:00:00+00:00",
        "updated_at": "2026-01-01T00:00:00+00:00",
    }
    defaults.update(overrides)
    return VMInstanceItem(**defaults)  # type: ignore[arg-type]


SAMPLE_IMAGE = ImageItem(
    id="a" * 64,
    os_slug="ubuntu-24.04",
    os_name="Ubuntu 24.04",
    arch="x86_64",
    path="/cache/images/" + "a" * 64 + ".ext4",
    fs_type="ext4",
    minimum_rootfs_size_mib=10,
    original_size=10485760,
    is_default=True,
    is_present=True,
    pulled_at="2026-01-01T00:00:00+00:00",
    created_at="2026-01-01T00:00:00+00:00",
    updated_at="2026-01-01T00:00:00+00:00",
    fs_uuid="12345678-1234-1234-1234-123456789abc",
)


class TestImageControllerInit:
    def test_with_imageitem(self):
        repo = MagicMock()
        controller = ImageController(SAMPLE_IMAGE, repo)
        assert controller._image == SAMPLE_IMAGE
        assert controller._repo is repo

    def test_get_returns_image(self):
        repo = MagicMock()
        controller = ImageController(SAMPLE_IMAGE, repo)
        assert controller.get() == SAMPLE_IMAGE


class TestImageControllerPaths:
    def test_image_path(self):
        repo = MagicMock()
        controller = ImageController(SAMPLE_IMAGE, repo)
        path = controller.image_path
        assert isinstance(path, Path)

    def test_compressed_path_default_zst(self):
        repo = MagicMock()
        image = ImageItem(
            **{**SAMPLE_IMAGE.__dict__, "compressed_format": None}
        )
        controller = ImageController(image, repo)
        compressed = controller.compressed_path
        assert compressed.suffix == ".zst"

    def test_compressed_path_with_fmt(self):
        repo = MagicMock()
        image = ImageItem(
            **{**SAMPLE_IMAGE.__dict__, "compressed_format": "gz"}
        )
        controller = ImageController(image, repo)
        compressed = controller.compressed_path
        assert compressed.suffix == ".gz"


class TestImageControllerRemovePath:
    def test_remove_path(self, tmp_path):
        repo = MagicMock()
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / ("a" * 64 + ".ext4")).write_text("image data")
        (images_dir / ("a" * 64 + ".zst")).write_text("compressed data")

        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / ("a" * 64 + "_warm.ext4")).write_text("warm data")

        with patch(
            "mvmctl.core.image._controller.CacheUtils.get_images_dir",
            return_value=images_dir,
        ):
            with patch(
                "mvmctl.core.image._controller.CacheUtils.get_warm_image_dir",
                return_value=warm_dir,
            ):
                controller = ImageController(SAMPLE_IMAGE, repo)
                removed = controller.remove_path()

        assert len(removed) > 0


class TestImageControllerRemove:
    def test_remove_without_force_raises(self):
        repo = MagicMock()

        vm = _make_vm()
        image_with_vms = ImageItem(**{**SAMPLE_IMAGE.__dict__, "vms": [vm]})
        controller = ImageController(image_with_vms, repo)

        with pytest.raises(ImageError, match="referenced by VMs"):
            controller.remove(force=False)

    def test_remove_with_force_soft_deletes(self):
        repo = MagicMock()

        vm = _make_vm()
        image_with_vms = ImageItem(**{**SAMPLE_IMAGE.__dict__, "vms": [vm]})

        with patch(
            "mvmctl.core.image._controller.ImageController.remove_path",
            return_value=[],
        ):
            controller = ImageController(image_with_vms, repo)
            controller.remove(force=True)

        repo.soft_delete.assert_called_once()

    def test_remove_without_vms_hard_deletes(self):
        repo = MagicMock()

        with patch(
            "mvmctl.core.image._controller.ImageController.remove_path",
            return_value=[],
        ):
            controller = ImageController(SAMPLE_IMAGE, repo)
            controller.remove(force=False)

        repo.delete.assert_called_once()


class TestImageControllerPruneCached:
    def test_prune_cached_removes_files(self, tmp_path):
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / "img1.ext4").write_text("data")
        (warm_dir / "img2.ext4").write_text("data")

        with patch(
            "mvmctl.core.image._controller.CacheUtils.get_warm_image_dir",
            return_value=warm_dir,
        ):
            count = ImageController.prune_cached()

        assert count == 2
        assert not warm_dir.exists() or not any(warm_dir.iterdir())

    def test_prune_cached_no_dir(self):
        with patch(
            "mvmctl.core.image._controller.CacheUtils.get_warm_image_dir",
            return_value=Path("/nonexistent"),
        ):
            count = ImageController.prune_cached()

        assert count == 0
