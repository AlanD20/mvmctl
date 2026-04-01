"""Tests for api/assets.py - Asset management API."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
import yaml
from pytest_mock import MockerFixture

from mvmctl.api.assets import (
    AssetInfo,
    fetch_images_parallel,
    list_assets,
    pull_image,
    pull_images,
    pull_kernel,
    remove_asset,
    setup_assets,
)
from mvmctl.core.binary_manager import BinaryVersion
from mvmctl.core.image import ImageImportResult
from mvmctl.exceptions import AssetNotFoundError, ConfigError, ImageError
from mvmctl.models.image import ImageSpec


@patch("mvmctl.api.assets.set_active_version")
@patch("mvmctl.api.assets.fetch_binary")
def test_setup_assets_default_bin_dir(
    mock_fetch: MagicMock, mock_set_active: MagicMock, tmp_path: Path, mocker: MockerFixture
):
    """setup_assets uses default bin_dir when None is passed."""
    mock_bv = MagicMock(spec=BinaryVersion)
    mock_fetch.return_value = mock_bv

    setup_assets("1.5.0", bin_dir=None)

    mock_fetch.assert_called_once_with("1.5.0", bin_dir=None)


@patch("mvmctl.api.assets.build_kernel_pipeline")
@patch("mvmctl.api.assets.get_kernels_dir")
def test_pull_kernel_success(
    mock_get_kernels_dir: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    """pull_kernel calls build_kernel_pipeline with correct arguments."""
    from mvmctl.core.kernel import KernelPipelineResult

    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    mock_get_kernels_dir.return_value = kernels_dir
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    result = pull_kernel(version="6.1.102")

    mock_build.assert_called_once()
    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["version"] == "6.1.102"
    assert "cdn.kernel.org" in call_kwargs["source_url"]
    assert call_kwargs["output_path"] == kernels_dir / "vmlinux"
    assert result == mock_result


@patch("mvmctl.api.assets.build_kernel_pipeline")
def test_pull_kernel_custom_url(mock_build: MagicMock, tmp_path: Path, mocker: MockerFixture):
    """pull_kernel uses custom remote_tar_url when provided."""
    mocker.patch("mvmctl.api.assets.get_kernels_dir", return_value=tmp_path)
    custom_url = "https://custom.example.com/kernel.tar.xz"

    pull_kernel(version="6.1.0", remote_tar_url=custom_url)

    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["source_url"] == custom_url


@patch("mvmctl.api.assets.build_kernel_pipeline")
def test_pull_kernel_custom_output_path(
    mock_build: MagicMock, tmp_path: Path, mocker: MockerFixture
):
    """pull_kernel uses custom output_path when provided."""
    mocker.patch("mvmctl.api.assets.get_kernels_dir", return_value=tmp_path / "unused")
    custom_output = tmp_path / "custom" / "kernel"

    pull_kernel(version="6.1.0", output_path=custom_output)

    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["output_path"] == custom_output


@patch("mvmctl.api.assets.build_kernel_pipeline")
def test_pull_kernel_custom_build_dir(mock_build: MagicMock, tmp_path: Path, mocker: MockerFixture):
    """pull_kernel uses custom build_dir when provided."""
    mocker.patch("mvmctl.api.assets.get_kernels_dir", return_value=tmp_path)
    custom_build = tmp_path / "build"

    pull_kernel(version="6.1.0", build_dir=custom_build)

    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["build_dir"] == custom_build


@patch("mvmctl.api.assets.build_kernel_pipeline")
def test_pull_kernel_jobs_parameter(mock_build: MagicMock, tmp_path: Path, mocker: MockerFixture):
    """pull_kernel passes jobs parameter to build_kernel_pipeline."""
    mocker.patch("mvmctl.api.assets.get_kernels_dir", return_value=tmp_path)

    pull_kernel(version="6.1.0", jobs=8)

    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["jobs"] == 8


@patch("mvmctl.api.assets.build_kernel_pipeline")
@patch("mvmctl.api.assets.get_kernels_dir")
def test_pull_kernel_build_failure(
    mock_get_kernels_dir: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    """pull_kernel propagates KernelError from build_kernel_pipeline."""
    mock_get_kernels_dir.return_value = tmp_path
    from mvmctl.exceptions import KernelError

    mock_build.side_effect = KernelError("Build failed")

    with pytest.raises(KernelError, match="Build failed"):
        pull_kernel(version="6.1.102")


@patch("mvmctl.api.assets.build_kernel_pipeline")
@patch("mvmctl.api.assets.get_kernels_dir")
def test_pull_kernel_default_build_dir(
    mock_get_kernels: MagicMock, mock_build: MagicMock, tmp_path: Path, mocker: MockerFixture
):
    """pull_kernel creates default build_dir under cache when not provided."""
    mock_get_kernels.return_value = tmp_path / "kernels"
    mocker.patch("mvmctl.utils.fs.get_cache_dir", return_value=tmp_path / "cache")

    pull_kernel(version="6.1.0")

    call_kwargs = mock_build.call_args[1]
    assert call_kwargs["build_dir"] == tmp_path / "cache" / "kernel-build"


def test_pull_image_success(tmp_path: Path, mocker: MockerFixture):
    """pull_image fetches image by ID from YAML config."""
    config = {
        "images": [
            {
                "id": "ubuntu-24.04",
                "name": "Ubuntu 24.04",
                "source": "https://example.com/ubuntu.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 4096,
                "sha256": "a" * 64,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    output_dir = tmp_path / "images"
    output_dir.mkdir()

    expected_path = output_dir / "ubuntu-24.04.ext4"
    mock_fetch = mocker.patch(
        "mvmctl.api.assets.fetch_image",
        return_value=ImageImportResult(path=expected_path, fs_type="ext4", fs_uuid="test-uuid"),
    )
    mocker.patch("mvmctl.api.assets.get_assets_dir", return_value=tmp_path)
    mocker.patch("mvmctl.api.assets.get_images_dir", return_value=output_dir)

    result = pull_image("ubuntu-24.04", images_yaml=images_yaml, output_dir=output_dir)

    assert result == expected_path
    mock_fetch.assert_called_once()
    call_args = mock_fetch.call_args
    assert call_args[0][0].id == "ubuntu-24.04"
    assert call_args[0][1] == output_dir
    assert call_args[1]["force"] is False


def test_pull_image_not_found(tmp_path: Path, mocker: MockerFixture):
    """pull_image raises ImageError when image ID not found in YAML."""
    config = {"images": []}
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    mocker.patch("mvmctl.api.assets.get_assets_dir", return_value=tmp_path)

    with pytest.raises(ImageError, match="Image ID 'unknown-image' not found"):
        pull_image("unknown-image", images_yaml=images_yaml)


def test_pull_image_force_re_download(tmp_path: Path, mocker: MockerFixture):
    """pull_image passes force parameter to fetch_image."""
    config = {
        "images": [
            {
                "id": "alpine",
                "name": "Alpine Linux",
                "source": "https://example.com/alpine.tar.gz",
                "format": "tar-rootfs",
                "convert_to": "ext4",
                "minimum_rootfs_size": 1024,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    output_dir = tmp_path / "images"
    output_dir.mkdir()

    mock_fetch = mocker.patch(
        "mvmctl.api.assets.fetch_image",
        return_value=ImageImportResult(
            path=output_dir / "alpine.ext4", fs_type="ext4", fs_uuid=None
        ),
    )

    pull_image("alpine", force=True, images_yaml=images_yaml, output_dir=output_dir)

    call_kwargs = mock_fetch.call_args[1]
    assert call_kwargs["force"] is True


def test_pull_image_fetch_failure(tmp_path: Path, mocker: MockerFixture):
    """pull_image propagates ImageError from fetch_image."""
    config = {
        "images": [
            {
                "id": "test-image",
                "name": "Test Image",
                "source": "https://example.com/test.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 2048,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    mocker.patch("mvmctl.api.assets.fetch_image", side_effect=ImageError("Download failed"))

    with pytest.raises(ImageError, match="Download failed"):
        pull_image("test-image", images_yaml=images_yaml, output_dir=tmp_path)


def test_pull_image_uses_default_paths(tmp_path: Path, mocker: MockerFixture):
    """pull_image uses default paths from get_assets_dir and get_images_dir."""
    config = {
        "images": [
            {
                "id": "test",
                "name": "Test",
                "source": "http://x/qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 100,
            }
        ]
    }
    (tmp_path / "images.yaml").write_text(yaml.dump(config))

    assets_dir = tmp_path / "assets"
    assets_dir.mkdir()
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    (assets_dir / "images.yaml").write_text(yaml.dump(config))

    mocker.patch("mvmctl.api.assets.get_assets_dir", return_value=assets_dir)
    mocker.patch("mvmctl.api.assets.get_images_dir", return_value=images_dir)
    mock_fetch = mocker.patch(
        "mvmctl.api.assets.fetch_image",
        return_value=ImageImportResult(path=images_dir / "test.ext4", fs_type="ext4", fs_uuid=None),
    )

    pull_image("test")

    mock_fetch.assert_called_once()


def test_fetch_images_parallel_success(tmp_path: Path, mocker: MockerFixture):
    """fetch_images_parallel downloads multiple images concurrently."""
    specs = [
        ImageSpec(
            id="img1",
            image_type="test",
            version="test",
            name="Image 1",
            source="http://x/1.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
        ),
        ImageSpec(
            id="img2",
            image_type="test",
            version="test",
            name="Image 2",
            source="http://x/2.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=200,
        ),
    ]

    def mock_fetch(
        spec: ImageSpec, output_dir: Path, force: bool = False, skip_optimization: bool = False
    ) -> ImageImportResult:
        return ImageImportResult(path=output_dir / f"{spec.id}.ext4", fs_type="ext4", fs_uuid=None)

    mocker.patch("mvmctl.api.assets.fetch_image", side_effect=mock_fetch)

    results = fetch_images_parallel(specs, tmp_path)

    assert len(results) == 2
    assert results[0] == tmp_path / "img1.ext4"
    assert results[1] == tmp_path / "img2.ext4"


def test_fetch_images_parallel_with_force(tmp_path: Path, mocker: MockerFixture):
    """fetch_images_parallel passes force parameter to each fetch."""
    specs = [
        ImageSpec(
            id="img1",
            image_type="test",
            version="test",
            name="Image 1",
            source="http://x/1.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
        )
    ]

    captured_force = []

    def mock_fetch_with_capture(spec, output_dir, force=False, skip_optimization=False):
        captured_force.append(force)
        return ImageImportResult(path=output_dir / f"{spec.id}.ext4", fs_type="ext4", fs_uuid=None)

    mocker.patch("mvmctl.api.assets.fetch_image", side_effect=mock_fetch_with_capture)

    fetch_images_parallel(specs, tmp_path, force=True)

    assert captured_force[0] is True


def test_fetch_images_parallel_custom_workers(tmp_path: Path, mocker: MockerFixture):
    """fetch_images_parallel respects max_workers parameter."""
    specs = [
        ImageSpec(
            id=f"img{i}",
            image_type="test",
            version="test",
            name=f"Image {i}",
            source=f"http://x/{i}.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
        )
        for i in range(3)
    ]

    mock_fetch = mocker.patch(
        "mvmctl.api.assets.fetch_image",
        return_value=ImageImportResult(path=tmp_path / "test.ext4", fs_type="ext4", fs_uuid=None),
    )

    results = fetch_images_parallel(specs, tmp_path, max_workers=2)

    assert len(results) == 3
    assert mock_fetch.call_count == 3


def test_fetch_images_parallel_failure(tmp_path: Path, mocker: MockerFixture):
    """fetch_images_parallel raises ImageError with all failures listed."""
    specs = [
        ImageSpec(
            id="img1",
            image_type="test",
            version="test",
            name="Image 1",
            source="http://x/1.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
        ),
        ImageSpec(
            id="img2",
            image_type="test",
            version="test",
            name="Image 2",
            source="http://x/2.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=200,
        ),
    ]

    def mock_fetch(
        spec: ImageSpec, output_dir: Path, force: bool = False, skip_optimization: bool = False
    ) -> Path:
        if spec.id == "img1":
            raise Exception("Network error for img1")
        return output_dir / f"{spec.id}.ext4"

    mocker.patch("mvmctl.api.assets.fetch_image", side_effect=mock_fetch)

    with pytest.raises(ImageError, match="Failed to fetch the following images") as exc_info:
        fetch_images_parallel(specs, tmp_path)

    assert "img1: Network error" in str(exc_info.value)


def test_fetch_images_parallel_multiple_failures(tmp_path: Path, mocker: MockerFixture):
    """fetch_images_parallel lists all failures when multiple downloads fail."""
    specs = [
        ImageSpec(
            id="img1",
            image_type="test",
            version="test",
            name="Image 1",
            source="http://x/1.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
        ),
        ImageSpec(
            id="img2",
            image_type="test",
            version="test",
            name="Image 2",
            source="http://x/2.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=200,
        ),
    ]

    mocker.patch("mvmctl.api.assets.fetch_image", side_effect=Exception("Download failed"))

    with pytest.raises(ImageError) as exc_info:
        fetch_images_parallel(specs, tmp_path)

    error_msg = str(exc_info.value)
    assert "img1:" in error_msg
    assert "img2:" in error_msg


def test_fetch_images_parallel_empty_list(tmp_path: Path):
    """fetch_images_parallel returns empty list for empty specs."""
    results = fetch_images_parallel([], tmp_path)
    assert results == []


def test_pull_images_success(tmp_path: Path, mocker: MockerFixture):
    """pull_images fetches multiple images by ID in parallel."""
    config = {
        "images": [
            {
                "id": "img1",
                "name": "Image 1",
                "source": "http://x/1.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 100,
            },
            {
                "id": "img2",
                "name": "Image 2",
                "source": "http://x/2.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 200,
            },
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    output_dir = tmp_path / "images"
    output_dir.mkdir()

    mock_fetch_parallel = mocker.patch(
        "mvmctl.api.assets.fetch_images_parallel",
        return_value=[output_dir / "img1.ext4", output_dir / "img2.ext4"],
    )

    results = pull_images(["img1", "img2"], images_yaml=images_yaml, output_dir=output_dir)

    assert len(results) == 2
    mock_fetch_parallel.assert_called_once()
    call_args = mock_fetch_parallel.call_args
    assert len(call_args[0][0]) == 2
    assert call_args[1]["force"] is False


def test_pull_images_missing_ids(tmp_path: Path, mocker: MockerFixture):
    """pull_images raises ImageError when image IDs not found in YAML."""
    config = {
        "images": [
            {
                "id": "img1",
                "name": "Image 1",
                "source": "http://x/1.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 100,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    mocker.patch("mvmctl.api.assets.get_assets_dir", return_value=tmp_path)

    with pytest.raises(ImageError, match="Image IDs not found") as exc_info:
        pull_images(["img1", "missing-img"], images_yaml=images_yaml)

    assert "missing-img" in str(exc_info.value)


def test_pull_images_empty_list(tmp_path: Path, mocker: MockerFixture):
    """pull_images returns empty list for empty image_ids."""
    mocker.patch("mvmctl.api.assets.load_images_config", return_value=[])

    results = pull_images([], images_yaml=tmp_path / "images.yaml")
    assert results == []


def test_pull_images_with_force(tmp_path: Path, mocker: MockerFixture):
    """pull_images passes force parameter to fetch_images_parallel."""
    config = {
        "images": [
            {
                "id": "img1",
                "name": "Image 1",
                "source": "http://x/1.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 100,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    mock_fetch_parallel = mocker.patch(
        "mvmctl.api.assets.fetch_images_parallel", return_value=[tmp_path / "img1.ext4"]
    )

    pull_images(["img1"], force=True, images_yaml=images_yaml, output_dir=tmp_path)

    call_kwargs = mock_fetch_parallel.call_args[1]
    assert call_kwargs["force"] is True


def test_pull_images_with_max_workers(tmp_path: Path, mocker: MockerFixture):
    """pull_images passes max_workers parameter to fetch_images_parallel."""
    config = {
        "images": [
            {
                "id": "img1",
                "name": "Image 1",
                "source": "http://x/1.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "minimum_rootfs_size": 100,
            }
        ]
    }
    images_yaml = tmp_path / "images.yaml"
    images_yaml.write_text(yaml.dump(config))

    mock_fetch_parallel = mocker.patch(
        "mvmctl.api.assets.fetch_images_parallel", return_value=[tmp_path / "img1.ext4"]
    )

    pull_images(["img1"], max_workers=8, images_yaml=images_yaml, output_dir=tmp_path)

    call_kwargs = mock_fetch_parallel.call_args[1]
    assert call_kwargs["max_workers"] == 8


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
@patch("mvmctl.api.assets.get_images_dir")
@patch("mvmctl.api.assets.get_assets_dir")
@patch("mvmctl.api.assets.load_images_config")
def test_list_assets_binaries(
    mock_load_config: MagicMock,
    mock_get_assets_dir: MagicMock,
    mock_get_images_dir: MagicMock,
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets includes binaries from list_local_versions."""
    mock_bv = MagicMock(spec=BinaryVersion)
    mock_bv.version = "1.5.0"
    mock_bv.is_active = True
    mock_bv.firecracker_path = tmp_path / "firecracker-v1.5.0"
    mock_list_local.return_value = [mock_bv]

    mock_get_kernels_dir.return_value = tmp_path / "kernels"
    mock_get_images_dir.return_value = tmp_path / "images"
    mock_get_assets_dir.return_value = tmp_path / "assets"
    mock_load_config.return_value = []

    assets = list_assets()

    binary_assets = [a for a in assets if a["type"] == "binary"]
    assert len(binary_assets) == 1
    assert binary_assets[0]["name"] == "1.5.0"
    assert binary_assets[0]["active"] is True
    assert binary_assets[0]["details"] == str(tmp_path / "firecracker-v1.5.0")


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
def test_list_assets_kernels(
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets includes kernel files from kernels directory."""
    mock_list_local.return_value = []

    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_text("fake kernel content" * 100)

    mock_get_kernels_dir.return_value = kernels_dir

    with patch("mvmctl.api.assets.get_images_dir", return_value=tmp_path / "images"):
        with patch("mvmctl.api.assets.get_assets_dir", return_value=tmp_path / "assets"):
            with patch("mvmctl.api.assets.load_images_config", return_value=[]):
                assets = list_assets()

    kernel_assets = [a for a in assets if a["type"] == "kernel"]
    assert len(kernel_assets) == 1
    assert kernel_assets[0]["name"] == "vmlinux"
    assert kernel_assets[0]["active"] is None
    assert kernel_assets[0]["size_mib"] is not None
    assert kernel_assets[0]["size_mib"] > 0


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
@patch("mvmctl.api.assets.get_images_dir")
@patch("mvmctl.api.assets.get_assets_dir")
@patch("mvmctl.api.assets.load_images_config")
def test_list_assets_images(
    mock_load_config: MagicMock,
    mock_get_assets_dir: MagicMock,
    mock_get_images_dir: MagicMock,
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets includes images from YAML config with existence check."""
    mock_list_local.return_value = []
    mock_get_kernels_dir.return_value = tmp_path / "kernels"

    images_dir = tmp_path / "images"
    images_dir.mkdir()
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    mock_get_images_dir.return_value = images_dir
    mock_get_assets_dir.return_value = tmp_path / "assets"

    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="http://x/qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
    )
    mock_load_config.return_value = [spec]

    assets = list_assets()

    image_assets = [a for a in assets if a["type"] == "image"]
    assert len(image_assets) == 1
    assert image_assets[0]["name"] == "ubuntu-24.04"
    assert image_assets[0]["active"] is True
    assert image_assets[0]["size_mib"] is not None
    assert image_assets[0]["details"] == "Format: qcow2"


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
@patch("mvmctl.api.assets.get_images_dir")
@patch("mvmctl.api.assets.get_assets_dir")
@patch("mvmctl.api.assets.load_images_config")
def test_list_assets_missing_image(
    mock_load_config: MagicMock,
    mock_get_assets_dir: MagicMock,
    mock_get_images_dir: MagicMock,
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets shows active=False for images that don't exist locally."""
    mock_list_local.return_value = []
    mock_get_kernels_dir.return_value = tmp_path / "kernels"

    images_dir = tmp_path / "images"
    images_dir.mkdir()

    mock_get_images_dir.return_value = images_dir
    mock_get_assets_dir.return_value = tmp_path / "assets"

    spec = ImageSpec(
        id="missing-img",
        image_type="test",
        version="test",
        name="Missing Image",
        source="http://x/qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=100,
    )
    mock_load_config.return_value = [spec]

    assets = list_assets()

    image_assets = [a for a in assets if a["type"] == "image"]
    assert len(image_assets) == 1
    assert image_assets[0]["name"] == "missing-img"
    assert image_assets[0]["active"] is False
    assert image_assets[0]["size_mib"] is None


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
@patch("mvmctl.api.assets.get_images_dir")
@patch("mvmctl.api.assets.get_assets_dir")
@patch("mvmctl.api.assets.load_images_config")
def test_list_assets_yaml_parse_error(
    mock_load_config: MagicMock,
    mock_get_assets_dir: MagicMock,
    mock_get_images_dir: MagicMock,
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets handles YAML parse errors gracefully."""
    mock_list_local.return_value = []
    mock_get_kernels_dir.return_value = tmp_path / "kernels"
    mock_get_images_dir.return_value = tmp_path / "images"
    mock_get_assets_dir.return_value = tmp_path / "assets"
    mock_load_config.side_effect = ConfigError("Invalid YAML")

    assets = list_assets()

    assert isinstance(assets, list)


@patch("mvmctl.api.assets.list_local_versions")
@patch("mvmctl.api.assets.get_kernels_dir")
def test_list_assets_kernels_dir_missing(
    mock_get_kernels_dir: MagicMock,
    mock_list_local: MagicMock,
    tmp_path: Path,
):
    """list_assets handles missing kernels directory."""
    mock_list_local.return_value = []

    kernels_dir = tmp_path / "kernels"
    mock_get_kernels_dir.return_value = kernels_dir

    with patch("mvmctl.api.assets.get_images_dir", return_value=tmp_path / "images"):
        with patch("mvmctl.api.assets.get_assets_dir", return_value=tmp_path / "assets"):
            with patch("mvmctl.api.assets.load_images_config", return_value=[]):
                assets = list_assets()

    kernel_assets = [a for a in assets if a["type"] == "kernel"]
    assert len(kernel_assets) == 0


@patch("mvmctl.api.assets.remove_version")
def test_remove_asset_binary(mock_remove_version: MagicMock):
    """remove_asset calls remove_version for binary type."""
    remove_asset("binary", "1.5.0")
    mock_remove_version.assert_called_once_with("1.5.0")


@patch("mvmctl.api.assets.get_kernels_dir")
def test_remove_asset_kernel_success(mock_get_kernels_dir: MagicMock, tmp_path: Path):
    """remove_asset deletes kernel file when it exists."""
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    kernel_file = kernels_dir / "vmlinux-custom"
    kernel_file.write_text("kernel data")

    mock_get_kernels_dir.return_value = kernels_dir

    remove_asset("kernel", "vmlinux-custom")

    assert not kernel_file.exists()


@patch("mvmctl.api.assets.get_kernels_dir")
def test_remove_asset_kernel_not_found(mock_get_kernels_dir: MagicMock, tmp_path: Path):
    """remove_asset raises FileNotFoundError for missing kernel."""
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()
    mock_get_kernels_dir.return_value = kernels_dir

    with pytest.raises(FileNotFoundError, match="Kernel vmlinux-missing not found"):
        remove_asset("kernel", "vmlinux-missing")


@patch("mvmctl.api.assets.get_images_dir")
def test_remove_asset_image_success(mock_get_images_dir: MagicMock, tmp_path: Path):
    """remove_asset deletes image files."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    image_file = images_dir / "ubuntu-24.04.ext4"
    image_file.write_text("image data")

    mock_get_images_dir.return_value = images_dir

    remove_asset("image", "ubuntu-24.04")

    assert not image_file.exists()


@patch("mvmctl.api.assets.get_images_dir")
def test_remove_asset_image_multiple_extensions(mock_get_images_dir: MagicMock, tmp_path: Path):
    """remove_asset deletes image files with any supported extension."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    (images_dir / "myimage.ext4").write_text("ext4 data")
    (images_dir / "myimage.btrfs").write_text("btrfs data")

    mock_get_images_dir.return_value = images_dir

    remove_asset("image", "myimage")

    assert not (images_dir / "myimage.ext4").exists()
    assert not (images_dir / "myimage.btrfs").exists()


@patch("mvmctl.api.assets.get_images_dir")
def test_remove_asset_image_not_found(mock_get_images_dir: MagicMock, tmp_path: Path):
    """remove_asset raises FileNotFoundError for missing image."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    mock_get_images_dir.return_value = images_dir

    with pytest.raises(FileNotFoundError, match="No image files found for 'missing'"):
        remove_asset("image", "missing")


@patch("mvmctl.api.assets.get_images_dir")
@patch("mvmctl.api.assets.shutil.rmtree")
def test_remove_asset_image_directory(
    mock_rmtree: MagicMock, mock_get_images_dir: MagicMock, tmp_path: Path
):
    """remove_asset uses rmtree for image directories."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    image_dir = images_dir / "myimage.img"
    image_dir.mkdir()
    (image_dir / "data").write_text("inside")

    mock_get_images_dir.return_value = images_dir

    remove_asset("image", "myimage")

    mock_rmtree.assert_called_once()


def test_remove_asset_unknown_type():
    """remove_asset raises ValueError for unknown asset type."""
    with pytest.raises(ValueError, match="Unknown asset type: unknown"):
        remove_asset("unknown", "test")  # type: ignore[arg-type]


@patch("mvmctl.api.assets.remove_version")
def test_remove_asset_binary_not_found(mock_remove_version: MagicMock):
    """remove_asset propagates AssetNotFoundError from remove_version."""
    mock_remove_version.side_effect = AssetNotFoundError("Version not found")

    with pytest.raises(AssetNotFoundError, match="Version not found"):
        remove_asset("binary", "9.9.9")


@patch("mvmctl.api.assets.get_images_dir")
def test_remove_asset_image_single_file(mock_get_images_dir: MagicMock, tmp_path: Path):
    """remove_asset handles single image file correctly."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    (images_dir / "test.ext4").write_text("data")

    mock_get_images_dir.return_value = images_dir

    remove_asset("image", "test")

    assert not (images_dir / "test.ext4").exists()


def test_asset_info_typed_dict():
    """AssetInfo TypedDict accepts valid data."""
    info: AssetInfo = {
        "type": "binary",
        "name": "1.5.0",
        "active": True,
        "size_mib": 100.5,
        "details": "/path/to/binary",
    }
    assert info["type"] == "binary"
    assert info["active"] is True


def test_asset_info_with_none_values():
    """AssetInfo TypedDict accepts None values for optional fields."""
    info: AssetInfo = {
        "type": "kernel",
        "name": "vmlinux",
        "active": None,
        "size_mib": None,
        "details": None,
    }
    assert info["active"] is None
    assert info["size_mib"] is None
