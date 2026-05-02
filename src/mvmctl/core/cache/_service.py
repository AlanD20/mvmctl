"""Stateless cache cleanup operations — guestfs, appliance, warm images."""

from __future__ import annotations

import logging
import shutil

from mvmctl.core._shared._guestfs import GuestfsService
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class CacheService:
    """Stateless cache cleanup — domain-agnostic infrastructure operations."""

    @staticmethod
    def clean_stale_guestfs_state() -> bool:
        """Remove stale libguestfs processes, locks, sockets, and caches.

        Delegates to GuestfsService.

        Returns:
            True if any stale state was removed or process was killed.
        """
        return GuestfsService.clean_stale_guestfs_state()

    @staticmethod
    def prune_appliance(dry_run: bool = False) -> bool:
        """Remove the libguestfs appliance folder and stale system state.

        Delegates to GuestfsService.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if appliance folder or stale state was removed.
        """
        return GuestfsService.prune_appliance(dry_run)

    @staticmethod
    def prune_warm_images(dry_run: bool = False) -> bool:
        """Remove warm images from the tmpfs ready pool.

        Warm images are decompressed VM images cached in RAM for fast cloning.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if warm images were removed or would be removed.
        """
        warm_dir = CacheUtils.get_warm_image_dir()
        if not warm_dir.exists():
            return False

        has_content = any(warm_dir.iterdir())
        if not has_content:
            return False

        if not dry_run:
            for item in warm_dir.iterdir():
                try:
                    if item.is_dir():
                        shutil.rmtree(item)
                    else:
                        item.unlink()
                except OSError:
                    pass
        return True


__all__ = ["CacheService"]
