"""Image management.

This module handles image operations including tmpfs caching,
compression, and decompression for fast VM cloning.
"""

from __future__ import annotations

import logging
import subprocess
from pathlib import Path

from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import ImageError
from mvmctl.models.image import ImageItem
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class ImageController:
    """Manages image operations for a specific image.

    This class handles compression, decompression, and tmpfs caching
    for fast VM rootfs cloning.

    Args:
        entity: Image os_slug, ID prefix, or Image db model instance.
        repo: ImageRepository for DB operations.

    Raises:
        ImageNotFoundError: If the image cannot be resolved.
    """

    def __init__(self, entity: str | ImageItem, repo: ImageRepository) -> None:
        self._repo = repo

        if isinstance(entity, ImageItem):
            self._image = entity
        else:
            self._resolver = ImageResolver(self._repo)
            self._image = self._resolver.resolve(entity)

    def get(self) -> ImageItem:
        """Return the resolved ImageItem."""
        return self._image

    @property
    def image_path(self) -> Path:
        """Get the image storage path."""
        return Path(self._image.path)

    @property
    def compressed_path(self) -> Path:
        """Get the compressed path for this image.

        Uses compressed_format from ImageItem if set, otherwise defaults to .zst.
        """
        fmt = self._image.compressed_format or "zst"
        suffix = f".{fmt}" if not fmt.startswith(".") else fmt
        return Path(self.image_path).with_suffix(suffix)

    def materialize_to(self, output_path: Path) -> None:
        """Fast durable copy from tmpfs cache to destination.

        Uses reflink (CoW) + sparse detection for maximum speed on btrfs/xfs.
        Falls back to a sparse-aware manual copy on non-CoW filesystems.
        After copy, fdatasync() ensures data durability without flushing
        non-critical metadata (timestamps).

        Args:
            output_path: Destination path for the VM rootfs

        Raises:
            ImageError: If the image is not in the cache
        """
        import os

        cached_path = (
            CacheUtils.get_warm_image_dir()
            / f"{self._image.id}.{self._image.fs_type}"
        )

        if not cached_path.exists():
            raise ImageError(f"Image not in cache: {self._image.id}")

        # Ensure output directory exists
        output_path.parent.mkdir(parents=True, exist_ok=True)

        try:
            # --reflink=auto: CoW clone if filesystem supports it (btrfs/xfs)
            # --sparse=always: detect zero regions and skip writing them
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
        except subprocess.CalledProcessError:
            # cp failed entirely (binary missing, disk full, etc.)
            # Fall back to sparse-aware Python copy
            self._copy_sparse_fallback(cached_path, output_path)

        # Ensure durability: flush file data + critical metadata to disk.
        # fdatasync() skips non-critical metadata (mtime/atime) which we don't need.
        with open(output_path, "rb") as f:
            os.fdatasync(f.fileno())

        logger.info("Copied image to: %s", output_path.name)

    @staticmethod
    def _copy_sparse_fallback(src: Path, dst: Path) -> None:
        """Sparse-aware fallback copy using lseek(SEEK_HOLE/SEEK_DATA).

        Detects hole regions in the source file and creates holes in dst
        instead of writing zeros. Much faster than shutil.copy2 for sparse
        images (common for freshly decompressed ext4 rootfs images).

        Args:
            src: Source file path (in tmpfs cache)
            dst: Destination file path (on physical disk)
        """
        import os

        buf_size = 4 * 1024 * 1024  # 4 MiB chunks

        with open(src, "rb") as fsrc, open(dst, "wb") as fdst:
            while True:
                # Find next data region (non-hole)
                pos = fsrc.tell()
                data_start = os.lseek(fsrc.fileno(), pos, os.SEEK_HOLE)
                if data_start == pos:
                    # At a hole, find the next data region
                    data_start = os.lseek(fsrc.fileno(), pos, os.SEEK_DATA)
                    if data_start == pos:
                        # No more data after this hole - we're done
                        break

                # Seek back to where we were and read the data chunk
                fsrc.seek(pos)
                remaining = data_start - pos
                while remaining > 0:
                    chunk_size = min(buf_size, remaining)
                    chunk = fsrc.read(chunk_size)
                    if not chunk:
                        break
                    fdst.write(chunk)
                    remaining -= len(chunk)

                if data_start == os.lseek(fsrc.fileno(), 0, os.SEEK_END):
                    break

            # Ensure correct file size (in case last region was a hole)
            fdst.truncate(src.stat().st_size)

    @staticmethod
    def prune_cached() -> int:
        """Remove all images from the tmpfs cache.

        Returns:
            Number of files removed
        """
        cache_dir = CacheUtils.get_warm_image_dir()
        removed_count = 0

        if cache_dir.exists():
            for item in cache_dir.iterdir():
                try:
                    item.unlink()
                    removed_count += 1
                    logger.debug("Removed from cache: %s", item.name)
                except OSError as e:
                    logger.warning("Failed to remove %s: %s", item.name, e)

        logger.info("Pruned cache: removed %d file(s)", removed_count)
        return removed_count


__all__ = [
    "ImageController",
]
