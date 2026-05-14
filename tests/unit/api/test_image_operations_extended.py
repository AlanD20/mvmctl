"""Extended tests for ImageOperation — covering pull, import_, and edge cases."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.image_operations import ImageOperation
from mvmctl.api.inputs._image_acquire_input import (
    ImageImportInput,
    ImagePullInput,
)
from mvmctl.api.inputs._image_input import ImageInput
from mvmctl.exceptions import ImageAcquireError, ImageError
from mvmctl.models import ImageItem, ImageSpec
from mvmctl.models.result import OperationResult


def _make_image(
    type: str = "ubuntu-24.04",
    name: str = "Ubuntu 24.04 LTS",
    is_default: bool = False,
    is_present: bool = True,
    image_id: str | None = None,
    **kwargs: object,
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


def _make_op_result(
    status: str = "success", code: str = "ok", item: object = None
) -> MagicMock:
    r = MagicMock(spec=OperationResult)
    r.status = status
    r.code = code
    r.message = ""
    r.item = item
    r.is_ok = status in ("success", "skipped", "warning")
    r.is_error = status in ("error", "failure")
    return r


def _setup_pull_mocks(
    mocker,
    spec_id: str = "ubuntu-24.04",
    existing_image: ImageItem | None = None,
    force: bool = False,
    default_firecracker: object = None,
) -> dict[str, MagicMock]:
    deps: dict[str, MagicMock] = {}

    mock_db = MagicMock()
    mocker.patch("mvmctl.api.image_operations.Database", return_value=mock_db)

    mock_repo = MagicMock()
    mock_repo.get_by_type.return_value = existing_image
    mocker.patch(
        "mvmctl.api.image_operations.ImageRepository", return_value=mock_repo
    )
    deps["repo"] = mock_repo

    mock_resolved = MagicMock()
    mock_resolved.output_dir = Path("/tmp/images")
    mock_resolved.force = force
    mock_resolved.partition = None
    mock_resolved.disabled_detectors = None
    mock_resolved.skip_optimization = False
    mock_resolved.set_default = True
    mock_resolved.arch = "x86_64"
    mock_request = MagicMock()
    mock_request.resolve_pull.return_value = mock_resolved
    mocker.patch(
        "mvmctl.api.image_operations.ImageAcquireRequest",
        return_value=mock_request,
    )
    deps["request"] = mock_request
    deps["resolved"] = mock_resolved

    mock_spec = MagicMock(spec=ImageSpec)
    mock_spec.id = spec_id
    mock_spec.source = "https://example.com/img.qcow2"
    mocker.patch(
        "mvmctl.core.image._service.ImageService.get_specs_for",
        return_value=[mock_spec],
    )
    deps["spec"] = mock_spec

    mock_binary_repo = MagicMock()
    mocker.patch(
        "mvmctl.api.image_operations.BinaryRepository",
        return_value=mock_binary_repo,
    )
    mock_binary_svc = MagicMock()
    mock_binary_svc.get_default_firecracker.return_value = default_firecracker
    mocker.patch(
        "mvmctl.core.binary._service.BinaryService",
        return_value=mock_binary_svc,
    )
    deps["binary_svc"] = mock_binary_svc

    mocker.patch(
        "mvmctl.api.image_operations.HashGenerator.image",
        return_value="hash" + "x" * 60,
    )

    mock_image_svc = MagicMock()
    mocker.patch(
        "mvmctl.core.image._service.ImageService",
        return_value=mock_image_svc,
    )
    deps["image_svc"] = mock_image_svc

    deps["db"] = mock_db
    return deps


class TestImageOperationPull:
    """Tests for ImageOperation.pull()."""

    def test_pull_early_return_image_exists(self, mocker):
        existing = _make_image(type="ubuntu-24.04")
        _setup_pull_mocks(mocker, existing_image=existing, force=False)

        mock_path = MagicMock()
        mock_path.exists.return_value = True
        mock_images_dir = MagicMock()
        mock_images_dir.__truediv__.return_value = mock_path
        mocker.patch(
            "mvmctl.api.image_operations.CacheUtils.get_images_dir",
            return_value=mock_images_dir,
        )

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        assert result.status == "skipped"
        assert result.code == "image.already_present"
        assert result.item is existing

    def test_pull_force_re_pull_despite_existing(self, mocker):
        existing = _make_image(type="ubuntu-24.04", image_id="old-" + "x" * 61)
        deps = _setup_pull_mocks(mocker, existing_image=existing, force=True)

        mock_download_path = Path("/tmp/images/downloaded.qcow2")
        mock_extracted_path = Path("/tmp/images/extracted.ext4")
        deps["image_svc"].download_image.return_value = mock_download_path
        deps["image_svc"].extract_image.return_value = mock_extracted_path

        new_item = _make_image(type="ubuntu-24.04", image_id="new-" + "x" * 60)
        deps["image_svc"].optimize_image.return_value = new_item

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04", force=True)
        )
        assert result.status == "success"
        assert result.code == "image.acquired"

    def test_pull_successful_download(self, mocker):
        deps = _setup_pull_mocks(mocker, existing_image=None)

        mock_download_path = Path("/tmp/images/downloaded.qcow2")
        mock_extracted_path = Path("/tmp/images/extracted.ext4")
        deps["image_svc"].download_image.return_value = mock_download_path
        deps["image_svc"].extract_image.return_value = mock_extracted_path

        new_item = _make_image(type="ubuntu-24.04")
        deps["image_svc"].optimize_image.return_value = new_item

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        assert result.status == "success"
        assert result.code == "image.acquired"
        deps["image_svc"].download_image.assert_called_once()
        deps["image_svc"].extract_image.assert_called_once()
        deps["image_svc"].optimize_image.assert_called_once()

    def test_pull_calls_progress_callback(self, mocker):
        deps = _setup_pull_mocks(mocker)

        on_progress = MagicMock()
        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        deps["image_svc"].extract_image.return_value = Path("/tmp/ext.ext4")
        deps["image_svc"].optimize_image.return_value = _make_image()

        ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04"),
            on_progress=on_progress,
        )
        assert on_progress.call_count >= 3

    def test_pull_root_partition_detection_error(self, mocker):
        deps = _setup_pull_mocks(mocker)
        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        from mvmctl.exceptions import RootPartitionDetectionError

        deps[
            "image_svc"
        ].extract_image.side_effect = RootPartitionDetectionError(
            "no root partition"
        )

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        assert result.status == "error"
        assert result.code == "image.acquire_failed"

    def test_pull_tie_detected_error(self, mocker):
        deps = _setup_pull_mocks(mocker)
        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        from mvmctl.exceptions import TieDetectedError

        deps["image_svc"].extract_image.side_effect = TieDetectedError(
            "multiple partitions"
        )

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        assert result.status == "error"

    def test_pull_cleans_up_old_image(self, mocker):
        existing = _make_image(
            type="ubuntu-24.04",
            image_id="old-" + "x" * 60,
            path="images/old-ubuntu.ext4",
        )
        deps = _setup_pull_mocks(mocker, existing_image=existing, force=True)

        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        deps["image_svc"].extract_image.return_value = Path("/tmp/ext.ext4")
        new_item = _make_image(type="ubuntu-24.04", image_id="new-" + "x" * 60)
        deps["image_svc"].optimize_image.return_value = new_item
        deps["image_svc"].remove_many_paths.return_value = ["old-file"]

        ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04", force=True)
        )
        deps["image_svc"].remove_many_paths.assert_called_once_with([existing])

    def test_pull_with_default_firecracker_ci_version(self, mocker):
        mock_firecracker = MagicMock()
        mock_firecracker.ci_version = "v1.11"
        deps = _setup_pull_mocks(
            mocker,
            existing_image=None,
            default_firecracker=mock_firecracker,
        )
        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        deps["image_svc"].extract_image.return_value = Path("/tmp/ext.ext4")
        deps["image_svc"].optimize_image.return_value = _make_image()

        ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        call_args = deps["image_svc"].download_image.call_args
        assert call_args is not None
        assert call_args[0][4] == "v1.11"

    def test_pull_resolved_output_dir_none_raises(self, mocker):
        mocker.patch("mvmctl.api.image_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        mock_resolved = MagicMock()
        mock_resolved.output_dir = None
        mock_request = MagicMock()
        mock_request.resolve_pull.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.image_operations.ImageAcquireRequest",
            return_value=mock_request,
        )

        with pytest.raises(ImageError, match="Failed to resolve output_dir"):
            ImageOperation.pull(
                ImagePullInput(type="ubuntu-24.04")
            )


class TestImageOperationImport:
    """Tests for ImageOperation.import_()."""

    def _setup_import_mocks(
        self,
        mocker,
        existing_image: ImageItem | None = None,
        force: bool = False,
    ) -> dict[str, MagicMock]:
        deps: dict[str, MagicMock] = {}

        mocker.patch("mvmctl.api.image_operations.Database")

        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = existing_image
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
            return_value=mock_repo,
        )
        deps["repo"] = mock_repo

        mock_resolved = MagicMock()
        mock_resolved.source_path = Path("/tmp/my-image.qcow2")
        mock_resolved.format = "qcow2"
        mock_resolved.arch = "x86_64"
        mock_resolved.type = "custom-image"
        mock_resolved.output_dir = Path("/tmp/images")
        mock_resolved.force = force
        mock_resolved.partition = None
        mock_resolved.disabled_detectors = None
        mock_resolved.skip_optimization = False
        mock_resolved.set_default = False
        mock_request = MagicMock()
        mock_request.resolve_import.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.image_operations.ImageAcquireRequest",
            return_value=mock_request,
        )
        deps["request"] = mock_request
        deps["resolved"] = mock_resolved

        mocker.patch(
            "mvmctl.api.image_operations.HashGenerator.image",
            return_value="hash" + "x" * 60,
        )

        mock_image_svc = MagicMock()
        mocker.patch(
            "mvmctl.core.image._service.ImageService",
            return_value=mock_image_svc,
        )
        deps["image_svc"] = mock_image_svc

        return deps

    def test_import_early_return_image_exists(self, mocker):
        existing = _make_image(type="custom-image")
        self._setup_import_mocks(mocker, existing_image=existing, force=False)

        mock_path = MagicMock()
        mock_path.exists.return_value = True
        mock_images_dir = MagicMock()
        mock_images_dir.__truediv__.return_value = mock_path
        mocker.patch(
            "mvmctl.api.image_operations.CacheUtils.get_images_dir",
            return_value=mock_images_dir,
        )

        result = ImageOperation.import_(
            ImageImportInput(
                name="custom-image", source_path=Path("/tmp/my-image.qcow2")
            )
        )
        assert result.status == "skipped"
        assert result.code == "image.already_present"

    def test_import_success(self, mocker):
        deps = self._setup_import_mocks(mocker)
        deps["image_svc"].extract_image.return_value = Path(
            "/tmp/extracted.ext4"
        )
        new_item = _make_image(type="custom-image")
        deps["image_svc"].optimize_image.return_value = new_item

        result = ImageOperation.import_(
            ImageImportInput(
                name="custom-image", source_path=Path("/tmp/my-image.qcow2")
            )
        )
        assert result.status == "success"
        assert result.code == "image.imported"
        deps["image_svc"].extract_image.assert_called_once()
        deps["image_svc"].optimize_image.assert_called_once()

    def test_import_extract_error(self, mocker):
        deps = self._setup_import_mocks(mocker)
        from mvmctl.exceptions import RootPartitionDetectionError

        deps[
            "image_svc"
        ].extract_image.side_effect = RootPartitionDetectionError(
            "bad partition"
        )

        result = ImageOperation.import_(
            ImageImportInput(
                name="custom-image", source_path=Path("/tmp/my-image.qcow2")
            )
        )
        assert result.status == "error"
        assert result.code == "image.import_failed"

    def test_import_cleans_up_old_image(self, mocker):
        existing = _make_image(
            type="custom-image",
            image_id="old-" + "x" * 60,
            path="images/old-custom.ext4",
        )
        deps = self._setup_import_mocks(
            mocker, existing_image=existing, force=True
        )
        deps["image_svc"].extract_image.return_value = Path(
            "/tmp/extracted.ext4"
        )
        new_item = _make_image(type="custom-image", image_id="new-" + "x" * 60)
        deps["image_svc"].optimize_image.return_value = new_item
        deps["image_svc"].remove_many_paths.return_value = ["old-file"]

        ImageOperation.import_(
            ImageImportInput(
                name="custom-image",
                source_path=Path("/tmp/my-image.qcow2"),
                force=True,
            )
        )
        deps["image_svc"].remove_many_paths.assert_called_once_with([existing])

    def test_import_calls_progress_callback(self, mocker):
        deps = self._setup_import_mocks(mocker)
        deps["image_svc"].extract_image.return_value = Path(
            "/tmp/extracted.ext4"
        )
        deps["image_svc"].optimize_image.return_value = _make_image()

        on_progress = MagicMock()
        ImageOperation.import_(
            ImageImportInput(
                name="custom-image", source_path=Path("/tmp/my-image.qcow2")
            ),
            on_progress=on_progress,
        )
        assert on_progress.call_count >= 2

    def test_import_no_source_path_raises(self, mocker):
        mocker.patch("mvmctl.api.image_operations.Database")
        mock_resolved = MagicMock()
        mock_resolved.source_path = None
        mock_request = MagicMock()
        mock_request.resolve_import.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.image_operations.ImageAcquireRequest",
            return_value=mock_request,
        )

        with pytest.raises(
            ImageAcquireError, match="Failed to resolve source path"
        ):
            ImageOperation.import_(
                ImageImportInput(name="test", source_path=Path("/nonexistent"))
            )

    def test_import_no_format_raises(self, mocker):
        mocker.patch("mvmctl.api.image_operations.Database")
        mock_resolved = MagicMock()
        mock_resolved.source_path = Path("/tmp/img.qcow2")
        mock_resolved.format = None
        mock_resolved.arch = "x86_64"
        mock_request = MagicMock()
        mock_request.resolve_import.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.image_operations.ImageAcquireRequest",
            return_value=mock_request,
        )

        with pytest.raises(ImageAcquireError, match="Failed to resolve format"):
            ImageOperation.import_(
                ImageImportInput(name="test", source_path=Path("/nonexistent"))
            )

    def test_import_no_arch_raises(self, mocker):
        mocker.patch("mvmctl.api.image_operations.Database")
        mock_resolved = MagicMock()
        mock_resolved.source_path = Path("/tmp/img.qcow2")
        mock_resolved.format = "qcow2"
        mock_resolved.arch = None
        mock_request = MagicMock()
        mock_request.resolve_import.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.image_operations.ImageAcquireRequest",
            return_value=mock_request,
        )

        with pytest.raises(ImageAcquireError, match="Failed to resolve format"):
            ImageOperation.import_(
                ImageImportInput(name="test", source_path=Path("/nonexistent"))
            )

    def test_import_force_reimport(self, mocker):
        existing = _make_image(type="custom-image")
        deps = self._setup_import_mocks(
            mocker, existing_image=existing, force=True
        )
        deps["image_svc"].extract_image.return_value = Path(
            "/tmp/extracted.ext4"
        )
        new_item = _make_image(type="custom-image", image_id="new-" + "x" * 60)
        deps["image_svc"].optimize_image.return_value = new_item

        result = ImageOperation.import_(
            ImageImportInput(
                name="custom-image",
                source_path=Path("/tmp/my-image.qcow2"),
                force=True,
            )
        )
        assert result.status == "success"


class TestImageOperationRemoveExtended:
    """Extended tests for ImageOperation.remove()."""

    def test_remove_with_force_multiple(self, mocker):
        img1 = _make_image("ubuntu-24.04")
        img2 = _make_image("debian-12")
        mock_resolved = MagicMock()
        mock_resolved.images = [img1, img2]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._image_input.ImageRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.image_operations.Database")

        mock_image_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.image_operations.ImageRepository",
        )
        mocker.patch(
            "mvmctl.core.image._service.ImageService",
            return_value=mock_image_svc,
        )

        result = ImageOperation.remove(
            ImageInput(type=["ubuntu-24.04", "debian-12"]), force=True
        )
        assert result.all_ok
        assert mock_image_svc.remove.call_count == 2


class TestImageOperationHelpersExtended:
    """Extended tests for ImageOperation helper methods."""

    def test_find_existing_image_falls_back_to_get_when_not_found_by_slug(
        self, mocker
    ):
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = None
        mock_repo.get.return_value = None
        images_dir = MagicMock()

        result = ImageOperation.find_existing_image(
            MagicMock(id="missing"), images_dir, mock_repo
        )
        assert result is None
        mock_repo.get.assert_called_once_with("missing")

    def test_find_existing_image_returns_none_when_no_path(self, mocker):
        item = _make_image("test", path="")
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = item
        images_dir = MagicMock()

        result = ImageOperation.find_existing_image(
            MagicMock(id="test"), images_dir, mock_repo
        )
        assert result is None

    def test_pull_image_with_no_ci_version(self, mocker):
        deps = _setup_pull_mocks(
            mocker, existing_image=None, default_firecracker=None
        )
        deps["image_svc"].download_image.return_value = Path("/tmp/dl.qcow2")
        deps["image_svc"].extract_image.return_value = Path("/tmp/ext.ext4")
        deps["image_svc"].optimize_image.return_value = _make_image()

        result = ImageOperation.pull(
            ImagePullInput(type="ubuntu-24.04")
        )
        assert result.status == "success"
