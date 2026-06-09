"""
Image management.

This module handles image operations including tmpfs caching,
compression, and decompression for fast VM cloning.
"""

from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.models import ImageItem
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class ImageController:
    """
    Manages image operations for a specific image.

    This class handles compression, decompression, and tmpfs caching
    for fast VM rootfs cloning.

    Args:
        entity: Image type, ID prefix, or Image db model instance.
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
        """
        Get the compressed path for this image.

        Uses compressed_format from ImageItem if set, otherwise defaults to .zst.
        """
        fmt = self._image.compressed_format or "zst"
        suffix = f".{fmt}" if not fmt.startswith(".") else fmt
        return Path(self.image_path).with_suffix(suffix)

    @staticmethod
    def prune_cached() -> int:
        """
        Remove all images from the tmpfs cache.

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
