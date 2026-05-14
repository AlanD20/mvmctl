"""Tests for ImageOperation class — API layer image management orchestration.

Covers: list_, get, inspect, set_default, remove, warm, _image_to_dict,
find_existing_image.

Follows the pattern from test_vm_operations.py: mock at point-of-use,
test delegation and error handling, not implementation details.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.image_operations import ImageOperation
from mvmctl.api.inputs._image_input import ImageInput
from mvmctl.exceptions import ImageError
from mvmctl.models import ImageItem, ImageSpec


def _make_image(
    os_slug: str = "ubuntu-24.04",
    name: str = "Ubuntu 24.04 LTS",
    is_default: bool = False,
    is_present: bool = True,
    image_id: str | None = None,
    **kwargs,
) -> ImageItem:
    defaults: dict = dict(
        id=image_id or f"img-{os_slug}-" + "x" * 55,
        os_slug=os_slug,
        os_name=name,
        arch="x86_64",
        path=f"images/{os_slug}.ext4",
        fs_type="ext4",
        minimum_rootfs_size_mib=2048,
        original_size=1024,
        is_default=is_default,
        is_present=is_present,
        pulled_at="2026-01-01T12:00:00+00:00",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        compressed_size=500,
        compression_ratio=2.0,
        compressed_format="zst",
    )
    defaults.update(kwargs)
    return ImageItem(**defaults)


class TestImageOperationList:
    """Tests for ImageOperation.list_()."""

    def test_list_all_no_inputs(self, mocker):
        """list_() with no inputs returns all local images via ImageService."""
        mock_images = [
            _make_image("ubuntu-24.04"),
            _make_image("debian-12"),
        ]
        mock_repo = MagicMock()
        mock_service = MagicMock()
        mock_service.list_local.return_value = mock_images
        mocker.patch(
            "mvmctl.api.image_operations.Database",
        )
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService", return_value=mock_service
        )

        result = ImageOperation.list_()
        assert len(result) == 2
        mock_service.list_local.assert_called_once()

    def test_list_all_with_identifiers(self, mocker):
        """list_() with ImageInput uses resolver to filter by identifiers."""
        mock_image = _make_image("ubuntu-24.04")
        mock_resolved = MagicMock()
        mock_resolved.items = [mock_image]
        mock_resolver = MagicMock()
        mock_resolver.resolve_many.return_value = mock_resolved
        mock_repo = MagicMock()
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.image_operations.ImageResolver",
            return_value=mock_resolver,
        )

        result = ImageOperation.list_(ImageInput(id=["ubuntu-24.04"]))
        assert len(result) == 1
        assert result[0].os_slug == "ubuntu-24.04"
        mock_resolver.resolve_many.assert_called_once_with(["ubuntu-24.04"])

    def test_list_empty(self, mocker):
        """list_() returns empty list when no images exist."""
        mock_repo = MagicMock()
        mock_service = MagicMock()
        mock_service.list_local.return_value = []
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService", return_value=mock_service
        )

        result = ImageOperation.list_()
        assert result == []

    def test_list_remote(self, mocker):
        """list_(remote=True) returns ImageSpec list from YAML."""
        mock_specs = [
            ImageSpec(
                id="ubuntu-24.04",
                image_type="ubuntu",
                version="24.04",
                name="Ubuntu 24.04",
                source="https://example.com/ubuntu.qcow2",
                format="qcow2",
            ),
        ]
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch(
            "mvmctl.api.image_operations.SettingsService.resolve",
            return_value="x86_64",
        )
        mocker.patch(
            "mvmctl.api.image_operations.BinaryRepository",
        )
        mocker.patch(
            "mvmctl.core.binary._service.BinaryService",
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService.load_available_images",
            return_value=mock_specs,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService.resolve_remote_sizes",
        )

        result = ImageOperation.list_(remote=True)
        assert len(result) == 1
        assert result[0].id == "ubuntu-24.04"

    def test_list_remote_resolves_sizes(self, mocker):
        """list_(remote=True) calls resolve_remote_sizes."""
        mock_specs = [
            ImageSpec(
                id="test",
                image_type="test",
                version="1",
                name="Test",
                source="https://example.com/test.qcow2",
                format="qcow2",
            )
        ]
        mock_firecracker = MagicMock()
        mock_firecracker.ci_version = "v1.10"
        mock_binary_svc = MagicMock()
        mock_binary_svc.get_default_firecracker.return_value = mock_firecracker
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch(
            "mvmctl.api.image_operations.SettingsService.resolve",
            return_value="x86_64",
        )
        mocker.patch(
            "mvmctl.api.image_operations.BinaryRepository",
        )
        mocker.patch(
            "mvmctl.core.binary._service.BinaryService",
            return_value=mock_binary_svc,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService.load_available_images",
            return_value=mock_specs,
        )
        mock_resolve_sizes = mocker.patch(
            "mvmctl.core.image._service.ImageService.resolve_remote_sizes",
        )

        ImageOperation.list_(remote=True)
        mock_resolve_sizes.assert_called_once_with(mock_specs, "v1.10")


class TestImageOperationGet:
    """Tests for ImageOperation.get()."""

    def test_get_success(self, mocker):
        """get() resolves identifier and returns single ImageItem."""
        mock_image = _make_image("ubuntu-24.04")
        mock_resolved = MagicMock()
        mock_resolved.images = [mock_image]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )

        result = ImageOperation.get(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.os_slug == "ubuntu-24.04"
        assert result.arch == "x86_64"

    def test_get_multiple_raises_error(self, mocker):
        """get() raises ImageError when multiple items resolved."""
        mock_resolved = MagicMock()
        mock_resolved.images = [_make_image("img1"), _make_image("img2")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )

        with pytest.raises(
            ImageError, match="Expected exactly one image identifier"
        ):
            ImageOperation.get(ImageInput(os_slug=["ambiguous"]))

    def test_get_delegates_to_image_request(self, mocker):
        """get() delegates resolution to ImageRequest."""
        mock_resolved = MagicMock()
        mock_resolved.images = [_make_image("ubuntu-24.04")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )

        ImageOperation.get(ImageInput(os_slug=["test"]))
        mock_request.resolve.assert_called_once()


class TestImageOperationInspect:
    """Tests for ImageOperation.inspect()."""

    def test_inspect_returns_item(self, mocker):
        """inspect() with is_json=False returns ImageItem."""
        mock_image = _make_image("ubuntu-24.04")
        mocker.patch.object(ImageOperation, "get", return_value=mock_image)

        result = ImageOperation.inspect(ImageInput(os_slug=["ubuntu-24.04"]))
        assert isinstance(result, ImageItem)
        assert result.os_slug == "ubuntu-24.04"

    def test_inspect_returns_dict(self, mocker):
        """inspect() with is_json=True returns dict representation."""
        mock_image = _make_image("ubuntu-24.04")
        mocker.patch.object(ImageOperation, "get", return_value=mock_image)

        result = ImageOperation.inspect(
            ImageInput(os_slug=["ubuntu-24.04"]), is_json=True
        )
        assert isinstance(result, dict)
        assert result["os_slug"] == "ubuntu-24.04"

    def test_inspect_calls_get(self, mocker):
        """inspect() delegates to get()."""
        mock_get = mocker.patch.object(
            ImageOperation, "get", return_value=_make_image()
        )

        ImageOperation.inspect(ImageInput(os_slug=["test"]))
        mock_get.assert_called_once()


class TestImageOperationSetDefault:
    """Tests for ImageOperation.set_default()."""

    def test_set_default_success(self, mocker):
        """set_default() resolves image and calls repo.set_default()."""
        mock_image = _make_image("ubuntu-24.04")
        mock_resolved = MagicMock()
        mock_resolved.images = [mock_image]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        result = ImageOperation.set_default(
            ImageInput(os_slug=["ubuntu-24.04"])
        )
        assert result.status == "success"
        assert result.code == "image.default_set"
        assert result.item is mock_image
        mock_repo.set_default.assert_called_once_with(mock_image.id)

    def test_set_default_multiple_raises(self, mocker):
        """set_default() raises ImageError when multiple images resolved."""
        mock_resolved = MagicMock()
        mock_resolved.images = [_make_image("img1"), _make_image("img2")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch("mvmctl.api.image_operations.Database")

        with pytest.raises(
            ImageError, match="Expected exactly one image identifier"
        ):
            ImageOperation.set_default(ImageInput(os_slug=["ambiguous"]))

    def test_set_default_audit_logged(self, mocker):
        """set_default() logs the action via AuditLog."""
        mock_image = _make_image("ubuntu-24.04")
        mock_resolved = MagicMock()
        mock_resolved.images = [mock_image]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch("mvmctl.api.image_operations.Database")
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        ImageOperation.set_default(ImageInput(os_slug=["ubuntu-24.04"]))
        mock_audit.assert_called_once()


class TestImageOperationRemove:
    """Tests for ImageOperation.remove()."""

    def _setup_remove_mocks(
        self, mocker, images: list[ImageItem]
    ) -> tuple[MagicMock, MagicMock]:
        mock_resolved = MagicMock()
        mock_resolved.images = images
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mock_image_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
        )
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch(
            "mvmctl.core.image._service.ImageService",
            return_value=mock_image_svc,
        )
        return mock_request, mock_image_svc

    def test_remove_success(self, mocker):
        """remove() resolves, resolves, and calls image_svc.remove()."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_image_svc = self._setup_remove_mocks(mocker, [mock_image])

        result = ImageOperation.remove(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.all_ok
        assert len(result.items) == 1
        assert result.items[0].status == "success"
        assert result.items[0].code == "image.removed"
        mock_image_svc.remove.assert_called_once_with(
            mock_image, force=False
        )

    def test_remove_with_force_flag(self, mocker):
        """remove() passes force=True to image_svc.remove()."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_image_svc = self._setup_remove_mocks(mocker, [mock_image])

        ImageOperation.remove(ImageInput(os_slug=["ubuntu-24.04"]), force=True)
        mock_image_svc.remove.assert_called_once_with(
            mock_image, force=True
        )

    def test_remove_controller_error(self, mocker):
        """remove() returns error OperationResult when ImageService.remove() raises."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_image_svc = self._setup_remove_mocks(mocker, [mock_image])
        mock_image_svc.remove.side_effect = ImageError(
            "Image is referenced by running VMs"
        )

        result = ImageOperation.remove(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.has_any_error
        assert result.items[0].status == "error"
        assert "referenced" in str(result.items[0].exception)

    def test_remove_multiple_images(self, mocker):
        """remove() handles multiple images in BatchResult."""
        img1 = _make_image("ubuntu-24.04")
        img2 = _make_image("debian-12")
        self._setup_remove_mocks(mocker, [img1, img2])

        result = ImageOperation.remove(
            ImageInput(os_slug=["ubuntu-24.04", "debian-12"])
        )
        assert result.all_ok
        assert len(result.items) == 2


class TestImageOperationWarm:
    """Tests for ImageOperation.warm()."""

    def _setup_warm_mocks(
        self, mocker, images: list[ImageItem]
    ) -> tuple[MagicMock, MagicMock]:
        mock_resolved = MagicMock()
        mock_resolved.images = images
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.core.image._service.ImageService", return_value=mock_service
        )
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch("mvmctl.api.image_operations.Database")
        return mock_request, mock_service

    def test_warm_success(self, mocker):
        """warm() calls svc.ensure_cached() and returns warmed paths."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_service = self._setup_warm_mocks(mocker, [mock_image])
        warm_path = Path("/cache/warm/ubuntu-24.04.ext4")
        mock_service.ensure_cached.return_value = [warm_path]

        result = ImageOperation.warm(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.status == "success"
        assert result.code == "image.warmed"
        assert len(result.item) == 1
        assert result.item[0] == warm_path
        mock_service.ensure_cached.assert_called_once_with([mock_image])

    def test_warm_calls_progress_callback(self, mocker):
        """warm() calls on_progress with running and complete events."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_service = self._setup_warm_mocks(mocker, [mock_image])
        mock_service.ensure_cached.return_value = [Path("/cache/warm/img.ext4")]

        on_progress = MagicMock()
        ImageOperation.warm(
            ImageInput(os_slug=["ubuntu-24.04"]), on_progress=on_progress
        )
        assert on_progress.call_count == 2
        calls = [call[0][0].status for call in on_progress.call_args_list]
        assert "running" in calls
        assert "complete" in calls

    def test_warm_service_failure(self, mocker):
        """warm() returns error result when svc.ensure_cached() raises."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_service = self._setup_warm_mocks(mocker, [mock_image])
        mock_service.ensure_cached.side_effect = RuntimeError("Disk full")

        result = ImageOperation.warm(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.status == "error"
        assert result.code == "image.warm_failed"
        assert "Disk full" in str(result.exception)

    def test_warm_on_progress_skipped_when_none(self, mocker):
        """warm() does not fail when on_progress is None."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_service = self._setup_warm_mocks(mocker, [mock_image])
        mock_service.ensure_cached.return_value = [Path("/cache/warm/img.ext4")]

        result = ImageOperation.warm(ImageInput(os_slug=["ubuntu-24.04"]))
        assert result.status == "success"

    def test_warm_multiple_images(self, mocker):
        """warm() handles multiple images."""
        img1 = _make_image("ubuntu-24.04")
        img2 = _make_image("debian-12")
        _, mock_service = self._setup_warm_mocks(mocker, [img1, img2])
        mock_service.ensure_cached.return_value = [
            Path("/cache/warm/1.ext4"),
            Path("/cache/warm/2.ext4"),
        ]

        result = ImageOperation.warm(
            ImageInput(os_slug=["ubuntu-24.04", "debian-12"])
        )
        assert result.status == "success"
        mock_service.ensure_cached.assert_called_once_with([img1, img2])


class TestImageOperationHelpers:
    """Tests for helper methods on ImageOperation."""

    def test_image_to_dict_includes_all_fields(self):
        """_image_to_dict() includes all relevant fields."""
        img = _make_image("ubuntu-24.04", fs_uuid="abc-123", distro="ubuntu")
        d = ImageOperation._image_to_dict(img)
        assert d["os_slug"] == "ubuntu-24.04"
        assert d["name"] == "Ubuntu 24.04 LTS"
        assert d["arch"] == "x86_64"
        assert d["fs_type"] == "ext4"
        assert d["fs_uuid"] == "abc-123"
        assert d["distro"] == "ubuntu"
        assert d["compressed_size"] == 500
        assert d["compression_ratio"] == 2.0
        assert d["minimum_rootfs_size_mib"] == 2048
        assert d["is_default"] is False
        assert d["is_present"] is True

    def test_image_to_dict_does_not_include_deleted_at(self):
        """_image_to_dict() omits deleted_at field."""
        img = _make_image("test")
        d = ImageOperation._image_to_dict(img)
        assert "deleted_at" not in d

    def test_image_to_dict_includes_distro_when_set(self):
        """_image_to_dict() includes distro field when set."""
        img = _make_image("test", distro="alpine")
        d = ImageOperation._image_to_dict(img)
        assert "distro" in d
        assert d["distro"] == "alpine"

    def test_image_to_dict_distro_defaults_to_none(self):
        """_image_to_dict() includes distro field as None when not set."""
        img = _make_image("test")
        d = ImageOperation._image_to_dict(img)
        assert "distro" in d
        assert d["distro"] is None

    def test_image_to_dict_handles_none_optionals(self):
        """_image_to_dict() handles None optional fields gracefully."""
        img = _make_image("test", fs_uuid=None, compressed_size=None)
        d = ImageOperation._image_to_dict(img)
        assert d["fs_uuid"] is None
        assert d["compressed_size"] is None

    def test_find_existing_image_found(self, mocker):
        """find_existing_image() returns item when found in repo and on disk."""
        mock_repo = MagicMock()
        mock_repo.get_by_os_slug.return_value = _make_image(
            "ubuntu-24.04", path="images/ubuntu-24.04.ext4"
        )
        images_dir = MagicMock()
        candidate = MagicMock()
        candidate.exists.return_value = True
        images_dir.__truediv__.return_value = candidate

        result = ImageOperation.find_existing_image(
            MagicMock(id="ubuntu-24.04"), images_dir, mock_repo
        )
        assert result is not None
        assert result.os_slug == "ubuntu-24.04"
        mock_repo.get_by_os_slug.assert_called_once_with("ubuntu-24.04")

    def test_find_existing_image_not_in_db(self, mocker):
        """find_existing_image() returns None when not in repo."""
        mock_repo = MagicMock()
        mock_repo.get_by_os_slug.return_value = None
        mock_repo.get.return_value = None

        result = ImageOperation.find_existing_image(
            MagicMock(id="missing"), MagicMock(), mock_repo
        )
        assert result is None

    def test_find_existing_image_not_on_disk(self, mocker):
        """find_existing_image() returns None when file missing on disk."""
        mock_repo = MagicMock()
        mock_repo.get_by_os_slug.return_value = _make_image(
            "ubuntu-24.04", path="images/ubuntu-24.04.ext4"
        )
        images_dir = MagicMock()
        candidate = MagicMock()
        candidate.exists.return_value = False
        images_dir.__truediv__.return_value = candidate

        result = ImageOperation.find_existing_image(
            MagicMock(id="ubuntu-24.04"), images_dir, mock_repo
        )
        assert result is None

    def test_find_existing_image_falls_back_to_get_by_id(self, mocker):
        """find_existing_image() falls back to repo.get() when get_by_os_slug returns None."""
        mock_repo = MagicMock()
        mock_repo.get_by_os_slug.return_value = None
        mock_repo.get.return_value = _make_image(
            "ubuntu-24.04", path="images/ubuntu-24.04.ext4"
        )
        images_dir = MagicMock()
        candidate = MagicMock()
        candidate.exists.return_value = True
        images_dir.__truediv__.return_value = candidate

        result = ImageOperation.find_existing_image(
            MagicMock(id="ubuntu-24.04"), images_dir, mock_repo
        )
        assert result is not None
        assert result.os_slug == "ubuntu-24.04"
        mock_repo.get.assert_called_once_with("ubuntu-24.04")
