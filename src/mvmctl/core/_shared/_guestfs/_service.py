from __future__ import annotations

import logging
import os
import shutil
import subprocess
from pathlib import Path

from mvmctl.utils._system import ProcessSignalHandler, has_python_ancestor
from mvmctl.utils.common import CacheUtils

from ._kernel_detector import KernelDetector

logger = logging.getLogger(__name__)


class GuestfsService:
    """Stateless service for libguestfs appliance and backend operations."""

    @staticmethod
    def build_appliance(cache_dir: Path) -> Path | None:
        """Build the libguestfs fixed appliance for faster image operations.

        Uses KernelDetector to find a suitable upstream kernel with virtio
        drivers, sets the appropriate environment variables, and runs
        libguestfs-make-fixed-appliance.

        Args:
            cache_dir: Base cache directory where the appliance will be built.

        Returns:
            Path to appliance directory if built, None if skipped or failed.
        """
        make_tool = shutil.which("libguestfs-make-fixed-appliance")
        if not make_tool:
            logger.debug(
                "libguestfs-make-fixed-appliance not found — skipping appliance build"
            )
            return None

        appliance_dir = cache_dir / "appliance"
        appliance_dir.mkdir(parents=True, exist_ok=True)

        required_files = {"kernel", "initrd", "root"}
        if required_files.issubset({p.name for p in appliance_dir.iterdir()}):
            logger.debug(
                "libguestfs appliance already present at %s", appliance_dir
            )
            return appliance_dir

        GuestfsService.clean_stale_guestfs_state()

        env = os.environ.copy()
        kernel_info = KernelDetector.find_best_kernel()
        if kernel_info is not None:
            kernel_path, modules_dir = kernel_info
            env["SUPERMIN_KERNEL"] = str(kernel_path)
            env["SUPERMIN_MODULES"] = str(modules_dir)
            logger.debug(
                "Forcing libguestfs appliance build with kernel %s",
                kernel_path,
            )
        else:
            logger.warning(
                "No suitable kernel with virtio drivers found in /boot — "
                "appliance build may hang if the auto-selected kernel lacks virtio"
            )

        try:
            subprocess.run(
                [make_tool, str(appliance_dir)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
                check=True,
                timeout=60,
                env=env,
            )
        except subprocess.TimeoutExpired:
            logger.warning("libguestfs appliance build timed out after 60s")
            return None
        except subprocess.CalledProcessError as e:
            logger.warning("libguestfs appliance build failed: %s", e.stderr)
            return None
        except FileNotFoundError:
            return None
        else:
            logger.info("libguestfs fixed appliance built at %s", appliance_dir)
            return appliance_dir

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
        abandoned_pids = GuestfsService._find_abandoned_guestfs_processes(uid)
        if abandoned_pids:
            ProcessSignalHandler.terminate_batch(
                abandoned_pids,
                graceful_timeout=0.5,
            )
            cleaned = True

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
            state_cleaned = GuestfsService.clean_stale_guestfs_state()
            removed = removed or state_cleaned

        return removed
