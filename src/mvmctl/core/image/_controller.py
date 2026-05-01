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
        """
        Get the compressed path for this image.

        Uses compressed_format from ImageItem if set, otherwise defaults to .zst.
        """
        fmt = self._image.compressed_format or "zst"
        suffix = f".{fmt}" if not fmt.startswith(".") else fmt
        return Path(self.image_path).with_suffix(suffix)

    def remove_path(self) -> list[str]:
        """
        Remove all files for this controller's image from disk. No DB changes.

        Removes files matching self._image.id from:
        - images directory (.zst, .img, .download, etc.)
        - warm cache directory (.ext4, .btrfs, etc.)

        Returns:
            List of removed filenames for logging.

        """
        images_dir = CacheUtils.get_images_dir()
        warm_dir = CacheUtils.get_warm_image_dir()
        removed: list[str] = []

        for candidate in images_dir.glob(f"{self._image.id}*"):
            if candidate.is_file():
                candidate.unlink(missing_ok=True)
                removed.append(candidate.name)

        for candidate in warm_dir.glob(f"{self._image.id}*"):
            if candidate.is_file():
                candidate.unlink(missing_ok=True)
                removed.append(candidate.name)

        return removed

    def remove(self, force: bool = False) -> None:
        """
        Remove image files and delete the DB record.

        Hard-deletes when no VMs reference the image.
        Soft-deletes only when VMs still reference it (to preserve history).

        Args:
            force: If True, remove even if referenced by VMs.

        Raises:
            ImageError: If image is referenced by VMs and force is False.

        """
        from mvmctl.exceptions import ImageError
        from mvmctl.utils.auditlog import AuditLog

        has_vms = bool(self._image.vms)

        # 1. VM reference check
        if has_vms and not force:
            names = ", ".join(vm.name for vm in self._image.vms)
            raise ImageError(f"Image is referenced by VMs: {names}")

        # 2. Delete ALL related files from disk
        removed = self.remove_path()
        if removed:
            logger.info("Removed image files: %s", ", ".join(removed))

        # 3. Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(self._image.id)
        else:
            self._repo.delete(self._image.id)

        # 4. Audit log
        AuditLog.log("image.remove", changes={"id": self._image.id})

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
