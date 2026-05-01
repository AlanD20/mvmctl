"""Stateless cache cleanup operations — guestfs, appliance, warm images."""

from __future__ import annotations

import logging
import os
import shutil
import signal
import time
from pathlib import Path

from mvmctl.utils._system import has_python_ancestor
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class CacheService:
    """Stateless cache cleanup — domain-agnostic infrastructure operations."""

    @staticmethod
    def clean_stale_guestfs_state() -> bool:
        """Remove stale libguestfs processes, locks, sockets, and caches.

        First, kills any abandoned QEMU/guestfish processes that are running
        the libguestfs appliance but have no active Python/mvmctl ancestor.
        Then cleans up lock files, daemon sockets, and cached appliance
        directories that can cause subsequent appliance operations to hang.

        Returns:
            True if any stale state was removed or process was killed.
        """
        uid = os.getuid()
        cleaned = False

        # ── Phase 0: Find and kill abandoned guestfs processes ──────────
        abandoned_pids = CacheService._find_abandoned_guestfs_processes(uid)
        for pid in abandoned_pids:
            try:
                os.kill(pid, signal.SIGTERM)
                cleaned = True
                logger.debug(
                    "Sent SIGTERM to abandoned guestfs process %d", pid
                )
            except (ProcessLookupError, PermissionError):
                continue

        # Wait briefly for processes to exit, then force-kill survivors
        if abandoned_pids:
            time.sleep(0.5)
            for pid in abandoned_pids:
                try:
                    os.kill(pid, 0)  # still alive?
                except (ProcessLookupError, OSError):
                    continue
                try:
                    os.kill(pid, signal.SIGKILL)
                    logger.debug(
                        "Sent SIGKILL to abandoned guestfs process %d", pid
                    )
                except (ProcessLookupError, PermissionError):
                    pass

        # ── Phase 1: Remove the global lock file ────────────────────────
        lock_file = Path(f"/var/tmp/.guestfs-{uid}/lock")
        if lock_file.exists():
            try:
                lock_file.unlink()
                cleaned = True
                logger.debug("Removed stale libguestfs lock: %s", lock_file)
            except OSError:
                pass

        # ── Phase 2: Remove stale daemon sockets ────────────────────────
        for sock_dir in Path(f"/run/user/{uid}").glob("libguestfs*"):
            for sock in sock_dir.glob("guestfsd.sock"):
                try:
                    sock.unlink()
                    cleaned = True
                    logger.debug("Removed stale libguestfs socket: %s", sock)
                except OSError:
                    pass

        # ── Phase 3: Remove cached appliance directories in /var/tmp ────
        guestfs_tmp = Path(f"/var/tmp/.guestfs-{uid}")
        if guestfs_tmp.exists():
            for entry in guestfs_tmp.glob("appliance.d*"):
                if entry.is_dir():
                    try:
                        shutil.rmtree(entry)
                        cleaned = True
                        logger.debug(
                            "Removed stale libguestfs cache: %s", entry
                        )
                    except OSError:
                        pass

        return cleaned

    @staticmethod
    def _find_abandoned_guestfs_processes(uid: int) -> list[int]:
        """Find QEMU/guestfish PIDs owned by *uid* that are running the
        libguestfs appliance but have no Python/mvmctl ancestor.

        These are orphaned processes left behind after crashes or failed
        appliance builds.
        """
        abandoned: list[int] = []

        proc_path = Path("/proc")
        if not proc_path.exists():
            return abandoned

        try:
            for proc_dir in proc_path.iterdir():
                if not proc_dir.name.isdigit():
                    continue
                pid = int(proc_dir.name)
                try:
                    proc_uid = proc_dir.stat().st_uid
                except OSError:
                    continue
                if proc_uid != uid:
                    continue

                # Read cmdline and check for guestfs signature
                try:
                    cmdline_raw = (proc_dir / "cmdline").read_bytes()
                    cmdline = cmdline_raw.decode("utf-8", errors="replace")
                except OSError:
                    continue

                # Match QEMU processes running the libguestfs appliance,
                # or guestfish processes left behind.
                if (
                    ".guestfs-" in cmdline
                    and ("appliance.d" in cmdline or "guestfsd.sock" in cmdline)
                ) or "guestfish" in cmdline.lower():
                    if not has_python_ancestor(pid):
                        abandoned.append(pid)
        except FileNotFoundError:
            pass

        return abandoned

    @staticmethod
    def prune_appliance(dry_run: bool = False) -> bool:
        """Remove the libguestfs appliance folder and stale system state.

        Also cleans up stale locks and sockets in /var/tmp and /run/user
        that can cause subsequent appliance builds to hang.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if appliance folder or stale state was removed.
        """
        appliance_dir = CacheUtils.get_cache_dir() / "appliance"
        removed = False
        if appliance_dir.exists():
            if not dry_run:
                shutil.rmtree(appliance_dir, ignore_errors=True)
            removed = True

        if not dry_run:
            state_cleaned = CacheService.clean_stale_guestfs_state()
            removed = removed or state_cleaned

        return removed

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
