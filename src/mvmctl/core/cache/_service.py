"""Stateless cache cleanup operations — guestfs, appliance, warm images."""

from __future__ import annotations

import logging
import shutil
from pathlib import Path

from mvmctl.core._shared._guestfs import GuestfsService
from mvmctl.core._shared._loopmount._manager import LoopMountManager
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

    @staticmethod
    def clean_stale_provision_mounts(dry_run: bool = False) -> bool:
        """Clean stale mvm-provision mount directories in /tmp/.

        Scans ``/tmp/`` for directories matching ``mvm-provision-*``,
        unmounts any that are still mounted, and removes the mount point.
        These mounts are left behind when the loop-mount provision process
        crashes or is killed before its cleanup handler runs.

        Args:
            dry_run: If True, only report what would be cleaned.

        Returns:
            True if any stale provision mount directories were found
            (and cleaned, unless dry_run is True).
        """
        tmp = Path("/tmp")
        cleaned = False

        for path in tmp.glob("mvm-provision-*"):
            if not path.is_dir():
                continue

            if not dry_run:
                try:
                    if path.is_mount():
                        logger.info(
                            "Unmounting stale provision mount: %s", path
                        )
                        LoopMountManager.cleanup_mount(str(path))

                    logger.info(
                        "Removing stale provision mount point: %s", path
                    )
                    path.rmdir()
                except OSError:
                    logger.warning(
                        "Failed to clean stale provision mount: %s", path
                    )

            cleaned = True

        return cleaned


__all__ = ["CacheService"]
