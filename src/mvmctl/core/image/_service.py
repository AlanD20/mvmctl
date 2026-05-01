"""Image processing service - handles compression, decompression, shrinking, and pool management."""

from __future__ import annotations

import logging
import os
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile
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
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core._shared import AssetManager
from mvmctl.core._shared._guestfs import OptimizedGuestfs
from mvmctl.core.image._repository import ImageRepository
from mvmctl.exceptions import (
    GuestfsNotAvailableError,
    ImageCompressionError,
    ImageCorruptError,
    ImageDecompressionError,
    ImageEmptyError,
    ImageError,
    ImageValidationError,
)
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.utils.common import CacheUtils, safe_int
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES


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

    def _process_format(
        self,
        fmt: str,
        input_path: Path,
        final_path: Path,
        minimum_rootfs_size: int | str = "dynamic",
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Route to the appropriate format handler.

        Each handler receives only the parameters it needs.
        """
        if fmt == "qcow2":
            return self._handle_qcow2(
                input_path, final_path, partition, disabled_detectors
            )
        elif fmt == "tar-rootfs":
            return self._handle_tar_rootfs(
                input_path, final_path, minimum_rootfs_size
            )
        elif fmt == "raw":
            return self._handle_raw(
                input_path, final_path, partition, disabled_detectors
            )
        elif fmt == "squashfs":
            return self._handle_squashfs(
                input_path,
                final_path,
                minimum_rootfs_size,
            )
        elif fmt == "vhd":
            return self._handle_vhd(
                input_path, final_path, partition, disabled_detectors
            )
        elif fmt == "vhdx":
            return self._handle_vhdx(
                input_path, final_path, partition, disabled_detectors
            )
        else:
            raise ImageError(f"Unknown format: {fmt}")

    def remove_many(self, images: list[ImageItem], force: bool = False) -> None:
        """Remove multiple images, enriching with VM references first.

        Uses a single batch query to enrich all images with their VMs,
        then creates a controller per image to handle removal.
        """
        from mvmctl.core.image._controller import ImageController
        from mvmctl.core.image._resolver import ImageResolver

        # Enrich with VMs (single batch query via resolver)
        resolver = ImageResolver(self._repo, include=["vm"])
        enriched = resolver._enrich(images)

        for image in enriched:
            controller = ImageController(image, self._repo)
            controller.remove(force=force)

    def remove_many_paths(self, images: list[ImageItem]) -> list[str]:
        """Remove files for multiple images from disk. No DB changes.

        Creates a controller per image to handle file removal.

        Args:
            images: List of ImageItems whose files should be removed.

        Returns:
            Flat list of all removed filenames.
        """
        from mvmctl.core.image._controller import ImageController

        removed: list[str] = []
        for image in images:
            controller = ImageController(image, self._repo)
            removed.extend(controller.remove_path())
        return removed

    def list_local(self, verify: bool = True) -> list[ImageItem]:
        """List all images, syncing is_present flag with filesystem.

        Checks each image's path on disk and bulk-updates is_present
        for any that are missing. Returns the full list with updated state.

        Args:
            verify: If True (default), check filesystem and update DB.
                   If False, return DB records as-is.
        """
        images = self._repo.list_all()
        if not verify:
            return images

        missing_ids: list[str] = []
        images_dir = CacheUtils.get_images_dir()
        for image in images:
            resolved = self._resolve_image_path(images_dir, image)
            if resolved is None or not resolved.exists():
                missing_ids.append(image.id)

        if missing_ids:
            self._repo.update_many_is_present(missing_ids, False)
            images = self._repo.list_all()

        return images

    def _resolve_image_path(
        self, images_dir: Path, image: ImageItem
    ) -> Path | None:
        """Resolve the actual filesystem path for an image.

        Tries the stored path first, then known extensions.
        Returns None if no file found.
        """
        filename = image.path
        if filename:
            candidate = images_dir / filename
            if candidate.exists():
                return candidate
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            candidate = images_dir / f"{image.id}{ext}"
            if candidate.exists():
                return candidate
        return None

    def extract_partition(
        self,
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Extract root partition from raw disk image."""
        from mvmctl.utils._disk import RootPartitionDetector

        try:
            # Check if the image is a direct filesystem (superfloppy) using blkid
            fs_type = self.detect_filesystem_type(raw_path)
            if fs_type in ("ext4", "ext3", "ext2", "btrfs", "xfs"):
                logger.info("Image is %s filesystem, using as-is", fs_type)
                try:
                    subprocess.run(
                        [
                            "cp",
                            "--sparse=always",
                            str(raw_path),
                            str(output_path),
                        ],
                        check=True,
                        capture_output=True,
                    )
                except (subprocess.CalledProcessError, FileNotFoundError):
                    self._copy_with_dd(raw_path, output_path, sparse=True)
                ext_map = {
                    "ext4": ".ext4",
                    "ext3": ".ext4",
                    "ext2": ".ext4",
                    "btrfs": ".btrfs",
                    "xfs": ".xfs",
                }
                ext = ext_map.get(fs_type, ".img")
                final_path = output_path.with_suffix(ext)
                output_path.rename(final_path)
                return final_path

            parsed = self._parse_partitions_sfdisk(raw_path, partition)
            if parsed is None:
                parsed = self._parse_partitions_parted(raw_path, partition)

            if parsed is None:
                raise ImageError(
                    "Failed to parse partition table: neither sfdisk nor parted is available or succeeded"
                )

            if isinstance(parsed, _NoPartitionTable):
                logger.info("No partition table found, using image as-is")
                shutil.move(str(raw_path), str(output_path))
                return output_path

            if not isinstance(parsed, tuple):
                raise ImageError(
                    f"Unexpected parse result type: {type(parsed).__name__}"
                )

            partitions, requested_partition = parsed

            if len(partitions) == 0:
                logger.info("No partitions found, using image as-is")
                shutil.move(str(raw_path), str(output_path))
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

            self._copy_bytes_dd(raw_path, output_path, skip_bytes, count_bytes)

            output_path = self._detect_and_rename_fs(output_path)

            logger.info("Extracted to %s", output_path.name)
            return output_path

        except OSError as e:
            raise ImageError("Extraction failed") from e
        except (IndexError, ValueError) as e:
            raise ImageError("Failed to parse partition table") from e

    def optimize_image(
        self,
        image_path: Path,
        image_id: str,
        spec: ImageSpec,
        timestamp: str,
        skip_optimization: bool = False,
    ) -> ImageItem:
        """Shrink and compress image. Returns fully constructed ImageItem."""
        import time

        t0 = time.monotonic()
        fs_type = self._resolve_fs_type(image_path)
        fs_uuid = self.get_filesystem_uuid(image_path)
        t1 = time.monotonic()
        logger.info("  fs detect: %.2fs", t1 - t0)

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
                is_present=True,
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
        t2 = time.monotonic()
        logger.info("  shrink: %.2fs", t2 - t1)
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
        t3 = time.monotonic()
        logger.info("  compress: %.2fs", t3 - t2)
        compressed_size = compressed_path.stat().st_size
        compression_ratio = (
            pre_shrink_size / compressed_size
            if compressed_size > 0
            else CONST_RATIO_MIN
        )

        minimum_rootfs_size_mib = (
            post_shrink_size // CONST_MEBIBYTE_BYTES
        ) + CONST_RUNTIME_BUFFER_MB

        logger.info("Optimization complete (total: %.2fs)", t3 - t0)

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
            is_present=True,
            pulled_at=timestamp,
            created_at=timestamp,
            updated_at=timestamp,
            fs_uuid=fs_uuid,
            compressed_size=compressed_size,
            compression_ratio=compression_ratio,
            compressed_format="zst",
        )

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
            silent_missing_checksum=resolved_sha256 is None,
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
        actual_path = self._process_format(
            spec.format,
            input_path=download_path,
            final_path=output_dir / f"{image_id}.img",
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

        actual_path = self._process_format(
            format,
            input_path=source_path,
            final_path=output_dir / f"{image_id}.img",
            partition=partition,
            disabled_detectors=disabled_detectors,
        )

        if actual_path is None:
            raise ImageError("Failed to determine image path")

        return actual_path

    def materialize_to(
        self, image_id: str, fs_type: str, output_path: Path
    ) -> None:
        """Fast durable copy from tmpfs cache to destination.

        Uses reflink (CoW) + sparse detection for maximum speed on btrfs/xfs.
        Falls back to dd conv=sparse,fsync on non-CoW filesystems.
        After copy, fdatasync() ensures data durability without flushing
        non-critical metadata (timestamps).

        Args:
            image_id: Image ID to materialize.
            fs_type: Filesystem type suffix for cache lookup.
            output_path: Destination path for the VM rootfs.

        Raises:
            ImageError: If the image is not in the cache or copy fails.
        """
        cached_path = CacheUtils.get_warm_image_dir() / f"{image_id}.{fs_type}"

        if not cached_path.exists():
            raise ImageError(f"Image not in cache: {image_id}")

        output_path.parent.mkdir(parents=True, exist_ok=True)

        try:
            subprocess.run(
                [
                    "cp",
                    "--reflink=auto",
                    "--sparse=always",
                    str(cached_path),
                    str(output_path),
                ],
                check=True,
                capture_output=True,
            )
        except (subprocess.CalledProcessError, FileNotFoundError):
            self._copy_with_dd(cached_path, output_path, sparse=True)

        with open(output_path, "rb") as f:
            os.fdatasync(f.fileno())

        logger.info("Copied image to: %s", output_path.name)

    @classmethod
    def get_specs_for(
        cls, os_slugs: list[str], version: str | None
    ) -> list[ImageSpec]:
        """Resolve ImageSpecs from the bundled images.yaml by os_slugs.

        Args:
            os_slugs: List of image IDs from images.yaml (e.g., ['ubuntu-24.04']).

        Returns:
            List of matching ImageSpecs.

        Raises:
            ImageError: If any image is not found in the catalog.
        """
        all_specs = cls.load_available_images()

        spec_map = {spec.id: spec for spec in all_specs}
        results: list[ImageSpec] = []
        missing: list[str] = []

        for os_slug in os_slugs:
            spec = spec_map.get(os_slug)
            if spec is not None and (
                version is None or spec.version == version
            ):
                results.append(spec)
            else:
                missing.append(os_slug)

        if missing:
            available = ", ".join(spec_map.keys())
            if version:
                raise ImageError(
                    f"Image(s) not found for version '{version}': "
                    f"{', '.join(missing)}. Available: {available}"
                )
            raise ImageError(
                f"Image(s) not found: {', '.join(missing)}. Available: {available}"
            )

        return results

    def compress(
        self, image_path: Path, level: int = 3, keep_source: bool = False
    ) -> Path:
        """Compress the image using zstd.

        Args:
            image_path: Path to the image file to compress.
            level: Compression level (1-22, default 3 for fast multi-threaded compression)
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
            compressor = zstd.ZstdCompressor(level=level, threads=-1)
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
            images_dir = CacheUtils.get_images_dir()
            compressed_path = images_dir / Path(image.path).with_suffix(suffix)

            logger.info("Decompressing to cache: %s", cached_path.name)
            self.decompress(compressed_path, cached_path, compressed_format=fmt)
            results.append(cached_path)

        return results

    def _has_significant_free_space(
        self, image_path: Path, threshold: float = 0.02
    ) -> bool:
        """Check if ext4 image has >threshold free space using dumpe2fs.

        Uses dumpe2fs to check block usage without mounting or booting
        a guestfs appliance. Returns True if there's significant free
        space that would benefit from shrinking.

        Args:
            image_path: Path to the ext4 image.
            threshold: Minimum free ratio to consider "significant".
                       Default 0.02 (2%). Ext4 reserves ~5% for root by
                       default, so threshold must account for that.

        Returns:
            True if free space > threshold, False otherwise.
            Returns True on any error (safe fallback: shrink anyway).
        """
        try:
            result = subprocess.run(
                ["dumpe2fs", "-h", str(image_path)],
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                return True  # Can't determine → safe to shrink

            block_count = 0
            free_blocks = 0
            for line in result.stdout.splitlines():
                if line.startswith("Block count:"):
                    block_count = int(line.split(":", 1)[1].strip())
                elif line.startswith("Free blocks:"):
                    free_blocks = int(line.split(":", 1)[1].strip())

            if block_count > 0:
                free_ratio = free_blocks / block_count
                return free_ratio > threshold
        except (FileNotFoundError, ValueError, IndexError):
            pass

        return True  # Safe fallback: shrink if we can't determine

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

        # Skip shrink if filesystem already has minimal free space
        if (
            image_path.suffix == ".ext4"
            and not self._has_significant_free_space(image_path)
        ):
            logger.debug(
                "Filesystem has <2%% free space, skipping shrink for %s",
                image_path.name,
            )
            return image_path, original_size, original_size

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

    def convert_vhdx_to_raw(
        self,
        vhdx_path: Path,
        raw_path: Path,
    ) -> bool:
        """Convert VHDX to raw using qemu-img."""
        try:
            logger.info("Converting %s to raw...", vhdx_path.name)

            subprocess.run(
                [
                    "qemu-img",
                    "convert",
                    "-m",
                    "16",
                    "-f",
                    "vhdx",
                    "-O",
                    "raw",
                    "-t",
                    "none",
                    "-T",
                    "none",
                    "-W",
                    str(vhdx_path),
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
        import time

        try:
            logger.info("Creating ext4 image from %s...", tar_path.name)
            t0 = time.monotonic()

            with tempfile.TemporaryDirectory(
                dir=CacheUtils.get_temp_dir()
            ) as tmpdir:
                t1 = time.monotonic()
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
                t2 = time.monotonic()
                logger.info("  tar extract: %.2fs", t2 - t1)

                subprocess.run(
                    ["chmod", "-R", "u+rwx", tmpdir],
                    capture_output=True,
                    check=False,
                )
                t3 = time.monotonic()
                logger.info("  chmod: %.2fs", t3 - t2)

                du_result = subprocess.run(
                    ["du", "-sb", tmpdir], capture_output=True, text=True
                )
                if du_result.returncode not in (0, 1):
                    raise ImageError(
                        f"Failed to get directory size: {du_result.stderr}"
                    )

                actual_bytes = int(du_result.stdout.split()[0])
                t4 = time.monotonic()
                logger.info(
                    "  du: %.2fs (size=%d bytes)", t4 - t3, actual_bytes
                )

                if minimum_rootfs_mib == "dynamic":
                    raw_size_mb = self._calculate_minimum_image_size_mb(
                        actual_bytes
                    )
                else:
                    raw_size_mb = int(minimum_rootfs_mib)

                logger.info("Creating ext4 image (%d MiB)...", raw_size_mb)

                subprocess.run(
                    ["truncate", "-s", f"{raw_size_mb}M", str(output_path)],
                    capture_output=True,
                    check=True,
                )
                t5 = time.monotonic()
                logger.info("  truncate: %.2fs", t5 - t4)

                subprocess.run(
                    ["mkfs.ext4", "-d", tmpdir, "-F", str(output_path)],
                    capture_output=True,
                    check=True,
                )
                t6 = time.monotonic()
                logger.info("  mkfs.ext4: %.2fs", t6 - t5)

            logger.info("Created %s (total: %.2fs)", output_path.name, t6 - t0)
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

    @classmethod
    def detect_image_format(cls, path: Path) -> str | None:
        """Detect container format from magic bytes. Returns None if unknown.

        This is a pure probe: it never modifies or deletes the file.
        """
        if not path.exists():
            return None

        file_size = path.stat().st_size
        if file_size == 0:
            return None

        if cls._is_qcow2(path):
            return "qcow2"
        if cls._is_vhd(path, file_size):
            return "vhd"
        if cls._is_vhdx(path):
            return "vhdx"
        if cls._is_squashfs(path):
            return "squashfs"
        if cls._is_tar(path):
            return "tar-rootfs"
        if cls._is_raw(path, file_size):
            return "raw"
        return None

    @classmethod
    def resolve_remote_sizes(
        cls, specs: list[ImageSpec], ci_version
    ) -> list[ImageSpec]:
        """Resolve remote image sizes via HEAD requests with HTTP caching.

        Uses the same template resolution and S3 listing logic as fetch
        so ls -r shows sizes for the same version that would be downloaded.

        Args:
            specs: List of ImageSpec to enrich with sizes.

        Returns:
            The same list with size fields populated where available.
        """
        from concurrent.futures import ThreadPoolExecutor

        from mvmctl.utils.http import HttpDownload

        def _resolve(spec: ImageSpec) -> None:
            if spec.list_url_template:
                # Dynamic image: use S3 listing to pick the latest version
                template_vars = cls._get_template_variables(spec, ci_version)
                try:
                    source = cls._resolve_source_template(spec, template_vars)
                except Exception:
                    return
            else:
                # Static image: resolve template variables directly on source
                source = spec.source
                if "{" in source:
                    from mvmctl.utils.template import render_template

                    try:
                        source = render_template(
                            source,
                            {
                                "ci_version": ci_version,
                                "arch": spec.arch,
                                "image_type": spec.image_type,
                                "version": spec.version,
                                "image_version": spec.version,
                                "ubuntu_version": spec.version,
                            },
                        )
                    except Exception:
                        return

            size = HttpDownload.head_size(source)
            if size is not None:
                spec.size = size

        with ThreadPoolExecutor(max_workers=5) as executor:
            executor.map(_resolve, specs)

        return specs

    @staticmethod
    def load_available_images() -> list[ImageSpec]:
        """Load image specifications from YAML config file."""
        import yaml

        asset = AssetManager()
        remote_images_path = asset.get_file("images.yaml")
        with open(str(remote_images_path)) as f:
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
                    sha256=img.get("sha256"),
                    sha256_url=img.get("sha256_url"),
                    list_url_template=img.get("list_url_template"),
                )
            )

        return images

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

    def _parse_partitions_parted(
        self,
        raw_path: Path,
        partition: int | None,
    ) -> tuple[list[dict[str, object]], int | None] | _NoPartitionTable | None:
        """Parse partition table using parted (fallback when sfdisk unavailable)."""
        try:
            result = subprocess.run(
                ["parted", "-sm", str(raw_path), "unit", "B", "print"],
                capture_output=True,
                text=True,
                check=True,
            )
        except (FileNotFoundError, subprocess.CalledProcessError):
            return None

        lines = result.stdout.strip().split("\n")
        if not lines or lines[0] != "BYT;":
            return None

        partitions: list[dict[str, object]] = []
        for line in lines[2:]:
            line = line.rstrip(";")
            if not line:
                continue
            parts = line.split(":")
            if len(parts) < 6:
                continue
            try:
                number = parts[0]
                start_bytes = int(parts[1].rstrip("B"))
                size_bytes = int(parts[3].rstrip("B"))
                filesystem = parts[4]
                part_type = parts[5]
            except (ValueError, IndexError):
                return None

            start_sector = start_bytes // _SECTOR_SIZE
            size_sector = size_bytes // _SECTOR_SIZE
            partitions.append(
                {
                    "start": start_sector,
                    "size": size_sector,
                    "type": part_type,
                    "node": number,
                    "fstype": filesystem,
                }
            )

        if not partitions:
            return _NO_PARTITION_TABLE

        return partitions, partition

    def _copy_bytes_dd(
        self,
        src: Path,
        dst: Path,
        skip_bytes: int,
        count_bytes: int | None,
    ) -> None:
        """Copy bytes from *src* starting at *skip_bytes* into *dst* using dd."""
        cmd = [
            "dd",
            f"if={src}",
            f"of={dst}",
            "bs=1M",
            f"skip={skip_bytes}",
            "iflag=skip_bytes,count_bytes",
            "conv=sparse,fsync",
            "status=none",
        ]
        if count_bytes is not None:
            cmd.append(f"count={count_bytes}")
        try:
            subprocess.run(cmd, check=True, capture_output=True)
        except subprocess.CalledProcessError as e:
            stderr = (
                e.stderr.decode()
                if isinstance(e.stderr, bytes)
                else (e.stderr if e.stderr else "")
            )
            raise ImageError(f"dd failed: {stderr}") from e
        except FileNotFoundError:
            raise ImageError("dd not found. Install coreutils.") from None

    def _copy_with_dd(
        self, src: Path, dst: Path, *, sparse: bool = False
    ) -> None:
        """Copy file from *src* to *dst* using dd."""
        conv = "sparse,fsync" if sparse else "fsync"
        try:
            subprocess.run(
                [
                    "dd",
                    f"if={src}",
                    f"of={dst}",
                    "bs=1M",
                    f"conv={conv}",
                    "status=none",
                ],
                check=True,
                capture_output=True,
            )
        except (subprocess.CalledProcessError, FileNotFoundError) as e:
            raise ImageError(f"dd copy failed: {e}") from e

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
        elif image_format == "vhdx":
            self._validate_vhdx(downloaded_path, file_size)
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

    def _calculate_minimum_image_size_mb(self, content_bytes: int) -> int:
        """Calculate minimum image size in MiB based on actual content bytes.

        Uses binary MiB (1,048,576 bytes) for calculation with headroom factor.
        """
        content_mib = content_bytes / CONST_MEBIBYTE_BYTES
        calculated_mib = int(content_mib * CONST_ROOTFS_HEADROOM_FACTOR)
        return max(CONST_MIN_ROOTFS_SIZE_MIB, calculated_mib)

    def _handle_qcow2(
        self,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle qcow2 format."""
        with tempfile.TemporaryDirectory(
            dir=CacheUtils.get_temp_dir()
        ) as tmpdir:
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
        input_path: Path,
        final_path: Path,
        minimum_rootfs_size: int | str,
    ) -> Path:
        """Handle tar-rootfs format."""
        self.create_ext4_from_tar(input_path, final_path, minimum_rootfs_size)
        return final_path

    def _handle_raw(
        self,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle raw format."""
        return self.extract_partition(
            input_path,
            final_path.with_suffix(".img"),
            partition,
            disabled_detectors,
        )

    def _handle_squashfs(
        self,
        input_path: Path,
        final_path: Path,
        minimum_rootfs_size: int | str,
    ) -> Path:
        """Handle squashfs format."""
        with tempfile.TemporaryDirectory(
            dir=CacheUtils.get_temp_dir()
        ) as tmpdir:
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
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle VHD format.

        Tries guestfs-based extraction first for reliability with non-standard
        VHD images (e.g., Alpine), falls back to sfdisk/fdisk parsing.
        """
        with tempfile.TemporaryDirectory(
            dir=CacheUtils.get_temp_dir()
        ) as tmpdir:
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

    def _handle_vhdx(
        self,
        input_path: Path,
        final_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Handle VHDX format.

        Tries guestfs-based extraction first for reliability with VHDX images,
        falls back to sfdisk/fdisk parsing.
        """
        with tempfile.TemporaryDirectory(
            dir=CacheUtils.get_temp_dir()
        ) as tmpdir:
            raw_path = Path(tmpdir) / "intermediate.raw"
            self.convert_vhdx_to_raw(input_path, raw_path)

            # Try guestfs-based extraction first (more reliable for VHDX)
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
                raise ImageError("Failed to extract partition from VHDX")
            return actual_path

    @staticmethod
    def _get_template_variables(
        spec: ImageSpec, ci_version: str
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

    @staticmethod
    def _resolve_source_template(
        spec: ImageSpec, template_vars: dict[str, str]
    ) -> str:
        """Resolve source URL by fetching and parsing CI image list."""
        if not spec.list_url_template:
            raise ImageError(
                f"Missing 'list_url_template' in images.yaml for {spec.id}"
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

        # Derive base URL from source (scheme + host + bucket/root path)
        from urllib.parse import urlparse

        source_resolved = render_template(spec.source, template_vars)
        parsed = urlparse(source_resolved)
        path_parts = parsed.path.strip("/").split("/")
        bucket = path_parts[0] if path_parts else ""
        base = f"{parsed.scheme}://{parsed.netloc}/{bucket}"
        return f"{base}/{chosen_key}"

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

    @staticmethod
    def _is_qcow2(path: Path) -> bool:
        """Check for QCOW2 magic number 'QFI\xfb' in first 4 bytes."""
        try:
            with open(path, "rb") as f:
                return f.read(4) == b"QFI\xfb"
        except (OSError, struct.error):
            return False

    @staticmethod
    def _is_vhd(path: Path, file_size: int) -> bool:
        """Check for VHD footer cookie 'conectix' in last 512 bytes."""
        if file_size < 512:
            return False
        try:
            with open(path, "rb") as f:
                f.seek(file_size - 512)
                return f.read(8) == b"conectix"
        except (OSError, struct.error):
            return False

    @staticmethod
    def _is_squashfs(path: Path) -> bool:
        """Check for SquashFS magic number 0x73717368 ('hsqs' on disk) in first 4 bytes."""
        try:
            with open(path, "rb") as f:
                magic: int = struct.unpack("<I", f.read(4))[0]
                return magic == 0x73717368
        except (OSError, struct.error):
            return False

    @staticmethod
    def _is_tar(path: Path) -> bool:
        """Check if Python's tarfile can read at least one member header."""
        try:
            with tarfile.open(path, "r:*") as tf:
                for _ in tf:
                    return True
                return False
        except (tarfile.TarError, OSError):
            return False

    @staticmethod
    def _is_raw(path: Path, file_size: int) -> bool:
        """Check if file looks like a raw disk image.

        Requires sector alignment and evidence of a partition table
        (MBR signature at offset 510, or GPT at offset 512) or
        non-zero content in the first 1 KiB.
        """
        if file_size < _SECTOR_SIZE or file_size % _SECTOR_SIZE != 0:
            return False
        try:
            with open(path, "rb") as f:
                first_kb = f.read(1024)
                if first_kb == b"\x00" * len(first_kb):
                    return False
                # MBR boot signature at offset 510
                if len(first_kb) > 512 and first_kb[510:512] == b"\x55\xaa":
                    return True
                # GPT signature at offset 512
                if len(first_kb) > 520 and first_kb[512:520] == b"EFI PART":
                    return True
                # Fallback: non-zero content (less reliable, but catches
                # raw filesystem images without partition tables)
                return True
        except OSError:
            return False

    @staticmethod
    def _is_vhdx(path: Path) -> bool:
        """Check for VHDX signature 'vhdxfile' in first 8 bytes."""
        try:
            with open(path, "rb") as f:
                return f.read(8) == b"vhdxfile"
        except (OSError, struct.error):
            return False

    def _validate_qcow2(self, path: Path) -> None:
        """Validate qcow2 by magic number, version, and size."""
        if not self._is_qcow2(path):
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid qcow2 file: wrong magic number")

        try:
            with open(path, "rb") as f:
                f.read(4)  # skip magic already checked
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

        if not self._is_vhd(path, file_size):
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                "Invalid VHD file: missing conectix cookie"
            )

        try:
            with open(path, "rb") as f:
                f.seek(file_size - 512)
                footer = f.read(512)
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

    def _validate_vhdx(self, path: Path, file_size: int) -> None:
        """Validate VHDX by signature and minimum size."""
        if file_size < 65536:
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid VHDX file: too small")

        if not self._is_vhdx(path):
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                "Invalid VHDX file: missing vhdxfile signature"
            )

    def _validate_raw(self, path: Path, file_size: int) -> None:
        """Validate raw image by size and non-zero content."""
        if file_size < _SECTOR_SIZE:
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid raw image: too small")

        if file_size % _SECTOR_SIZE != 0:
            logger.warning("Raw image size %d is not sector-aligned", file_size)

        if not self._is_raw(path, file_size):
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                "Invalid raw image: file appears to be all zeros"
            )

    def _validate_squashfs(self, path: Path) -> None:
        """Validate squashfs by magic number and version."""
        if not self._is_squashfs(path):
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                "Invalid squashfs file: wrong magic number"
            )

        try:
            with open(path, "rb") as f:
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
        if not self._is_tar(path):
            path.unlink(missing_ok=True)
            raise ImageValidationError("Invalid tar file")

        try:
            with tarfile.open(path, "r:*") as tf:
                for _ in tf:
                    pass
        except tarfile.TarError as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(f"Invalid tar file: {e}") from e
        except OSError as e:
            path.unlink(missing_ok=True)
            raise ImageValidationError(
                f"Failed to validate tar file: {e}"
            ) from e


__all__ = ["ImageService"]
