"""Integration tests for Image API operations.

Tests exercise the complete image orchestration flow:
  import → list → get → inspect → set_default → warm → remove

Only subprocess (system-level operations like cp, dd, blkid, qemu-img)
and libguestfs are mocked. ALL orchestration logic in api/ and core/
runs unmocked.
"""

from __future__ import annotations

import subprocess
from pathlib import Path
from typing import cast
from unittest.mock import MagicMock

import pytest

from mvmctl.api import ImageImportInput, ImageInput, ImageOperation
from mvmctl.exceptions import (
    GuestfsNotAvailableError,
    ImageError,
    ImageNotFoundError,
)
from mvmctl.models import ImageItem
from mvmctl.models.result import BatchResult, OperationResult
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Helpers
# ======================================================================


class _ImageSubprocessMock:
    """Extended subprocess mock that handles image-specific tools."""

    def __init__(self) -> None:
        from tests.integration.conftest import SmartSubprocessMock

        self._base = SmartSubprocessMock()
        self.calls = self._base.calls

    def __call__(
        self, *args: object, **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        cmd = kwargs.get("args", args[0] if args else [])
        if not isinstance(cmd, list):
            cmd = []
        self.calls.append(cmd)
        cmd_str = " ".join(str(c) for c in cmd)

        if "blkid" in cmd_str and "-s" in cmd_str and "TYPE" in cmd_str:
            return subprocess.CompletedProcess(
                args=[], returncode=0, stdout="ext4", stderr=""
            )

        # Fail cp/dd when the source file does not exist so that invalid-path
        # tests actually hit error paths.
        if cmd and cmd[0] == "cp":
            src = None
            for i, part in enumerate(cmd):
                if part == "--sparse=always" and i + 1 < len(cmd):
                    src = Path(str(cmd[i + 1]))
                    break
            if src is not None and not src.exists():
                raise subprocess.CalledProcessError(
                    1,
                    cmd,
                    stderr=f"cp: cannot stat '{src}': No such file or directory",
                )

        if cmd and cmd[0] == "dd":
            src = None
            for part in cmd:
                if part.startswith("if="):
                    src = Path(part[3:])
                    break
            if src is not None and not src.exists():
                raise subprocess.CalledProcessError(
                    1,
                    cmd,
                    stderr=f"dd: failed to open '{src}': No such file or directory",
                )

        return self._base(*args, **kwargs)


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    """Apply subprocess mocks and guestfs mock for image tests."""
    from tests.integration.conftest import SmartPopenMock

    sub_mock = _ImageSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    def _raise_guestfs(*args: object, **kwargs: object) -> None:
        raise GuestfsNotAvailableError("mock")

    monkeypatch.setattr(
        "mvmctl.core.image._service.OptimizedGuestfs",
        _raise_guestfs,
    )

    return {"subprocess": sub_mock, "popen": popen_mock}


def _create_fake_raw_image(path: Path) -> None:
    """Create a fake raw ext4-like image file with non-zero content."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"\xff" * (1024 * 1024))


# ======================================================================
# Image lifecycle tests
# ======================================================================


class TestImageImport:
    """Test image import through the real API."""

    def test_import_image(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Import a raw image and verify the returned ImageItem."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "test-image.raw"
        _create_fake_raw_image(fake_path)

        result = ImageOperation.import_(
            ImageImportInput(
                name="test-image",
                source_path=fake_path,
                format="raw",
            )
        )

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        image = result.item
        assert isinstance(image, ImageItem)
        assert image.os_name == "test-image"
        assert image.os_slug == "test_image"
        assert image.is_present is True
        assert image.fs_type == "ext4"
        assert len(image.id) == 64  # SHA256 hash


class TestImageList:
    """Test image listing through the real API."""

    def test_list_images_after_import(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """After importing an image, list_ contains it."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "list-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="list-test",
                source_path=fake_path,
                format="raw",
            )
        )

        images = ImageOperation.list_()
        images = cast(list[ImageItem], images)
        slugs = [img.os_slug for img in images]
        assert "list_test" in slugs

    def test_list_images_filter_by_identifier(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """list_ with ImageInput filters to matching images."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "filter-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="filter-test",
                source_path=fake_path,
                format="raw",
            )
        )

        images = ImageOperation.list_(ImageInput(id=["filter_test"]))
        images = cast(list[ImageItem], images)
        assert len(images) == 1
        assert images[0].os_slug == "filter_test"

    def test_list_images_empty_when_no_match(self) -> None:
        """list_ with non-matching identifier returns empty list."""
        images = ImageOperation.list_(ImageInput(id=["no-such-image"]))
        assert images == []


class TestImageGet:
    """Test image retrieval through the real API."""

    def test_get_image_by_os_slug(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Get an image by its OS slug."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "get-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="get-test",
                source_path=fake_path,
                format="raw",
            )
        )

        image = ImageOperation.get(ImageInput(id=["get_test"]))
        assert isinstance(image, ImageItem)
        assert image.os_slug == "get_test"
        assert image.os_name == "get-test"

    def test_get_image_by_id_prefix(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Get an image by the first 6 chars of its ID hash."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "get-by-id.raw"
        _create_fake_raw_image(fake_path)

        result = ImageOperation.import_(
            ImageImportInput(
                name="get-by-id",
                source_path=fake_path,
                format="raw",
            )
        )
        full_id = result.item.id

        image = ImageOperation.get(ImageInput(id=[full_id[:6]]))
        assert image.id == full_id
        assert image.os_name == "get-by-id"


class TestImageInspect:
    """Test image inspect through the real API."""

    def test_inspect_returns_image_item(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """inspect without is_json returns an ImageItem."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "inspect-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="inspect-test",
                source_path=fake_path,
                format="raw",
            )
        )

        image = ImageOperation.inspect(ImageInput(id=["inspect_test"]))
        assert isinstance(image, ImageItem)
        assert image.os_slug == "inspect_test"

    def test_inspect_returns_dict_when_json(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """inspect with is_json=True returns a dict."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "inspect-json.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="inspect-json",
                source_path=fake_path,
                format="raw",
            )
        )

        result = ImageOperation.inspect(
            ImageInput(id=["inspect_json"]), is_json=True
        )
        assert isinstance(result, dict)
        assert result["os_slug"] == "inspect_json"
        assert result["os_name"] == "inspect-json"
        assert "id" in result
        assert "fs_type" in result


class TestImageSetDefault:
    """Test setting an image as default."""

    def test_set_default_marks_image(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """set_default sets is_default=True on the target image."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "default-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="default-test",
                source_path=fake_path,
                format="raw",
            )
        )

        ImageOperation.set_default(ImageInput(id=["default_test"]))

        image = ImageOperation.get(ImageInput(id=["default_test"]))
        assert bool(image.is_default) is True


class TestImageWarm:
    """Test image warming (cache pre-decompression)."""

    def test_warm_creates_cached_file(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """warm decompresses the image to the warm cache."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "warm-test.raw"
        _create_fake_raw_image(fake_path)

        result = ImageOperation.import_(
            ImageImportInput(
                name="warm-test",
                source_path=fake_path,
                format="raw",
            )
        )
        image = result.item

        warmed_paths = ImageOperation.warm(ImageInput(id=[image.os_slug]))
        assert isinstance(warmed_paths, OperationResult)
        assert len(warmed_paths.item) == 1
        warmed_path = warmed_paths.item[0]
        assert warmed_path.exists()
        assert warmed_path.name == f"{image.id}.ext4"


class TestImageRemove:
    """Test image removal through the real API."""

    def test_remove_image(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Import then remove an image; it should be gone."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "remove-test.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="remove-test",
                source_path=fake_path,
                format="raw",
            )
        )

        ImageOperation.remove(ImageInput(id=["remove_test"]))

        with pytest.raises(ImageNotFoundError):
            ImageOperation.get(ImageInput(id=["remove_test"]))

    def test_remove_image_cleans_files(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Removing an image also removes its files from disk."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "remove-files.raw"
        _create_fake_raw_image(fake_path)

        result = ImageOperation.import_(
            ImageImportInput(
                name="remove-files",
                source_path=fake_path,
                format="raw",
            )
        )
        image = result.item

        images_dir = CacheUtils.get_images_dir()
        image_file = images_dir / image.path
        assert image_file.exists()

        ImageOperation.remove(ImageInput(id=["remove_files"]))

        assert not image_file.exists()


class TestImageEdgeCases:
    """Test edge cases and error handling."""

    def test_get_nonexistent_image(self) -> None:
        """Getting a non-existent image raises ImageNotFoundError."""
        with pytest.raises(ImageNotFoundError):
            ImageOperation.get(ImageInput(id=["no-such-image"]))

    def test_remove_nonexistent_image(self) -> None:
        """Removing a non-existent image raises ImageNotFoundError."""
        with pytest.raises(ImageNotFoundError):
            ImageOperation.remove(ImageInput(id=["no-such-image"]))

    def test_import_invalid_path(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Importing a non-existent path raises ImageError."""
        _setup_mocks(monkeypatch)
        fake_path = Path("/tmp/nonexistent-image.raw")

        with pytest.raises(ImageError):
            ImageOperation.import_(
                ImageImportInput(
                    name="invalid",
                    source_path=fake_path,
                    format="raw",
                )
            )

    def test_remove_image_referenced_by_vms_without_force(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Removing an image referenced by VMs without force raises ImageError."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "ref-vm.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="ref-vm",
                source_path=fake_path,
                format="raw",
            )
        )

        def _mock_enrich(
            _self: object, images: list[ImageItem]
        ) -> list[ImageItem]:
            for img in images:
                if img.os_slug == "ref_vm":
                    mock_vm = MagicMock()
                    mock_vm.name = "test-vm"
                    img.vms = [mock_vm]
            return images

        monkeypatch.setattr(
            "mvmctl.core.image._resolver.ImageResolver._enrich",
            _mock_enrich,
        )

        result = ImageOperation.remove(ImageInput(id=["ref_vm"]), force=False)
        assert isinstance(result, BatchResult)
        assert result.has_any_error

    def test_remove_image_referenced_by_vms_with_force(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Removing an image referenced by VMs with force succeeds."""
        _setup_mocks(monkeypatch)
        fake_path = tmp_path / "ref-vm-force.raw"
        _create_fake_raw_image(fake_path)

        ImageOperation.import_(
            ImageImportInput(
                name="ref-vm-force",
                source_path=fake_path,
                format="raw",
            )
        )

        def _mock_enrich(
            _self: object, images: list[ImageItem]
        ) -> list[ImageItem]:
            for img in images:
                if img.os_slug == "ref_vm_force":
                    mock_vm = MagicMock()
                    mock_vm.name = "test-vm"
                    img.vms = [mock_vm]
            return images

        monkeypatch.setattr(
            "mvmctl.core.image._resolver.ImageResolver._enrich",
            _mock_enrich,
        )

        ImageOperation.remove(ImageInput(id=["ref_vm_force"]), force=True)

        with pytest.raises(ImageNotFoundError):
            ImageOperation.get(ImageInput(id=["ref_vm_force"]))
