"""Image processing service - handles compression, decompression, shrinking, and pool management."""

from __future__ import annotations

import logging
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile
from collections.abc import Callable
from pathlib import Path

from mvmctl.constants import (
    CONST_MEBIBYTE_BYTES,
    CONST_MIN_ROOTFS_SIZE_MIB,
    CONST_PERCENT,
    CONST_RATIO_MIN,
    CONST_ROOTFS_HEADROOM_FACTOR,
    CONST_RUNTIME_BUFFER_MB,
    CONST_SECTOR_SIZE_BYTES,
    CONST_SHRINK_SAFETY_MARGIN,
    DEFAULT_IMAGE_ARCH,
    HTTP_TIMEOUT_SHA256_FETCH_S,
)
from mvmctl.core._internal._guestfs import OptimizedGuestfs
from mvmctl.core.image._repository import ImageRepository
from mvmctl.exceptions import (
    ConfigError,
    GuestfsNotAvailableError,
    ImageCompressionError,
    ImageCorruptError,
    ImageDecompressionError,
    ImageEmptyError,
    ImageError,
    ImageValidationError,
)
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.common import safe_int
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES
_COPY_CHUNK_SIZE = CONST_MEBIBYTE_BYTES  # 1 MiB


class _NoPartitionTable:
    """Sentinel: raw image has no partition table and should be used as-is."""


_NO_PARTITION_TABLE = _NoPartitionTable()


class ImageService:
    """Handles image processing: compression, decompression, shrinking, format conversion, and pool management.

    Args:
        repo: ImageRepository for DB operations. Must be provided.
    """

    def __init__(self, repo: ImageRepository) -> None:
        """Initialize ImageService.

        Args:
            repo: ImageRepository for DB operations.
        """
        self._repo = repo

    # =====================================================================
    # Spec Resolution
    # =====================================================================

    def get_specs_for(self, os_slugs: list[str]) -> list[ImageSpec]:
        """Resolve ImageSpecs from the bundled images.yaml by os_slugs.

        Args:
            os_slugs: List of image IDs from images.yaml (e.g., ['ubuntu-24.04']).

        Returns:
            List of matching ImageSpecs.

        Raises:
            ImageError: If any image is not found in the catalog.
        """
        from mvmctl.core._internal._asset_manager import AssetManager

        manager = AssetManager()
        yaml_path = manager.get_file("images.yaml")
        all_specs = self.load_available_images(Path(str(yaml_path)))

        spec_map = {spec.id: spec for spec in all_specs}
        results: list[ImageSpec] = []
        missing: list[str] = []

        for os_slug in os_slugs:
            spec = spec_map.get(os_slug)
            if spec is not None:
                results.append(spec)
            else:
                missing.append(os_slug)

        if missing:
            available = ", ".join(spec_map.keys())
            raise ImageError(
                f"Image(s) not found: {', '.join(missing)}. Available: {available}"
            )

        return results

    # =====================================================================
    # Image Path Validation
    # =====================================================================

    def _validate_image_path(self, image_path: Path) -> Path:
        """Validate that an image path exists.

        Args:
            image_path: Path to validate

        Returns:
            The validated path

        Raises:
            ImageError: If path does not exist
        """
        if not image_path.exists():
            raise ImageError(f"Image file not found: {image_path}")

        try:
            current_size = image_path.stat().st_size
        except (OSError, AttributeError):
            raise ImageError("Failed to get image size") from None

        if current_size == 0:
            raise ImageEmptyError(f"Image file is empty: {image_path}")

        return image_path

    def compress(
        self, image_path: Path, level: int = 6, keep_source: bool = False
    ) -> Path:
        """Compress the image using zstd.

        Args:
            image_path: Path to the image file to compress.
            level: Compression level (1-22, default 6 for speed/size balance)
            keep_source: If True, keep the source file after successful
                         compression. Default False (source is deleted).

        Returns:
            Path to the compressed file (with .zst suffix)

        Raises:
            ImageCompressionError: If compression fails
            ImageEmptyError: If source file is empty
            ImageCorruptError: If source file appears to be all zeros
        """
        import zstandard as zstd

        if not image_path.exists():
            raise ImageCompressionError(
                f"Cannot compress: source file does not exist: {image_path}"
            )

        original_size = image_path.stat().st_size

        with open(image_path, "rb") as f:
            first_mb = f.read(CONST_MEBIBYTE_BYTES)
            if first_mb == b"\x00" * len(first_mb):
                raise ImageCorruptError(
                    f"Source file appears to be all zeros: {image_path}. "
                    f"File may be corrupted."
                )

        compressed_path = image_path.with_suffix(".zst")

        try:
            compressor = zstd.ZstdCompressor(level=level)
            with (
                open(image_path, "rb") as src,
                open(compressed_path, "wb") as dst,
            ):
                compressor.copy_stream(src, dst)

            if not compressed_path.exists():
                raise ImageCompressionError(
                    f"Compression failed: output not created: {compressed_path}"
                )

            compressed_size = compressed_path.stat().st_size
            if compressed_size == 0:
                compressed_path.unlink(missing_ok=True)
                raise ImageCompressionError(
                    f"Compression failed: output is empty (source was {original_size} bytes)"
                )

            ratio = original_size / compressed_size

            if not keep_source:
                image_path.unlink()

            logger.info(
                "Compressed %s: %d MB → %d MB (%.1fx reduction)",
                image_path.name,
                original_size // CONST_MEBIBYTE_BYTES,
                compressed_size // CONST_MEBIBYTE_BYTES,
                ratio,
            )
            return compressed_path
        except OSError as e:
            raise ImageCompressionError(f"Failed to compress image: {e}") from e

    def decompress(
        self,
        compressed_path: Path,
        output_path: Path,
        compressed_format: str | None = None,
    ) -> None:
        """Decompress the image to the specified output path.

        Args:
            compressed_path: Path to the compressed image file.
            output_path: Path where the decompressed file should be written.
            compressed_format: Compression format (e.g. "zst"). If None, defaults to "zst".

        Raises:
            ImageDecompressionError: If decompression fails or source not found
        """
        import zstandard as zstd

        fmt = compressed_format or "zst"
        if fmt is not None and fmt not in ("zst",):
            raise ImageDecompressionError(
                f"Unsupported compression format: {fmt!r}. Only 'zst' (zstd) is supported."
            )

        self._validate_image_path(compressed_path)

        try:
            decompressor = zstd.ZstdDecompressor()
            with (
                open(compressed_path, "rb") as src,
                open(output_path, "wb") as dst,
            ):
                decompressor.copy_stream(src, dst)

            try:
                self._validate_image_path(output_path)
            except ImageEmptyError:
                output_path.unlink(missing_ok=True)
                raise ImageDecompressionError(
                    f"Decompression failed: output could not be verified: {output_path}"
                )

            output_size = output_path.stat().st_size

            logger.info(
                "Decompressed %s → %s (%d MB)",
                compressed_path.name,
                output_path.name,
                output_size // CONST_MEBIBYTE_BYTES,
            )
        except OSError as e:
            raise ImageDecompressionError(
                f"Failed to decompress image: {e}"
            ) from e

    def ensure_cached(self, images: list[ImageItem]) -> list[Path]:
        """Ensure images are decompressed to tmpfs cache, creating if needed.

        This maintains a tmpfs-based cache of decompressed images
        for fast cloning. First call decompresses to RAM, subsequent calls
        return the cached path immediately.

        Args:
            images: List of ImageItem to cache.

        Returns:
            List of paths to cached images (in tmpfs/RAM).

        Raises:
            ImageDecompressionError: If decompression fails.
        """
        from mvmctl.utils.common import CacheUtils

        results: list[Path] = []
        for image in images:
            cached_path = (
                CacheUtils.get_warm_image_dir() / f"{image.id}.{image.fs_type}"
            )

            if cached_path.exists():
                logger.debug("Found image in cache: %s", cached_path)
                results.append(cached_path)
                continue

            fmt = image.compressed_format or "zst"
            suffix = f".{fmt}" if not fmt.startswith(".") else fmt
            compressed_path = Path(image.path).with_suffix(suffix)

            logger.info("Decompressing to cache: %s", cached_path.name)
            self.decompress(compressed_path, cached_path, compressed_format=fmt)
            results.append(cached_path)

        return results

    # =====================================================================
    # Shrinking
    # =====================================================================

    def shrink_with_guestfs(self, image_path: Path) -> tuple[Path, int, int]:
        """Shrink an image to its minimum size using libguestfs."""
        try:
            og = OptimizedGuestfs(image_path, readonly=False)
        except GuestfsNotAvailableError:
            logger.warning("libguestfs not available, skipping image shrink")
            return (
                image_path,
                image_path.stat().st_size,
                image_path.stat().st_size,
            )

        self._validate_image_path(image_path)

        original_size = image_path.stat().st_size

        try:
            with og:
                partitions = og.list_partitions()
                root_device = partitions[0] if partitions else "/dev/sda"

                fs_type = og.vfs_type(root_device)

                if fs_type in ("ext2", "ext3", "ext4"):
                    og.shrink_ext4(root_device)
                elif fs_type == "btrfs":
                    og.shrink_btrfs(root_device)
                else:
                    if fs_type:
                        logger.debug(
                            "Skipping shrink: %s filesystem not supported for shrinking",
                            fs_type,
                        )
                    else:
                        logger.debug(
                            "Skipping shrink: filesystem type could not be detected (may already be minimal or raw image)"
                        )
                    return image_path, original_size, original_size

                new_size = og.blockdev_getsize64(root_device)

            final_size = int(new_size * CONST_SHRINK_SAFETY_MARGIN)
            with open(image_path, "r+b") as f:
                f.truncate(final_size)

            actual_final = image_path.stat().st_size
            logger.info(
                "Shrunk %s: %d MB → %d MB (%.1fx reduction)",
                image_path.name,
                original_size // CONST_MEBIBYTE_BYTES,
                actual_final // CONST_MEBIBYTE_BYTES,
                original_size / actual_final
                if actual_final > 0
                else CONST_RATIO_MIN,
            )

            return image_path, original_size, actual_final

        except Exception as e:
            logger.debug("Failed to shrink image: %s", e)
            return image_path, original_size, image_path.stat().st_size

    def grow_rootfs_with_guestfs(
        self, image_path: Path, target_size_bytes: int
    ) -> None:
        """Grow the root filesystem to target size using libguestfs.

        Args:
            image_path: Path to the disk image.
            target_size_bytes: Target size in bytes to grow the filesystem to.

        Raises:
            ImageError: If libguestfs is unavailable, target size is smaller than
                current size, or resize operation fails.
        """
        try:
            og = OptimizedGuestfs(image_path, readonly=False)
        except GuestfsNotAvailableError:
            raise ImageError("libguestfs required for disk resize") from None

        self._validate_image_path(image_path)

        current_size = image_path.stat().st_size

        if current_size >= target_size_bytes:
            raise ImageError(
                f"Requested disk size ({target_size_bytes // CONST_MEBIBYTE_BYTES} MB) "
                f"is smaller than current image size ({current_size // CONST_MEBIBYTE_BYTES} MB). "
                "Cannot shrink filesystem. Use a larger size or recreate VM with smaller image."
            )

        try:
            with open(image_path, "r+b") as file_handle:
                file_handle.truncate(target_size_bytes)

            with og:
                partitions = og.list_partitions()
                root_device = partitions[0] if partitions else "/dev/sda"
                og.grow_fs(root_device, target_size_bytes)

            logger.info(
                "Grew rootfs: %d MB --> %d MB",
                current_size // CONST_MEBIBYTE_BYTES,
                target_size_bytes // CONST_MEBIBYTE_BYTES,
            )
        except ImageError:
            raise
        except Exception as exc:
            raise ImageError(f"Failed to grow rootfs: {exc}") from exc

    # =====================================================================
    # Format Conversion
    # =====================================================================

    def convert_qcow2_to_raw(
        self,
        qcow2_path: Path,
        raw_path: Path,
    ) -> bool:
        """Convert qcow2 to raw using qemu-img."""
        try:
            logger.info("Converting %s to raw...", qcow2_path.name)

            subprocess.run(
                [
                    "qemu-img",
                    "convert",
                    "-m",
                    "16",
                    "-f",
                    "qcow2",
                    "-O",
                    "raw",
                    "-t",
                    "none",
                    "-T",
                    "none",
                    "-W",
                    str(qcow2_path),
                    str(raw_path),
                ],
                capture_output=True,
                text=True,
                check=True,
            )

            logger.info("Converted to %s", raw_path.name)
            return True

        except subprocess.CalledProcessError as e:
            detail = e.stderr.strip() if e.stderr else "no details"
            raise ImageError(f"qemu-img conversion failed: {detail}") from e
        except FileNotFoundError as e:
            raise ImageError("qemu-img not found. Install qemu-utils.") from e

    def convert_vhd_to_raw(
        self,
        vhd_path: Path,
        raw_path: Path,
    ) -> bool:
        """Convert VHD to raw using qemu-img."""
        try:
            logger.info("Converting %s to raw...", vhd_path.name)

            subprocess.run(
                [
                    "qemu-img",
                    "convert",
                    "-m",
                    "16",
                    "-f",
                    "vpc",
                    "-O",
                    "raw",
                    "-t",
                    "none",
                    "-T",
                    "none",
                    "-W",
                    str(vhd_path),
                    str(raw_path),
                ],
                capture_output=True,
                text=True,
                check=True,
            )

            logger.info("Converted to %s", raw_path.name)
            return True

        except subprocess.CalledProcessError as e:
            detail = e.stderr.strip() if e.stderr else "no details"
            raise ImageError(f"qemu-img conversion failed: {detail}") from e
        except FileNotFoundError as e:
            raise ImageError("qemu-img not found. Install qemu-utils.") from e

    def create_ext4_from_tar(
        self,
        tar_path: Path,
        output_path: Path,
        minimum_rootfs_mib: int | str,
    ) -> bool:
        """Create ext4 image from tar archive."""
        import tempfile

        try:
            logger.info("Creating ext4 image from %s...", tar_path.name)

            with tempfile.TemporaryDirectory() as tmpdir:
                logger.debug("Extracting tar to %s...", tmpdir)

                cmd = [
                    "tar",
                    "-xf",
                    str(tar_path),
                    "-C",
                    tmpdir,
                    "--exclude=dev/*",
                    "--no-same-owner",
                    "--no-same-permissions",
                ]
                subprocess.run(cmd, capture_output=True, check=True)

                subprocess.run(
                    ["chmod", "-R", "u+rwx", tmpdir],
                    capture_output=True,
                    check=False,
                )

                du_result = subprocess.run(
                    ["du", "-sb", tmpdir], capture_output=True, text=True
                )
                if du_result.returncode not in (0, 1):
                    raise ImageError(
                        f"Failed to get directory size: {du_result.stderr}"
                    )

                actual_bytes = int(du_result.stdout.split()[0])
                actual_mib = actual_bytes / CONST_MEBIBYTE_BYTES

                if minimum_rootfs_mib == "dynamic":
                    calculated_mib = int(
                        actual_mib * CONST_ROOTFS_HEADROOM_FACTOR
                    )
                    raw_size_mb = max(CONST_MIN_ROOTFS_SIZE_MIB, calculated_mib)
                else:
                    calculated_mib = int(
                        int(minimum_rootfs_mib) * CONST_ROOTFS_HEADROOM_FACTOR
                    )
                    raw_size_mb = max(CONST_MIN_ROOTFS_SIZE_MIB, calculated_mib)

                logger.info("Creating ext4 image (%d MiB)...", raw_size_mb)

                subprocess.run(
                    ["truncate", "-s", f"{raw_size_mb}M", str(output_path)],
                    capture_output=True,
                    check=True,
                )

                subprocess.run(
                    ["mkfs.ext4", "-d", tmpdir, "-F", str(output_path)],
                    capture_output=True,
                    check=True,
                )

            logger.info("Created %s", output_path.name)
            return True

        except subprocess.CalledProcessError as e:
            stderr_msg = (
                e.stderr.decode()
                if isinstance(e.stderr, bytes)
                else (e.stderr if e.stderr else "no_details")
            )
            logger.error("Failed to create ext4 image: %s", stderr_msg)
            raise ImageError(
                f"Failed to create ext4 image: {stderr_msg}"
            ) from e
        except FileNotFoundError as e:
            raise ImageError(
                "Required tool not found: tar, truncate, or mkfs.ext4"
            ) from e

    # =====================================================================
    # Partition & Filesystem Helpers
    # =====================================================================

    def extract_partition(
        self,
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Extract root partition from raw disk image."""
        from mvmctl.utils.partition_detection import RootPartitionDetector

        try:
            # Check if the image is a direct filesystem (superfloppy) using blkid
            fs_type = self.detect_filesystem_type(raw_path)
            if fs_type in ("ext4", "ext3", "ext2", "btrfs", "xfs"):
                logger.info("Image is %s filesystem, using as-is", fs_type)
                shutil.copy2(raw_path, output_path)
                return output_path

            parsed = self._parse_partitions_sfdisk(raw_path, partition)
            if parsed is None:
                parsed = self._parse_partitions_fdisk(raw_path, partition)

            if isinstance(parsed, _NoPartitionTable):
                logger.info("No partition table found, using image as-is")
                raw_path.rename(output_path)
                return output_path

            if not isinstance(parsed, tuple):
                raise ImageError(
                    f"Unexpected parse result type: {type(parsed).__name__}"
                )

            partitions, requested_partition = parsed

            if len(partitions) == 0:
                logger.info("No partitions found, using image as-is")
                raw_path.rename(output_path)
                return output_path

            # Determine which partition to extract
            if len(partitions) > 1 and requested_partition is None:
                logger.info("Found %d partitions:", len(partitions))
                for i, p in enumerate(partitions, 1):
                    logger.debug(
                        "  %d: start=%s size=%s type=%s",
                        i,
                        p.get("start"),
                        p.get("size"),
                        p.get("type", "?"),
                    )
                detector = RootPartitionDetector(
                    disabled_detectors=disabled_detectors
                )
                chosen_idx = detector.detect(partitions)
                logger.info(
                    "Detector selected partition %d as root", chosen_idx
                )
                chosen = partitions[chosen_idx - 1]
                partition_num = chosen_idx
            elif requested_partition is not None:
                if requested_partition < 1 or requested_partition > len(
                    partitions
                ):
                    raise ImageError(
                        f"Partition {requested_partition} out of range (1-{len(partitions)})"
                    )
                logger.info("Found %d partitions:", len(partitions))
                logger.info("Using partition %d as root", requested_partition)
                chosen = partitions[requested_partition - 1]
                partition_num = requested_partition
            else:
                chosen = partitions[0]
                partition_num = 1

            start_sector = safe_int(chosen.get("start"), 0)
            size_val = chosen.get("size")
            sector_count: int | None = (
                safe_int(size_val, 0) if size_val else None
            )

            skip_bytes = start_sector * _SECTOR_SIZE
            count_bytes = sector_count * _SECTOR_SIZE if sector_count else None

            # Validate extraction is within file bounds
            raw_file_size = raw_path.stat().st_size
            if skip_bytes >= raw_file_size:
                raise ImageError(
                    f"Partition {partition_num} start sector ({start_sector}) "
                    f"offset ({skip_bytes} bytes) exceeds file size ({raw_file_size} bytes). "
                    f"Partition table may be corrupted or in unsupported format."
                )

            logger.info(
                "Extracting partition %d (start=%d, offset=%d bytes)...",
                partition_num,
                start_sector,
                skip_bytes,
            )

            self._copy_bytes(raw_path, output_path, skip_bytes, count_bytes)

            output_path = self._detect_and_rename_fs(output_path)

            logger.info("Extracted to %s", output_path.name)
            return output_path

        except OSError as e:
            raise ImageError("Extraction failed") from e
        except (IndexError, ValueError) as e:
            raise ImageError("Failed to parse partition table") from e

    def detect_filesystem_type(self, image_path: Path) -> str | None:
        """Detect filesystem type using blkid."""
        try:
            blkid_result = subprocess.run(
                ["blkid", "-o", "value", "-s", "TYPE", str(image_path)],
                capture_output=True,
                text=True,
                check=False,
            )
            fs_type = blkid_result.stdout.strip()
            return fs_type if fs_type else None
        except FileNotFoundError:
            return None

    def get_filesystem_uuid(self, image_path: Path) -> str | None:
        """Get filesystem UUID from image using blkid."""
        try:
            blkid_result = subprocess.run(
                ["blkid", "-p", "-s", "UUID", "-o", "value", str(image_path)],
                capture_output=True,
                text=True,
                check=False,
            )
        except FileNotFoundError:
            return None

        fs_uuid = blkid_result.stdout.strip()
        return fs_uuid if fs_uuid else None

    def _detect_and_rename_fs(self, output_path: Path) -> Path:
        """Detect filesystem type via blkid and rename output file accordingly."""
        try:
            blkid_result = subprocess.run(
                ["blkid", "-o", "value", "-s", "TYPE", str(output_path)],
                capture_output=True,
                text=True,
                check=False,
            )
            fs_type = blkid_result.stdout.strip()
            if fs_type:
                ext_map = {"ext4": ".ext4", "btrfs": ".btrfs", "xfs": ".xfs"}
                ext = ext_map.get(fs_type, ".img")
                final_path = output_path.with_suffix(ext)
                output_path.rename(final_path)
                output_path = final_path
                logger.info("Detected filesystem: %s", fs_type)
        except FileNotFoundError:
            pass
        return output_path

    def _parse_partitions_sfdisk(
        self,
        raw_path: Path,
        partition: int | None,
    ) -> tuple[list[dict[str, object]], int | None] | _NoPartitionTable | None:
        """Parse partition table using sfdisk."""
        import json as json_mod

        try:
            sfdisk_result = subprocess.run(
                ["sfdisk", "--json", str(raw_path)],
                capture_output=True,
                text=True,
                check=True,
            )
            table = json_mod.loads(sfdisk_result.stdout)
            partitions_raw = table.get("partitiontable", {}).get(
                "partitions", []
            )

            if not partitions_raw:
                return _NO_PARTITION_TABLE

            partitions: list[dict[str, object]] = []
            for p in partitions_raw:
                start = p.get("start")
                size = p.get("size")
                if not isinstance(start, (int, float)) or not isinstance(
                    size, (int, float)
                ):
                    raise ImageError("Failed to parse partition table")
                partitions.append(
                    {
                        "start": int(start),
                        "size": int(size),
                        "type": p.get("type", ""),
                        "node": p.get("node", ""),
                    }
                )

            return partitions, partition

        except (
            FileNotFoundError,
            subprocess.CalledProcessError,
            json_mod.JSONDecodeError,
            KeyError,
        ):
            return None

    def _parse_partitions_fdisk(
        self,
        raw_path: Path,
        partition: int | None,
    ) -> tuple[list[dict[str, object]], int | None] | _NoPartitionTable:
        """Parse partition table using fdisk (fallback when sfdisk unavailable)."""
        result = subprocess.run(
            ["fdisk", "-l", str(raw_path)],
            capture_output=True,
            text=True,
            check=False,
        )

        partition_lines = [
            line
            for line in result.stdout.split("\n")
            if re.match(rf"^{re.escape(str(raw_path))}p?\d", line)
        ]

        if not partition_lines:
            return _NO_PARTITION_TABLE

        partitions: list[dict[str, object]] = []
        for line in partition_lines:
            parts = line.split()
            if len(parts) >= 6:
                try:
                    start = int(parts[3])
                    size = int(parts[4])
                    part_type = parts[5] if len(parts) > 5 else ""
                    partitions.append(
                        {
                            "start": start,
                            "size": size,
                            "type": part_type,
                        }
                    )
                except (ValueError, IndexError):
                    raise ImageError(
                        "Failed to parse fdisk output for partition sectors"
                    )

        if not partitions:
            return _NO_PARTITION_TABLE

        return partitions, partition

    def _copy_bytes(
        self,
        src: Path,
        dst: Path,
        offset: int,
        count: int | None,
    ) -> None:
        """Copy bytes from *src* starting at *offset* into *dst*."""
        with open(src, "rb") as fin, open(dst, "wb") as fout:
            fin.seek(offset)
            remaining = count
            while True:
                chunk_size = _COPY_CHUNK_SIZE
                if remaining is not None:
                    chunk_size = min(chunk_size, remaining)
                data = fin.read(chunk_size)
                if not data:
                    break
                fout.write(data)
                if remaining is not None:
                    remaining -= len(data)
                    if remaining <= 0:
                        break

    def _calculate_minimum_image_size_mb(self, content_bytes: int) -> int:
        """Calculate minimum image size in MiB based on actual content bytes.

        Uses decimal MB (1,000,000 bytes) for calculation with headroom factor,
        then converts to MiB for filesystem operations.
        """
        from mvmctl.constants import CONST_MEGABYTE_BYTES

        content_mb_decimal = content_bytes / CONST_MEGABYTE_BYTES
        calculated_mb = int(content_mb_decimal * CONST_ROOTFS_HEADROOM_FACTOR)
        return max(CONST_MIN_ROOTFS_SIZE_MIB, calculated_mb)

    # =====================================================================
    # Format Handlers
    # =====================================================================

    def _handle_qcow2(
        self,
        *,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle qcow2 format."""
        with tempfile.TemporaryDirectory() as tmpdir:
            raw_path = Path(tmpdir) / "intermediate.raw"
            self.convert_qcow2_to_raw(input_path, raw_path)

            # Try guestfs-based extraction first (more reliable)
            actual_path = OptimizedGuestfs.extract_partition(
                raw_path, final_path.with_suffix(".img"), partition
            )
            if actual_path is not None:
                return actual_path

            # Fall back to sfdisk/fdisk parsing
            logger.info(
                "Guestfs extraction unavailable, falling back to manual partition parsing"
            )
            actual_path = self.extract_partition(
                raw_path,
                final_path.with_suffix(".img"),
                partition=partition,
                disabled_detectors=disabled_detectors,
            )
            return actual_path

    def _handle_tar_rootfs(
        self,
        *,
        input_path: Path,
        final_path: Path,
        minimum_rootfs_size: int | str,
    ) -> Path:
        """Handle tar-rootfs format."""
        self.create_ext4_from_tar(
            input_path, final_path, minimum_rootfs_mib=minimum_rootfs_size
        )
        return final_path

    def _handle_raw(
        self,
        *,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle raw format."""
        return self.extract_partition(
            input_path,
            final_path.with_suffix(".img"),
            partition=partition,
            disabled_detectors=disabled_detectors,
        )

    def _handle_squashfs(
        self,
        *,
        input_path: Path,
        final_path: Path,
        minimum_rootfs_size: int | str,
    ) -> Path:
        """Handle squashfs format."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir_path = Path(tmpdir)
            extract_dir = tmpdir_path / "squashfs-root"

            try:
                subprocess.run(
                    ["unsquashfs", "-d", str(extract_dir), str(input_path)],
                    capture_output=True,
                    check=True,
                )
            except subprocess.CalledProcessError as e:
                raise ImageError("unsquashfs failed") from e
            except FileNotFoundError as e:
                raise ImageError(
                    "unsquashfs not found. Install squashfs-tools."
                ) from e

            if not shutil.which("mkfs.ext4"):
                raise ImageError(
                    "mkfs.ext4 not found. Install e2fsprogs package."
                )

            try:
                du_result = subprocess.run(
                    ["du", "-sb", str(extract_dir)],
                    capture_output=True,
                    text=True,
                    check=True,
                )
                content_bytes = int(du_result.stdout.split()[0])
            except (subprocess.CalledProcessError, ValueError, IndexError):
                content_bytes = 0

            if minimum_rootfs_size == "dynamic":
                image_size_mb = self._calculate_minimum_image_size_mb(
                    content_bytes
                )
            else:
                image_size_mb = int(minimum_rootfs_size)

            try:
                subprocess.run(
                    ["truncate", "-s", f"{image_size_mb}M", str(final_path)],
                    capture_output=True,
                    check=True,
                )
            except subprocess.CalledProcessError as e:
                raise ImageError("Failed to allocate ext4 image file") from e

            try:
                subprocess.run(
                    [
                        "mkfs.ext4",
                        "-d",
                        str(extract_dir),
                        "-L",
                        "",
                        str(final_path),
                    ],
                    capture_output=True,
                    text=True,
                    check=True,
                )
            except subprocess.CalledProcessError as e:
                stderr_msg = e.stderr.strip() if e.stderr else "no_details"
                raise ImageError(
                    f"Failed to create ext4 from squashfs: {stderr_msg}"
                ) from e

        logger.info("Created ext4 from squashfs: %s", final_path)
        return final_path

    def _handle_vhd(
        self,
        *,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle VHD format.

        Tries guestfs-based extraction first for reliability with non-standard
        VHD images (e.g., Alpine), falls back to sfdisk/fdisk parsing.
        """
        with tempfile.TemporaryDirectory() as tmpdir:
            raw_path = Path(tmpdir) / "intermediate.raw"
            self.convert_vhd_to_raw(input_path, raw_path)

            # Try guestfs-based extraction first (more reliable for VHD)
            actual_path = OptimizedGuestfs.extract_partition(
                raw_path, final_path.with_suffix(".img"), partition
            )
            if actual_path is not None:
                return actual_path

            # Fall back to sfdisk/fdisk parsing
            logger.info(
                "Guestfs extraction unavailable, falling back to manual partition parsing"
            )
            actual_path = self.extract_partition(
                raw_path,
                final_path.with_suffix(".img"),
                partition=partition,
                disabled_detectors=disabled_detectors,
            )
            if actual_path is None:
                raise ImageError("Failed to extract partition from VHD")
            return actual_path

    # =====================================================================
    # Format Handler Dictionary (as class property)
    # =====================================================================

    @property
    def _format_handlers(self) -> dict[str, Callable[..., Path]]:
        return {
            "qcow2": self._handle_qcow2,
            "tar-rootfs": self._handle_tar_rootfs,
            "raw": self._handle_raw,
            "squashfs": self._handle_squashfs,
            "vhd": self._handle_vhd,
        }

    # =====================================================================
    # Fetch / Import Orchestration
    # =====================================================================

    def _get_template_variables(
        self, spec: ImageSpec, ci_version: str
    ) -> dict[str, str]:
        """Build template variables dict from ImageSpec."""
        variables = {
            "ci_version": ci_version,
            "arch": spec.arch,
            "image_type": spec.image_type,
            "version": spec.version,
            "image_version": spec.version,
            "ubuntu_version": spec.version,
        }
        return {k: str(v) for k, v in variables.items()}

    def _resolve_source_template(
        self, spec: ImageSpec, template_vars: dict[str, str]
    ) -> str:
        """Resolve source URL by fetching and parsing CI image list."""
        if not spec.list_url_template:
            raise ImageError(
                f"Missing 'list_url_template' in images.yaml for {spec.id}"
            )
        if not spec.source_base:
            raise ImageError(
                f"Missing 'source_base' in images.yaml for {spec.id}"
            )

        list_url = render_template(spec.list_url_template, template_vars)

        try:
            xml_content = HttpDownload.read_raw_content(list_url)
        except Exception as e:
            logger.debug(
                "Failed to list Firecracker CI ubuntu images from %s",
                list_url,
                exc_info=True,
            )
            raise ImageError(
                "Failed to list Firecracker CI ubuntu images"
            ) from e

        ci_version = template_vars["ci_version"]
        arch = template_vars["arch"]
        pattern = (
            rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/"
            rf"ubuntu-[0-9.]+\.squashfs)</Key>"
        )
        keys = re.findall(pattern, xml_content)
        if not keys:
            raise ImageError(
                f"No ubuntu squashfs found for CI version {ci_version} / arch {arch}"
            )

        keys.sort()
        chosen_key = keys[-1]
        return f"{spec.source_base}/{chosen_key}"

    def _fetch_sha256_from_url(
        self,
        sha256_url: str,
        source_filename: str | None = None,
    ) -> str | None:
        """Fetch SHA256 checksum from URL."""
        from mvmctl.exceptions import HttpDownloadError

        try:
            content = HttpDownload.read_raw_content(
                sha256_url, timeout=HTTP_TIMEOUT_SHA256_FETCH_S
            ).strip()
        except HttpDownloadError:
            return None

        if source_filename is None:
            parts: list[str] = content.split()
            if not parts:
                return None
            return parts[0].lower()

        source_basename = Path(source_filename).name
        for line in content.splitlines():
            line = line.strip()
            if not line:
                continue
            line_parts: list[str] = line.split()
            if len(line_parts) < 2:
                continue
            filename_in_line = line_parts[-1].lstrip("*")
            filename_in_line_basename = Path(filename_in_line).name
            if (
                filename_in_line == source_filename
                or filename_in_line == source_basename
                or filename_in_line_basename == source_filename
                or filename_in_line_basename == source_basename
            ):
                return line_parts[0].lower()
        return None

    def _validate_downloaded_file(
        self,
        downloaded_path: Path,
        image_format: str,
    ) -> None:
        """Validate downloaded file is valid for its format.

        Uses Python-only header checks — no external tools.
        Unlinks the file on validation failure.
        """
        if not downloaded_path.exists():
            raise ImageValidationError("Downloaded file not found")

        file_size = downloaded_path.stat().st_size
        if file_size == 0:
            downloaded_path.unlink(missing_ok=True)
            raise ImageValidationError("Downloaded file is empty")

        if image_format == "qcow2":
            self._validate_qcow2(downloaded_path)
        elif image_format == "vhd":
            self._validate_vhd(downloaded_path, file_size)
        elif image_format == "raw":
            self._validate_raw(downloaded_path, file_size)
        elif image_format == "squashfs":
            self._validate_squashfs(downloaded_path)
        elif image_format == "tar-rootfs":
            self._validate_tar(downloaded_path)
        else:
            downloaded_path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Unknown format for validation: {image_format}"
            )

    def _validate_qcow2(self, path: Path) -> None:
        """Validate qcow2 by magic number, version, and size."""
        try:
            with open(path, "rb") as f:
                magic = f.read(4)
                if magic != b"QFI\xfb":
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        "Invalid qcow2 file: wrong magic number"
                    )

                version = struct.unpack(">I", f.read(4))[0]
                if version not in (2, 3):
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        f"Unsupported qcow2 version: {version} (expected 2 or 3)"
                    )

                f.seek(24)
                size = struct.unpack(">Q", f.read(8))[0]
                if size == 0:
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        "Invalid qcow2 file: zero virtual size"
                    )
        except (OSError, struct.error) as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate qcow2 file: {e}"
            ) from e

    def _validate_vhd(self, path: Path, file_size: int) -> None:
        """Validate VHD by footer cookie and basic fields."""
        if file_size < 512:
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid VHD file: too small")

        try:
            with open(path, "rb") as f:
                # Try 512-byte footer first (standard)
                f.seek(file_size - 512)
                footer = f.read(512)
                if footer[:8] != b"conectix":
                    # Fallback: pre-2004 511-byte footer
                    if file_size >= 511:
                        f.seek(file_size - 511)
                        footer = f.read(511)
                    if footer[:8] != b"conectix":
                        path.unlink(missing_ok=True)
                        raise ImageValidationError(
                            "Invalid VHD file: missing conectix cookie"
                        )

                features = struct.unpack(">I", footer[8:12])[0]
                if not (features & 0x00000002):
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        "Invalid VHD file: reserved bit not set"
                    )

                disk_type = struct.unpack(">I", footer[60:64])[0]
                if disk_type not in (2, 3, 4):
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        f"Invalid VHD file: unknown disk type {disk_type}"
                    )
        except (OSError, struct.error) as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate VHD file: {e}"
            ) from e

    def _validate_raw(self, path: Path, file_size: int) -> None:
        """Validate raw image by size and non-zero content."""
        if file_size < _SECTOR_SIZE:
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid raw image: too small")

        if file_size % _SECTOR_SIZE != 0:
            logger.warning("Raw image size %d is not sector-aligned", file_size)

        try:
            with open(path, "rb") as f:
                first_kb = f.read(1024)
                if first_kb == b"\x00" * len(first_kb):
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        "Invalid raw image: file appears to be all zeros"
                    )
        except OSError as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate raw image: {e}"
            ) from e

    def _validate_squashfs(self, path: Path) -> None:
        """Validate squashfs by magic number and version."""
        try:
            with open(path, "rb") as f:
                magic = struct.unpack("<I", f.read(4))[0]
                if magic != 0x73717368:
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        "Invalid squashfs file: wrong magic number"
                    )

                f.seek(28)
                major = struct.unpack("<H", f.read(2))[0]
                minor = struct.unpack("<H", f.read(2))[0]
                if major != 4:
                    path.unlink(missing_ok=True)
                    raise ImageValidationError(
                        f"Unsupported squashfs version: {major}.{minor} (expected 4.x)"
                    )
        except (OSError, struct.error) as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate squashfs file: {e}"
            ) from e

    def _validate_tar(self, path: Path) -> None:
        """Validate tar archive using Python's tarfile module."""
        try:
            with tarfile.open(path, "r:*") as tf:
                # Just iterate headers to validate structure
                for _member in tf:
                    pass
        except tarfile.TarError as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(f"Invalid tar file: {e}") from e
        except OSError as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate tar file: {e}"
            ) from e

    # =====================================================================
    # Phase Methods
    # =====================================================================

    def download_image(
        self,
        spec: ImageSpec,
        image_id: str,
        output_dir: Path,
        force: bool,
        ci_version: str,
    ) -> Path:
        """Download image from remote source. Returns path to downloaded file."""
        download_path = output_dir / f"{image_id}.download"

        if force and download_path.exists():
            download_path.unlink()

        template_vars = self._get_template_variables(spec, ci_version)
        source = spec.source
        if "{" in spec.source:
            source = self._resolve_source_template(spec, template_vars)

        resolved_sha256 = (
            spec.sha256.lower() if spec.sha256 is not None else None
        )
        sha256_url = render_optional_template(spec.sha256_url, template_vars)
        if resolved_sha256 is None and sha256_url is not None:
            source_basename = source.rsplit("/", 1)[-1] if source else None
            resolved_sha256 = self._fetch_sha256_from_url(
                sha256_url, source_filename=source_basename
            )

        HttpDownload.download_file(
            source,
            download_path,
            expected_sha256=resolved_sha256,
            timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
            progress_bar=True,
            allow_missing_checksum=resolved_sha256 is None,
            title=f"Downloading image: '{spec.id}'",
        )
        self._validate_downloaded_file(download_path, spec.format)

        return download_path

    def extract_downloaded_image(
        self,
        download_path: Path,
        spec: ImageSpec,
        image_id: str,
        output_dir: Path,
        partition: int | None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Extract/convert downloaded image to final format. Returns extracted path."""
        logger.info("Preparing & optimizing image...")
        handler = self._format_handlers.get(spec.format)
        if handler is None:
            raise ImageError(f"Unknown format: {spec.format}")

        actual_path = handler(
            input_path=download_path,
            final_path=output_dir / f"{image_id}.{spec.convert_to}",
            minimum_rootfs_size="dynamic",
            partition=partition,
            disabled_detectors=disabled_detectors,
        )

        if actual_path is None:
            raise ImageError("Failed to determine image path")

        return actual_path

    def extract_import_image(
        self,
        source_path: Path,
        image_id: str,
        output_dir: Path,
        format: str,
        convert_to: str,
        partition: int | None,
        disabled_detectors: list[str] | None,
    ) -> Path:
        """Extract/convert imported local image. Returns extracted path."""
        logger.info(
            "Importing %s as '%s' (format: %s)...",
            source_path.name,
            image_id,
            format,
        )

        handler = self._format_handlers.get(format)
        if handler is None:
            raise ImageError(f"Unsupported import format: {format}")

        actual_path = handler(
            input_path=source_path,
            final_path=output_dir / f"{image_id}.{convert_to}",
            minimum_rootfs_size="dynamic",
            partition=partition,
            disabled_detectors=disabled_detectors,
        )

        if actual_path is None:
            raise ImageError("Failed to determine image path")

        return actual_path

    def _resolve_fs_type(self, image_path: Path) -> str:
        """Detect filesystem type via blkid or file extension. Never returns empty."""
        fs_type = self.detect_filesystem_type(image_path)
        if fs_type:
            return fs_type

        # Fall back to file extension
        ext_map = {".ext4": "ext4", ".btrfs": "btrfs", ".xfs": "xfs"}
        ext = image_path.suffix.lower()
        if ext in ext_map:
            return ext_map[ext]

        raise ImageError(
            f"Could not detect filesystem type for {image_path}. "
            "Ensure the image has a valid filesystem."
        )

    def optimize_image(
        self,
        image_path: Path,
        image_id: str,
        spec: ImageSpec,
        timestamp: str,
        skip_optimization: bool = False,
    ) -> ImageItem:
        """Shrink and compress image. Returns fully constructed ImageItem."""
        fs_type = self._resolve_fs_type(image_path)
        fs_uuid = self.get_filesystem_uuid(image_path)

        if skip_optimization:
            logger.info("Skipping optimization (shrink and compression)")
            actual_size = image_path.stat().st_size
            return ImageItem(
                id=image_id,
                os_slug=spec.id,
                os_name=spec.name,
                arch=spec.arch,
                path=str(image_path.name),
                fs_type=fs_type,
                minimum_rootfs_size_mib=actual_size // CONST_MEBIBYTE_BYTES,
                original_size=actual_size,
                is_default=False,
                pulled_at=timestamp,
                created_at=timestamp,
                updated_at=timestamp,
                fs_uuid=fs_uuid,
                compressed_size=None,
                compression_ratio=None,
                compressed_format=None,
            )

        if not image_path.exists():
            raise ImageError(
                f"Image processing failed: output file not created at {image_path}"
            )

        shrunk_path, pre_shrink_size, post_shrink_size = (
            self.shrink_with_guestfs(image_path)
        )
        shrink_successful = (
            pre_shrink_size and post_shrink_size and pre_shrink_size > 0
        )
        if shrink_successful:
            logger.info(
                "Image shrunk: %.1f MiB → %.1f MiB (%.1f%% reduction)",
                pre_shrink_size / CONST_MEBIBYTE_BYTES,
                post_shrink_size / CONST_MEBIBYTE_BYTES,
                (pre_shrink_size - post_shrink_size)
                / pre_shrink_size
                * CONST_PERCENT,
            )
        else:
            logger.debug(
                "Image shrinking not performed (filesystem type may be unsupported or detection failed)"
            )

        compressed_path = self.compress(shrunk_path)
        compressed_size = compressed_path.stat().st_size
        compression_ratio = (
            pre_shrink_size / compressed_size
            if compressed_size > 0
            else CONST_RATIO_MIN
        )

        minimum_rootfs_size_mib = (
            post_shrink_size // CONST_MEBIBYTE_BYTES
        ) + CONST_RUNTIME_BUFFER_MB

        return ImageItem(
            id=image_id,
            os_slug=spec.id,
            os_name=spec.name,
            arch=spec.arch,
            path=str(compressed_path.name),
            fs_type=fs_type,
            minimum_rootfs_size_mib=minimum_rootfs_size_mib,
            original_size=pre_shrink_size,
            is_default=False,
            pulled_at=timestamp,
            created_at=timestamp,
            updated_at=timestamp,
            fs_uuid=fs_uuid,
            compressed_size=compressed_size,
            compression_ratio=compression_ratio,
            compressed_format="zst",
        )

    # =====================================================================
    # Config Loading
    # =====================================================================

    def load_available_images(self, config_path: Path) -> list[ImageSpec]:
        """Load image specifications from YAML config file."""
        import yaml

        if not config_path.exists():
            raise ConfigError("Config not found")

        with open(config_path) as f:
            data = yaml.safe_load(f)

        arch = DEFAULT_IMAGE_ARCH
        images = []
        for img in data.get("images", []):
            image_id = img["id"]
            images.append(
                ImageSpec(
                    id=image_id,
                    image_type=img.get("type", image_id),
                    version=str(img.get("version", image_id)),
                    arch=img.get("arch", arch),
                    name=img.get("name", image_id),
                    source=img["source"],
                    format=img["format"],
                    convert_to=img["convert_to"],
                    sha256=img.get("sha256"),
                    sha256_url=img.get("sha256_url"),
                    list_url_template=img.get("list_url_template"),
                    source_base=img.get("source_base"),
                )
            )

        return images


__all__ = ["ImageService"]
