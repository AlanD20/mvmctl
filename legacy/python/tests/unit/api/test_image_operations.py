"""Tests for ImageOperation class — API layer image management orchestration.

Covers: list_, get, inspect, set_default, remove, warm, to_dict,
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
from mvmctl.models import ImageItem, ImageVersion


def _make_image(
    type: str = "ubuntu-24.04",
    name: str = "Ubuntu 24.04 LTS",
    is_default: bool = False,
    is_present: bool = True,
    image_id: str | None = None,
    **kwargs,
) -> ImageItem:
    defaults: dict = dict(
        id=image_id or f"img-{type}-" + "x" * 55,
        type=type,
        name=name,
        arch="x86_64",
        path=f"images/{type}.ext4",
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
    """Tests for ImageOperation.list_all()."""

    def test_list_all_no_inputs(self, mocker):
        """list_all() with no inputs returns all local images via ImageService."""
        mock_images = [
            _make_image("ubuntu-24.04"),
            _make_image("debian-12"),
        ]
        mock_repo = MagicMock()
        mock_service = MagicMock()
        mock_service.list_all.return_value = mock_images
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

        result = ImageOperation.list_all()
        assert len(result) == 2
        mock_service.list_all.assert_called_once()

    def test_list_all_with_identifiers(self, mocker):
        """list_all() with ImageInput uses resolver to filter by identifiers."""
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

        result = ImageOperation.list_all(ImageInput(id=["ubuntu-24.04"]))
        assert len(result) == 1
        assert result[0].type == "ubuntu-24.04"
        mock_resolver.resolve_many.assert_called_once_with(["ubuntu-24.04"])

    def test_list_empty(self, mocker):
        """list_all() returns empty list when no images exist."""
        mock_repo = MagicMock()
        mock_service = MagicMock()
        mock_service.list_all.return_value = []
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService", return_value=mock_service
        )

        result = ImageOperation.list_all()
        assert result == []

    def test_list_remote(self, mocker):
        """list_all(remote=True) returns ImageVersion list from HttpDirVersionResolver."""
        mock_versions = [
            ImageVersion(
                version="24.04",
                codename="noble",
                type="ubuntu",
                download_url="https://example.com/ubuntu.qcow2",
                sha256_url=None,
                format="qcow2",
                display_name="Ubuntu 24.04 LTS",
                type_name="Ubuntu",
            ),
        ]
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch(
            "mvmctl.api.image_operations.SettingsService.resolve",
            side_effect=lambda db, cat, key: (
                "x86_64" if key == "arch" else "3600"
            ),
        )
        mocker.patch(
            "mvmctl.api.image_operations.BinaryRepository",
        )
        mocker.patch(
            "mvmctl.core.binary._service.BinaryService",
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService.load_image_types_config",
            return_value=[{"type": "ubuntu"}],
        )
        mocker.patch(
            "mvmctl.core.image._version_resolver.HttpDirVersionResolver.resolve",
            return_value={"ubuntu": mock_versions},
        )

        result = ImageOperation.list_all(remote=True)
        assert len(result) == 1
        assert result[0].type == "ubuntu"
        assert result[0].version == "24.04"

    def test_list_remote_resolves_sizes(self, mocker):
        """list_all(remote=True) delegates to HttpDirVersionResolver with ci_version."""
        mock_versions = [
            ImageVersion(
                version="24.04",
                codename="noble",
                type="ubuntu",
                download_url="https://example.com/ubuntu.qcow2",
                sha256_url=None,
                format="qcow2",
            ),
        ]
        mock_firecracker = MagicMock()
        mock_firecracker.ci_version = "v1.10"
        mock_binary_svc = MagicMock()
        mock_binary_svc.get_default_firecracker.return_value = mock_firecracker
        mocker.patch("mvmctl.api.image_operations.Database")
        mocker.patch("mvmctl.api.image_operations.ImageRepository")
        mocker.patch(
            "mvmctl.api.image_operations.SettingsService.resolve",
            side_effect=lambda db, cat, key: (
                "x86_64" if key == "arch" else "3600"
            ),
        )
        mocker.patch(
            "mvmctl.api.image_operations.BinaryRepository",
        )
        mocker.patch(
            "mvmctl.core.binary._service.BinaryService",
            return_value=mock_binary_svc,
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService.load_image_types_config",
            return_value=[{"type": "ubuntu"}],
        )
        mock_resolver = mocker.patch(
            "mvmctl.core.image._version_resolver.HttpDirVersionResolver.resolve",
            return_value={"ubuntu": mock_versions},
        )

        ImageOperation.list_all(remote=True)
        mock_resolver.assert_called_once()
        assert mock_resolver.call_args[1]["ci_version"] == "v1.10"


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

        result = ImageOperation.get(ImageInput(type=["ubuntu-24.04"]))
        assert result.type == "ubuntu-24.04"
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
            ImageOperation.get(ImageInput(type=["ambiguous"]))

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

        ImageOperation.get(ImageInput(type=["test"]))
        mock_request.resolve.assert_called_once()


class TestImageOperationInspect:
    """Tests for ImageOperation.inspect()."""

    def test_inspect_returns_dict(self, mocker):
        """inspect() returns grouped dict representation."""
        mock_image = _make_image("ubuntu-24.04")
        mocker.patch.object(ImageOperation, "get", return_value=mock_image)

        result = ImageOperation.inspect(ImageInput(type=["ubuntu-24.04"]))
        assert isinstance(result, dict)
        assert result["image"]["type"] == "ubuntu-24.04"
        assert "storage" in result
        assert "compression" in result
        assert "requirements" in result
        assert "timestamps" in result

    def test_inspect_calls_get(self, mocker):
        """inspect() delegates to get()."""
        mock_get = mocker.patch.object(
            ImageOperation, "get", return_value=_make_image()
        )

        ImageOperation.inspect(ImageInput(type=["test"]))
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

        result = ImageOperation.set_default(ImageInput(type=["ubuntu-24.04"]))
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
            ImageOperation.set_default(ImageInput(type=["ambiguous"]))

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

        ImageOperation.set_default(ImageInput(type=["ubuntu-24.04"]))
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

        result = ImageOperation.remove(ImageInput(type=["ubuntu-24.04"]))
        assert result.all_ok
        assert len(result.items) == 1
        assert result.items[0].status == "success"
        assert result.items[0].code == "image.removed"
        mock_image_svc.remove.assert_called_once_with(mock_image, force=False)

    def test_remove_with_force_flag(self, mocker):
        """remove() passes force=True to image_svc.remove()."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_image_svc = self._setup_remove_mocks(mocker, [mock_image])

        ImageOperation.remove(ImageInput(type=["ubuntu-24.04"]), force=True)
        mock_image_svc.remove.assert_called_once_with(mock_image, force=True)

    def test_remove_controller_error(self, mocker):
        """remove() returns error OperationResult when ImageService.remove() raises."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_image_svc = self._setup_remove_mocks(mocker, [mock_image])
        mock_image_svc.remove.side_effect = ImageError(
            "Image is referenced by running VMs"
        )

        result = ImageOperation.remove(ImageInput(type=["ubuntu-24.04"]))
        assert result.has_any_error
        assert result.items[0].status == "error"
        assert "referenced" in str(result.items[0].exception)

    def test_remove_multiple_images(self, mocker):
        """remove() handles multiple images in BatchResult."""
        img1 = _make_image("ubuntu-24.04")
        img2 = _make_image("debian-12")
        self._setup_remove_mocks(mocker, [img1, img2])

        result = ImageOperation.remove(
            ImageInput(type=["ubuntu-24.04", "debian-12"])
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

        result = ImageOperation.warm(ImageInput(type=["ubuntu-24.04"]))
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
            ImageInput(type=["ubuntu-24.04"]), on_progress=on_progress
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

        result = ImageOperation.warm(ImageInput(type=["ubuntu-24.04"]))
        assert result.status == "error"
        assert result.code == "image.warm_failed"
        assert "Disk full" in str(result.exception)

    def test_warm_on_progress_skipped_when_none(self, mocker):
        """warm() does not fail when on_progress is None."""
        mock_image = _make_image("ubuntu-24.04")
        _, mock_service = self._setup_warm_mocks(mocker, [mock_image])
        mock_service.ensure_cached.return_value = [Path("/cache/warm/img.ext4")]

        result = ImageOperation.warm(ImageInput(type=["ubuntu-24.04"]))
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
            ImageInput(type=["ubuntu-24.04", "debian-12"])
        )
        assert result.status == "success"
        mock_service.ensure_cached.assert_called_once_with([img1, img2])


class TestImageOperationHelpers:
    """Tests for ImageItem.to_dict() helper."""

    def test_to_dict_includes_all_fields(self):
        """to_dict() includes all relevant fields."""
        img = _make_image("ubuntu-24.04", fs_uuid="abc-123", distro="ubuntu")
        d = img.to_dict()
        assert d["type"] == "ubuntu-24.04"
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

    def test_to_dict_does_not_include_deleted_at(self):
        """to_dict() omits deleted_at field."""
        img = _make_image("test")
        d = img.to_dict()
        assert "deleted_at" not in d

    def test_to_dict_includes_distro_when_set(self):
        """to_dict() includes distro field when set."""
        img = _make_image("test", distro="alpine")
        d = img.to_dict()
        assert "distro" in d
        assert d["distro"] == "alpine"

    def test_to_dict_distro_defaults_to_none(self):
        """to_dict() includes distro field as None when not set."""
        img = _make_image("test")
        d = img.to_dict()
        assert "distro" in d
        assert d["distro"] is None

    def test_to_dict_handles_none_optionals(self):
        """to_dict() handles None optional fields gracefully."""
        img = _make_image("test", fs_uuid=None, compressed_size=None)
        d = img.to_dict()
        assert d["fs_uuid"] is None
        assert d["compressed_size"] is None

    def test_find_existing_image_found(self, mocker):
        """find_existing_image() returns item when found in repo and on disk."""
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = _make_image(
            "ubuntu-24.04", path="/home/user/cache/images/ubuntu-24.04.ext4"
        )
        mocker.patch("pathlib.Path.exists", return_value=True)

        spec = MagicMock(id="ubuntu-24.04")
        spec.type = "ubuntu-24.04"
        result = ImageOperation.find_existing_image(spec, MagicMock(), mock_repo)
        assert result is not None
        assert result.type == "ubuntu-24.04"
        mock_repo.get_by_type.assert_called_once_with("ubuntu-24.04")

    def test_find_existing_image_not_in_db(self, mocker):
        """find_existing_image() returns None when not in repo."""
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = None
        mock_repo.get_by_version_and_type.return_value = None

        spec = MagicMock(id="missing")
        spec.type = "missing"
        spec.version = "1.0"
        result = ImageOperation.find_existing_image(
            spec, MagicMock(), mock_repo
        )
        assert result is None

    def test_find_existing_image_not_on_disk(self, mocker):
        """find_existing_image() returns None when file missing on disk."""
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = _make_image(
            "ubuntu-24.04", path="images/ubuntu-24.04.ext4"
        )
        images_dir = MagicMock()
        candidate = MagicMock()
        candidate.exists.return_value = False
        images_dir.__truediv__.return_value = candidate

        spec = MagicMock(id="ubuntu-24.04")
        spec.type = "ubuntu-24.04"
        result = ImageOperation.find_existing_image(spec, images_dir, mock_repo)
        assert result is None

    def test_find_existing_image_falls_back_to_get_by_id(self, mocker):
        """find_existing_image() falls back to get_by_version_and_type when get_by_type returns None."""
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = None
        mock_repo.get_by_version_and_type.return_value = _make_image(
            "ubuntu-24.04", path="/home/user/cache/images/ubuntu-24.04.ext4"
        )
        mocker.patch("pathlib.Path.exists", return_value=True)

        spec = MagicMock(id="ubuntu-24.04")
        spec.type = "ubuntu-24.04"
        spec.version = "latest"
        result = ImageOperation.find_existing_image(spec, MagicMock(), mock_repo)
        assert result is not None
        assert result.type == "ubuntu-24.04"
        mock_repo.get_by_version_and_type.assert_called_once_with(
            "latest", "ubuntu-24.04"
        )
