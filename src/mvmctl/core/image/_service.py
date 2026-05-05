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
    HTTP_TIMEOUT_SHA256_FETCH_S,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core._shared import AssetManager
from mvmctl.core.image._repository import ImageRepository
from mvmctl.exceptions import (
    ImageCompressionError,
    ImageCorruptError,
    ImageDecompressionError,
    ImageEmptyError,
    ImageError,
    ImageValidationError,
)
from mvmctl.models import ImageItem, ImageSpec
from mvmctl.models.provisioner import ProvisionerType
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_optional_template, render_template

logger = logging.getLogger(__name__)

_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES


class ImageService:
    """
    Handles image processing: compression, decompression, shrinking, format conversion, and pool management.

    Args:
        repo: ImageRepository for DB operations. Must be provided.

    """

    def __init__(self, repo: ImageRepository) -> None:
        """
        Initialize ImageService.

        Args:
            repo: ImageRepository for DB operations.

        """
        self._repo = repo

    def remove_many(self, images: list[ImageItem], force: bool = False) -> None:
        """
        Remove multiple images, enriching with VM references first.

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
        """
        Remove files for multiple images from disk. No DB changes.

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
        """
        List all images, syncing is_present flag with filesystem.

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
        """
        Resolve the actual filesystem path for an image.

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

    def optimize_image(
        self,
        image_path: Path,
        image_id: str,
        spec: ImageSpec,
        timestamp: str,
        skip_optimization: bool = False,
        provisioner_type: ProvisionerType = ProvisionerType.LOOP_MOUNT,
    ) -> ImageItem:
        """Shrink and compress image. Returns fully constructed ImageItem.

        Args:
            image_path: Path to the extracted root filesystem image.
            image_id: Unique image identifier.
            spec: Image specification from the catalog.
            timestamp: ISO timestamp for the operation.
            skip_optimization: If True, skip shrink and compression steps.
            provisioner_type: Which backend to use for shrink/deblob.
                Defaults to LOOP_MOUNT. Use GUESTFS for the libguestfs path.

        """
        import time

        t0 = time.monotonic()
        fs_type = self._resolve_fs_type(image_path)
        fs_uuid = self.get_filesystem_uuid(image_path)
        t1 = time.monotonic()
        logger.debug("  fs detect: %.2fs", t1 - t0)

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

        # ── Shrink + deblob via ImageProvisioner ──────────────────────
        from mvmctl.core.image._provisioner import ImageProvisioner

        pre_shrink_size = image_path.stat().st_size

        p = ImageProvisioner(
            image_path=image_path,
            provisioner_type=provisioner_type,
            fs_type=fs_type,
        )
        p.deblob()
        p.shrink()
        p.run()

        # After shrink the file may be smaller
        post_shrink_size = image_path.stat().st_size
        shrunk_path = image_path

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
        progress_callback: Callable[[int, int | None], None] | None = None,
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
            progress_callback=progress_callback,
            allow_missing_checksum=resolved_sha256 is None,
            silent_missing_checksum=resolved_sha256 is None,
        )
        self._validate_downloaded_file(download_path, spec.format)

        return download_path

    def extract_image(
        self,
        source_path: Path,
        image_id: str,
        output_dir: Path,
        format: str,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
        provisioner_type: ProvisionerType = ProvisionerType.LOOP_MOUNT,
    ) -> Path:
        """Extract/convert a source image to a root filesystem.

        Handles all formats: qcow2, vhd, vhdx, raw, tar-rootfs, squashfs.
        """
        final_path = output_dir / f"{image_id}.img"

        if format in ("qcow2", "vhd", "vhdx", "raw"):
            return self._extract_disk_image(
                source_path,
                final_path,
                format,
                partition=partition,
                disabled_detectors=disabled_detectors,
                provisioner_type=provisioner_type,
            )
        elif format == "tar-rootfs":
            self.create_ext4_from_tar(source_path, final_path, "dynamic")
            return final_path
        elif format == "squashfs":
            return self._handle_squashfs(source_path, final_path, "dynamic")
        else:
            raise ImageError(f"Unknown format: {format}")

    def materialize_to(
        self, image_id: str, fs_type: str, output_path: Path
    ) -> None:
        """
        Fast durable copy from tmpfs cache to destination.

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
        cls, os_slugs: list[str], version: str | None, arch: str
    ) -> list[ImageSpec]:
        """
        Resolve ImageSpecs from the bundled images.yaml by os_slugs.

        Args:
            os_slugs: List of image IDs from images.yaml (e.g., ['ubuntu-24.04']).

        Returns:
            List of matching ImageSpecs.

        Raises:
            ImageError: If any image is not found in the catalog.

        """
        all_specs = cls.load_available_images(arch)

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
        """
        Compress the image using zstd.

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
        """
        Decompress the image to the specified output path.

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
        """
        Ensure images are decompressed to tmpfs cache, creating if needed.

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

    @staticmethod
    def _convert_to_raw(input_path: Path, output_path: Path, fmt: str) -> None:
        """Convert a disk image to raw format using qemu-img."""
        try:
            logger.info("Converting %s to raw...", input_path.name)
            subprocess.run(
                [
                    "qemu-img",
                    "convert",
                    "-m",
                    "16",
                    "-f",
                    fmt,
                    "-O",
                    "raw",
                    "-t",
                    "none",
                    "-T",
                    "none",
                    "-W",
                    str(input_path),
                    str(output_path),
                ],
                capture_output=True,
                text=True,
                check=True,
            )
            logger.info("Converted to %s", output_path.name)
        except subprocess.CalledProcessError as e:
            detail = e.stderr.strip() if e.stderr else "no details"
            raise ImageError(f"qemu-img conversion failed: {detail}") from e
        except FileNotFoundError:
            raise ImageError("qemu-img not found. Install qemu-utils.")

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
        """
        Detect container format from magic bytes. Returns None if unknown.

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
        cls, specs: list[ImageSpec], ci_version: str
    ) -> list[ImageSpec]:
        """
        Resolve remote image sizes via HEAD requests with HTTP caching.

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
    def load_available_images(arch: str) -> list[ImageSpec]:
        """Load image specifications from YAML config file."""
        import yaml

        asset = AssetManager()
        remote_images_path = asset.get_file("images.yaml")
        with open(str(remote_images_path)) as f:
            data = yaml.safe_load(f)

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
        """
        Validate downloaded file is valid for its format.

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

    def _validate_image_path(self, image_path: Path) -> Path:
        """
        Validate that an image path exists.

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
        """
        Calculate minimum image size in MiB based on actual content bytes.

        Uses binary MiB (1,048,576 bytes) for calculation with headroom factor.
        """
        content_mib = content_bytes / CONST_MEBIBYTE_BYTES
        calculated_mib = int(content_mib * CONST_ROOTFS_HEADROOM_FACTOR)
        return max(CONST_MIN_ROOTFS_SIZE_MIB, calculated_mib)

    @staticmethod
    def _extract_via_backend(
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
        provisioner_type: ProvisionerType = ProvisionerType.LOOP_MOUNT,
    ) -> Path:
        """Extract root partition from a raw disk image via the selected backend."""
        from mvmctl.core._shared._provisioner._backend import (
            ProvisionerBackend,
        )

        backend = ProvisionerBackend.get_image(
            raw_path,
            provisioner_type=provisioner_type,
        )
        try:
            return backend.extract_partition(
                raw_path,
                output_path,
                partition=partition,
                disabled_detectors=disabled_detectors,
            )
        except RuntimeError as e:
            raise ImageError(str(e)) from e

    def _extract_disk_image(
        self,
        input_path: Path,
        output_path: Path,
        format: str,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
        provisioner_type: ProvisionerType = ProvisionerType.LOOP_MOUNT,
    ) -> Path:
        """Extract root partition from a disk image (qcow2, vhd, vhdx, raw).

        For qcow2/vhd/vhdx: converts to raw via qemu-img first, then extracts.
        For raw: extracts directly.

        Tries the selected backend first (e.g. guestfs), falls back to the
        loop-mount backend (sfdisk/fdisk + dd) if that fails.
        """
        if format in ("qcow2", "vhd", "vhdx"):
            fmt_flag = {"qcow2": "qcow2", "vhd": "vpc", "vhdx": "vhdx"}[format]
            with tempfile.TemporaryDirectory(
                dir=CacheUtils.get_temp_dir()
            ) as tmpdir:
                raw_path = Path(tmpdir) / "intermediate.raw"
                self._convert_to_raw(input_path, raw_path, fmt_flag)
                try:
                    return self._extract_via_backend(
                        raw_path,
                        output_path.with_suffix(".img"),
                        partition=partition,
                        disabled_detectors=disabled_detectors,
                        provisioner_type=provisioner_type,
                    )
                except (ImageError, RuntimeError):
                    pass  # fall back to loop-mount below
                return self._extract_via_backend(
                    raw_path,
                    output_path.with_suffix(".img"),
                    partition=partition,
                    disabled_detectors=disabled_detectors,
                    provisioner_type=ProvisionerType.LOOP_MOUNT,
                )
        elif format == "raw":
            try:
                return self._extract_via_backend(
                    input_path,
                    output_path.with_suffix(".img"),
                    partition=partition,
                    disabled_detectors=disabled_detectors,
                    provisioner_type=provisioner_type,
                )
            except (ImageError, RuntimeError):
                pass  # fall back to loop-mount below
            return self._extract_via_backend(
                input_path,
                output_path.with_suffix(".img"),
                partition=partition,
                disabled_detectors=disabled_detectors,
                provisioner_type=ProvisionerType.LOOP_MOUNT,
            )
        else:
            raise ImageError(f"Unsupported disk image format: {format}")

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
        """
        Check if file looks like a raw disk image.

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
