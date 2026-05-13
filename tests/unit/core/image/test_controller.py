"""Tests for ImageController — image lifecycle management (adapted for ImageService)."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.image._service import ImageService
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


class TestImageServiceRemovePath:
    """Tests for ImageService._remove_image_files()."""

    def test_remove_path(self, tmp_path):
        """_remove_image_files() removes image files from disk."""
        repo = MagicMock()
        images_dir = tmp_path / "images"
        images_dir.mkdir(parents=True)
        (images_dir / ("a" * 64 + ".ext4")).write_text("image data")
        (images_dir / ("a" * 64 + ".zst")).write_text("compressed data")

        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / ("a" * 64 + "_warm.ext4")).write_text("warm data")

        with patch(
            "mvmctl.utils.common.CacheUtils.get_images_dir",
            return_value=images_dir,
        ):
            with patch(
                "mvmctl.utils.common.CacheUtils.get_warm_image_dir",
                return_value=warm_dir,
            ):
                service = ImageService(repo)
                removed = service._remove_image_files(SAMPLE_IMAGE)

        assert len(removed) > 0


class TestImageServiceRemove:
    """Tests for ImageService.remove()."""

    def test_remove_without_force_raises(self):
        """remove() raises ImageError when VMs reference image and force=False."""
        repo = MagicMock()
        vm = _make_vm()
        image_with_vms = ImageItem(**{**SAMPLE_IMAGE.__dict__, "vms": [vm]})

        with patch(
            "mvmctl.core.image._resolver.ImageResolver.enrich",
            return_value=[image_with_vms],
        ):
            service = ImageService(repo)
            with pytest.raises(ImageError, match="referenced by VMs"):
                service.remove(image_with_vms, force=False)

    def test_remove_with_force_soft_deletes(self):
        """remove() soft-deletes when VMs reference image and force=True."""
        repo = MagicMock()
        vm = _make_vm()
        image_with_vms = ImageItem(**{**SAMPLE_IMAGE.__dict__, "vms": [vm]})

        with patch(
            "mvmctl.core.image._resolver.ImageResolver.enrich",
            return_value=[image_with_vms],
        ):
            service = ImageService(repo)
            service.remove(image_with_vms, force=True)

        repo.soft_delete.assert_called_once()

    def test_remove_without_vms_hard_deletes(self):
        """remove() hard-deletes when no VMs reference image."""
        repo = MagicMock()
        no_vms_image = ImageItem(**{**SAMPLE_IMAGE.__dict__, "vms": []})

        with patch(
            "mvmctl.core.image._resolver.ImageResolver.enrich",
            return_value=[no_vms_image],
        ):
            service = ImageService(repo)
            service.remove(no_vms_image, force=False)

        repo.delete.assert_called_once()


class TestImageServicePruneCached:
    """Tests for ImageController.prune_cached() — static method kept in ImageController."""

    def test_prune_cached_removes_files(self, tmp_path):
        warm_dir = tmp_path / "warm"
        warm_dir.mkdir()
        (warm_dir / "img1.ext4").write_text("data")
        (warm_dir / "img2.ext4").write_text("data")

        with patch(
            "mvmctl.core.image._controller.CacheUtils.get_warm_image_dir",
            return_value=warm_dir,
        ):
            from mvmctl.core.image._controller import ImageController

            count = ImageController.prune_cached()

        assert count == 2

    def test_prune_cached_no_dir(self, tmp_path):
        nonexistent = tmp_path / "does-not-exist"
        with patch(
            "mvmctl.core.image._controller.CacheUtils.get_warm_image_dir",
            return_value=nonexistent,
        ):
            from mvmctl.core.image._controller import ImageController

            count = ImageController.prune_cached()

        assert count == 0
