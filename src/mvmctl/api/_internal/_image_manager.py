"""Image management.

This module handles image operations including tmpfs caching,
compression, and decompression for fast VM cloning.
"""

from __future__ import annotations

import logging
import shutil
import subprocess
import tempfile
from pathlib import Path
from typing import TYPE_CHECKING

import zstandard as zstd

from mvmctl.api._internal._resolvers import ImageResolver
from mvmctl.constants import CLI_NAME
from mvmctl.core import image as image_core
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import ImageError

if TYPE_CHECKING:
    from mvmctl.db.models import Image


logger = logging.getLogger(__name__)


class ImageManager:
    """Manages image operations for a specific image.

    This class handles compression, decompression, and tmpfs caching
    for fast VM rootfs cloning.

    Args:
        entity: Image os_slug, ID prefix, or Image db model instance.
        db: Optional MVMDatabase instance (creates new if None).

    Raises:
        ImageNotFoundError: If the image cannot be resolved.
    """

    def __init__(self, entity: str | Image, db: MVMDatabase | None = None) -> None:
        self._db = db if db is not None else MVMDatabase()

        if isinstance(entity, Image):
            self._image = entity
        else:
            self._resolver = ImageResolver(self._db)
            self._image = self._resolver.resolve(entity)

    @property
    def image_id(self) -> str:
        """Get the resolved image ID."""
        return self._image.id

    @property
    def image_os_slug(self) -> str:
        """Get the resolved image OS slug."""
        return self._image.os_slug

    @property
    def image_path(self) -> Path:
        """Get the image storage path."""
        return Path(self._image.path)

    @staticmethod
    def _get_cache_dir() -> Path:
        """Get the tmpfs cache directory for fast clones."""
        cache_dir = Path(tempfile.gettempdir()) / CLI_NAME / "ready"
        cache_dir.mkdir(parents=True, exist_ok=True)
        return cache_dir

    def _get_cached_image_path(self) -> Path:
        """Get path to cached image in tmpfs."""
        return self._get_cache_dir() / f"{self.image_id}.{self._image.fs_type}"

    def _decompress_zstd(self, compressed_path: Path, output_path: Path) -> None:
        """Decompress a zstd compressed image."""
        try:
            decompressor = zstd.ZstdDecompressor()
            with open(compressed_path, "rb") as src, open(output_path, "wb") as dst:
                decompressor.copy_stream(src, dst)
            logger.info("Decompressed %s → %s", compressed_path.name, output_path.name)
        except OSError as e:
            raise ImageError(f"Failed to decompress image: {e}") from e

    def compress(self, level: int = 6) -> Path:
        """Compress the image using zstd.

        Args:
            level: Compression level (1-22, default 6 for speed/size balance)

        Returns:
            Path to the compressed file (with .zst suffix)

        Raises:
            ImageError: If compression fails
        """
        return image_core.compress_image(self.image_path, level=level)

    def decompress(self, output_path: Path) -> None:
        """Decompress the image to the specified output path.

        Args:
            output_path: Path where the decompressed file should be written

        Raises:
            ImageError: If decompression fails
        """
        compressed_path = self.image_path
        if not compressed_path.suffix == ".zst":
            compressed_path = compressed_path.with_suffix(compressed_path.suffix + ".zst")

        self._decompress_zstd(compressed_path, output_path)

    def ensure_cached(self) -> Path:
        """Ensure image is decompressed to tmpfs cache, creating if needed.

        This maintains a tmpfs-based cache of decompressed images
        for fast cloning. First call decompresses to RAM, subsequent calls
        return the cached path immediately.

        Returns:
            Path to the cached image (in tmpfs/RAM)

        Raises:
            ImageError: If decompression fails
        """
        cached_path = self._get_cached_image_path()

        if cached_path.exists():
            logger.debug("Found image in cache: %s", cached_path)
            return cached_path

        compressed_path = self.image_path
        if not compressed_path.suffix == ".zst":
            compressed_path = compressed_path.with_suffix(compressed_path.suffix + ".zst")

        logger.info("Decompressing to cache: %s", cached_path.name)
        self._decompress_zstd(compressed_path, cached_path)

        return cached_path

    def copy_cached_to(self, output_path: Path) -> None:
        """Fast copy from tmpfs cache to destination.

        Uses reflink (copy-on-write) if available (btrfs/xfs), otherwise
        falls back to regular copy. Since the cache is in tmpfs (RAM),
        the copy is fast regardless of the underlying filesystem.

        Args:
            output_path: Destination path for the VM rootfs

        Raises:
            ImageError: If the image is not found in the cache
        """
        cached_path = self._get_cached_image_path()

        if not cached_path.exists():
            raise ImageError(f"Image not in cache: {self.image_id}")

        try:
            subprocess.run(
                ["cp", "--reflink=auto", str(cached_path), str(output_path)],
                check=True,
                capture_output=True,
            )
            logger.info("Fast-copied from cache: %s", output_path.name)
        except subprocess.CalledProcessError:
            shutil.copy2(cached_path, output_path)
            logger.info("Copied from cache: %s", output_path.name)

    @staticmethod
    def prune_cached() -> int:
        """Remove all images from the tmpfs cache.

        Returns:
            Number of files removed
        """
        cache_dir = ImageManager._get_cache_dir()
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
    "ImageManager",
]
