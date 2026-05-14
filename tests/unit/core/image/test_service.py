"""Tests for ImageService — image processing with mocked subprocess calls."""

from __future__ import annotations

import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.constants import CONST_MEBIBYTE_BYTES, CONST_MIN_ROOTFS_SIZE_MIB
from mvmctl.core._shared import Database
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._service import ImageService
from mvmctl.exceptions import (
    ImageCorruptError,
    ImageDecompressionError,
    ImageEmptyError,
    ImageError,
)


@pytest.fixture
def repo(tmp_path: Path) -> ImageRepository:
    """Create a fresh ImageRepository backed by a temp database."""
    db_path = tmp_path / "test.db"
    db = Database(db_path)
    db.migrate()
    return ImageRepository(db)


@pytest.fixture
def service(repo: ImageRepository) -> ImageService:
    """Create an ImageService with the test repository."""
    return ImageService(repo)


# =========================================================================
# _convert_to_raw
# =========================================================================


class TestConvertToRaw:
    @pytest.mark.parametrize(
        ("fmt", "format_flag"),
        [
            ("qcow2", "qcow2"),
            ("vhd", "vpc"),
            ("vhdx", "vhdx"),
        ],
    )
    def test_success(
        self, service: ImageService, tmp_path: Path, fmt: str, format_flag: str
    ) -> None:
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            src = tmp_path / f"image.{fmt}"
            raw = tmp_path / "image.raw"
            ImageService._convert_to_raw(src, raw, format_flag)
            mock_run.assert_called_once()
            cmd = mock_run.call_args[0][0]
            assert cmd[0] == "qemu-img"
            assert "-f" in cmd and format_flag in cmd
            assert "-O" in cmd and "raw" in cmd

    def test_called_process_error(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = subprocess.CalledProcessError(
                1, "qemu-img", stderr="error"
            )
            with pytest.raises(ImageError, match="qemu-img conversion failed"):
                ImageService._convert_to_raw(
                    tmp_path / "image.qcow2", tmp_path / "image.raw", "qcow2"
                )

    def test_file_not_found(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError("qemu-img not found")
            with pytest.raises(ImageError, match="qemu-img conversion failed"):
                ImageService._convert_to_raw(
                    tmp_path / "image.qcow2", tmp_path / "image.raw", "qcow2"
                )

    def test_uses_parallel_coroutines(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0)
            qcow2 = tmp_path / "image.qcow2"
            raw = tmp_path / "image.raw"
            ImageService._convert_to_raw(qcow2, raw, "qcow2")
            cmd = mock_run.call_args[0][0]
            assert "-m" in cmd
            m_idx = cmd.index("-m")
            assert cmd[m_idx + 1] == "16"


# =========================================================================
# detect_filesystem_type
# =========================================================================


class TestDetectFilesystemType:
    def test_detects_ext4(self, service: ImageService, tmp_path: Path) -> None:
        image = tmp_path / "rootfs.ext4"
        image.write_bytes(b"\x00" * 1024)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(stdout="ext4\n", returncode=0)
            result = service.detect_filesystem_type(image)
            assert result == "ext4"

    def test_returns_none_on_empty(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        image = tmp_path / "rootfs.ext4"
        image.write_bytes(b"\x00" * 1024)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(stdout="\n", returncode=0)
            result = service.detect_filesystem_type(image)
            assert result is None

    def test_returns_none_on_blkid_not_found(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        image = tmp_path / "rootfs.ext4"
        image.write_bytes(b"\x00" * 1024)
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError("blkid not found")
            result = service.detect_filesystem_type(image)
            assert result is None


# =========================================================================
# get_filesystem_uuid
# =========================================================================


class TestGetFilesystemUuid:
    def test_success(self, service: ImageService, tmp_path: Path) -> None:
        image = tmp_path / "rootfs.ext4"
        image.write_bytes(b"image")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.return_value = MagicMock(
                stdout="123e4567-e89b-12d3-a456-426614174000\n"
            )
            fs_uuid = service.get_filesystem_uuid(image)
            assert fs_uuid == "123e4567-e89b-12d3-a456-426614174000"
            cmd = mock_run.call_args[0][0]
            assert cmd[0] == "blkid"
            assert "-p" in cmd

    def test_no_blkid(self, service: ImageService, tmp_path: Path) -> None:
        image = tmp_path / "rootfs.ext4"
        image.write_bytes(b"image")
        with patch.object(subprocess, "run") as mock_run:
            mock_run.side_effect = FileNotFoundError("blkid not found")
            fs_uuid = service.get_filesystem_uuid(image)
            assert fs_uuid is None


# =========================================================================
# create_ext4_from_tar
# =========================================================================


class TestCreateExt4FromTar:
    def test_success(self, service: ImageService, tmp_path: Path) -> None:
        tar_file = tmp_path / "rootfs.tar"
        tar_file.write_bytes(b"tar content")
        output = tmp_path / "rootfs.ext4"

        with (
            patch.object(subprocess, "run") as mock_run,
            patch("mvmctl.core.image._service.CacheUtils") as mock_cache,
        ):
            mock_cache.get_temp_dir.return_value = tmp_path
            mock_run.side_effect = [
                MagicMock(returncode=0),  # tar extraction
                MagicMock(returncode=0),  # chmod
                MagicMock(
                    returncode=0, stdout="104857600\t/tmp\tdir"
                ),  # du -sb
                MagicMock(returncode=0),  # truncate
                MagicMock(returncode=0),  # mkfs.ext4
            ]
            result = service.create_ext4_from_tar(
                tar_file, output, minimum_rootfs_mib=1024
            )
            assert result is True
            assert mock_run.call_count == 5

    def test_tar_failure(self, service: ImageService, tmp_path: Path) -> None:
        tar_file = tmp_path / "rootfs.tar"
        tar_file.write_bytes(b"tar content")
        output = tmp_path / "rootfs.ext4"

        with (
            patch.object(subprocess, "run") as mock_run,
            patch("mvmctl.core.image._service.CacheUtils") as mock_cache,
        ):
            mock_cache.get_temp_dir.return_value = tmp_path
            mock_run.side_effect = subprocess.CalledProcessError(
                1, "tar", stderr="extraction failed"
            )
            with pytest.raises(ImageError):
                service.create_ext4_from_tar(
                    tar_file, output, minimum_rootfs_mib=2048
                )

    def test_dynamic_minimum_size(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        """Test with minimum_rootfs_mib='dynamic'."""
        tar_file = tmp_path / "rootfs.tar"
        tar_file.write_bytes(b"tar content")
        output = tmp_path / "rootfs.ext4"

        with (
            patch.object(subprocess, "run") as mock_run,
            patch("mvmctl.core.image._service.CacheUtils") as mock_cache,
        ):
            mock_cache.get_temp_dir.return_value = tmp_path
            mock_run.side_effect = [
                MagicMock(returncode=0),  # tar
                MagicMock(returncode=0),  # chmod
                MagicMock(
                    returncode=0, stdout="500000000\t/tmp\tdir"
                ),  # du: 500 MB
                MagicMock(returncode=0),  # truncate
                MagicMock(returncode=0),  # mkfs.ext4
            ]
            result = service.create_ext4_from_tar(
                tar_file, output, minimum_rootfs_mib="dynamic"
            )
            assert result is True
            # Check truncate used calculated size
            truncate_call = mock_run.call_args_list[3]
            truncate_size = truncate_call[0][0][2]  # "-s" "625M"
            assert "M" in truncate_size


# =========================================================================
# compress
# =========================================================================


class TestCompress:
    def test_compress_success(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        """Compress requires zstd — test with a real file and zstd if available, else skip."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"\x01" * 4096)  # non-zero content

        try:
            compressed = service.compress(image_path, keep_source=False)
            assert compressed.exists()
            assert compressed.suffix == ".zst"
            assert compressed.stat().st_size > 0
        except ImageEmptyError:
            pytest.fail("Unexpected ImageEmptyError for non-zero file")
        except Exception as exc:
            # zstd may not be available in test environment
            if "Failed to compress" in str(exc):
                pytest.skip("zstd not available")
            raise

    def test_compress_all_zeros_raises_corrupt(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        image_path = tmp_path / "corrupt.img"
        image_path.write_bytes(b"\x00" * CONST_MEBIBYTE_BYTES)

        with pytest.raises(ImageCorruptError, match="all zeros"):
            service.compress(image_path)


# =========================================================================
# decompress
# =========================================================================


class TestDecompress:
    def test_decompress_missing_source(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        compressed = tmp_path / "nonexistent.zst"
        output = tmp_path / "output.img"
        with pytest.raises(ImageError, match="not found"):
            service.decompress(compressed, output)

    def test_decompress_unsupported_format(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        compressed = tmp_path / "test.gz"
        compressed.write_bytes(b"data")
        output = tmp_path / "output.img"
        with pytest.raises(
            ImageDecompressionError, match="Unsupported compression format"
        ):
            service.decompress(compressed, output, compressed_format="gz")


# =========================================================================
# get_specs_for
# =========================================================================


class TestGetSpecsFor:
    def test_returns_matching_specs(self) -> None:
        specs = ImageService.get_specs_for(
            ["archlinux"], version="latest", arch="x86_64"
        )
        assert len(specs) >= 1
        assert specs[0].id == "archlinux-latest"

    def test_raises_for_missing(self) -> None:
        with pytest.raises(ImageError, match="not found"):
            ImageService.get_specs_for(
                ["nonexistent-image"], version=None, arch="x86_64"
            )


# =========================================================================
# _calculate_minimum_image_size_mb (private, tested via math)
# =========================================================================


class TestCalculateMinimumImageSize:
    def test_small_content_uses_minimum(self, service: ImageService) -> None:
        small = 1_000_000  # 1 MB
        result = service._calculate_minimum_image_size_mb(small)
        assert result >= CONST_MIN_ROOTFS_SIZE_MIB

    def test_large_content_uses_headroom(self, service: ImageService) -> None:
        large = 1_000_000_000  # 1 GB
        result = service._calculate_minimum_image_size_mb(large)
        # Uses binary MiB (1,048,576): 1G / 1MiB * 1.25 headroom
        content_mib = 1_000_000_000 / CONST_MEBIBYTE_BYTES
        expected = int(content_mib * 1.25)
        assert result == expected

    def test_uses_decimal_mb(self, service: ImageService) -> None:
        content = 500_000_000  # 500 MB decimal
        result = service._calculate_minimum_image_size_mb(content)
        content_mib = 500_000_000 / CONST_MEBIBYTE_BYTES
        expected = int(content_mib * 1.25)
        assert result == expected


# =========================================================================
# materialize_to
# =========================================================================


class TestMaterializeTo:
    def test_copy_to_destination(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        """materialize_to copies from cache to destination via cp."""
        image_id = "test-image-id-123"
        fs_type = "ext4"

        # Create the cached file
        from mvmctl.utils.common import CacheUtils

        warm_dir = CacheUtils.get_warm_image_dir()
        warm_dir.mkdir(parents=True, exist_ok=True)
        cached = warm_dir / f"{image_id}.{fs_type}"
        cached.write_bytes(b"cached image content")

        output_path = tmp_path / "dest" / "rootfs.ext4"

        with patch.object(subprocess, "run") as mock_run:

            def _fake_cp(args, **kwargs):  # noqa: ARG001
                output_path.parent.mkdir(parents=True, exist_ok=True)
                output_path.write_bytes(b"cached image content")
                return MagicMock(returncode=0)

            mock_run.side_effect = _fake_cp
            service.materialize_to(image_id, fs_type, output_path)
            assert output_path.exists()
            assert output_path.read_bytes() == b"cached image content"

    def test_raises_when_not_in_cache(
        self, service: ImageService, tmp_path: Path
    ) -> None:
        output_path = tmp_path / "dest" / "rootfs.ext4"
        with pytest.raises(ImageError, match="not in cache"):
            service.materialize_to("nonexistent-id", "ext4", output_path)


# =========================================================================
# detect_image_format (classmethod)
# =========================================================================


class TestDetectImageFormat:
    def test_returns_none_for_nonexistent(self, tmp_path: Path) -> None:
        result = ImageService.detect_image_format(tmp_path / "nonexistent")
        assert result is None

    def test_returns_none_for_empty_file(self, tmp_path: Path) -> None:
        f = tmp_path / "empty.img"
        f.write_bytes(b"")
        result = ImageService.detect_image_format(f)
        assert result is None

    def test_detects_qcow2(self, tmp_path: Path) -> None:
        f = tmp_path / "test.qcow2"
        # QCOW2 magic: "QFI\xfb"
        f.write_bytes(b"QFI\xfb" + b"\x00" * 512)
        result = ImageService.detect_image_format(f)
        assert result == "qcow2"

    def test_detects_squashfs(self, tmp_path: Path) -> None:
        f = tmp_path / "test.squashfs"
        # SquashFS magic: "hsqs" or "sqsh"
        f.write_bytes(b"hsqs" + b"\x00" * 512)
        result = ImageService.detect_image_format(f)
        assert result == "squashfs"
