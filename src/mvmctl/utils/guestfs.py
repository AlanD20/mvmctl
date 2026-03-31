from __future__ import annotations

import os
from contextlib import contextmanager
from pathlib import Path
from typing import Any

from mvmctl.exceptions import MVMError


class OptimizedGuestfs:
    def __init__(self, disk_path: Path, readonly: bool = False) -> None:
        self.disk_path = disk_path
        self.readonly = readonly
        self._g: Any = None
        self._orig_env: dict[str, str | None] = {}

    def _setup_environment(self) -> None:
        self._orig_env = {
            "LIBGUESTFS_BACKEND": os.environ.get("LIBGUESTFS_BACKEND"),
            "LIBGUESTFS_CACHEDIR": os.environ.get("LIBGUESTFS_CACHEDIR"),
            "QEMU_LOCKING": os.environ.get("QEMU_LOCKING"),
        }
        os.environ["LIBGUESTFS_BACKEND"] = "direct"
        if Path("/dev/shm").exists():
            os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"
        # Disable QEMU file locking — prevents stale lock issues from crashed
        # guestfs sessions on shared images (ready pool, etc.)
        os.environ["QEMU_LOCKING"] = "off"
        # Disable QEMU file locking — prevents stale lock issues from crashed
        # guestfs sessions on shared images (ready pool, etc.)
        os.environ["QEMU_LOCKING"] = "off"

    def _restore_environment(self) -> None:
        for key, value in self._orig_env.items():
            if value is not None:
                os.environ[key] = value
            elif key in os.environ:
                del os.environ[key]

    def _create_handle(self) -> Any:
        import importlib

        guestfs = importlib.import_module("guestfs")
        g = guestfs.GuestFS(python_return_dict=True)

        if hasattr(g, "set_recovery_proc"):
            g.set_recovery_proc(False)
        if hasattr(g, "set_autosync"):
            g.set_autosync(False)
        if hasattr(g, "set_network"):
            g.set_network(False)
        if hasattr(g, "set_smp"):
            g.set_smp(1)
        if hasattr(g, "set_memsize"):
            g.set_memsize(256)

        g.add_drive_opts(
            str(self.disk_path),
            format="raw",
            readonly=self.readonly,
            cachemode="unsafe",
        )

        return g

    def __enter__(self) -> Any:
        self._setup_environment()
        try:
            self._g = self._create_handle()
            self._g.launch()
            return self._g
        except Exception as e:
            self._restore_environment()
            raise MVMError(f"Failed to launch guestfs: {e}") from e

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        try:
            if self._g is not None:
                try:
                    self._g.shutdown()
                except Exception:
                    pass
        finally:
            self._restore_environment()


@contextmanager
def optimized_guestfs(disk_path: Path, readonly: bool = False) -> Any:
    with OptimizedGuestfs(disk_path, readonly) as g:
        yield g


def check_libguestfs() -> bool:
    try:
        import importlib

        guestfs = importlib.import_module("guestfs")
        return hasattr(guestfs, "GuestFS")
    except ImportError:
        return False


def extract_partition_with_guestfs(
    raw_path: Path,
    output_path: Path,
    partition: int | None = None,
) -> Path | None:
    """Extract root partition using libguestfs for reliable VHD handling.

    Uses guestfs to reliably extract partitions from VHD-converted images
    that may have non-standard partition tables.

    Args:
        raw_path: Path to the raw disk image
        output_path: Path to write the extracted filesystem image
        partition: Partition number (1-indexed), or None for auto-detect

    Returns:
        Path to the extracted filesystem image, or None if guestfs unavailable/fails
    """
    import logging

    from mvmctl.constants import CONST_SHRINK_SAFETY_MARGIN

    logger = logging.getLogger(__name__)

    if not check_libguestfs():
        return None

    try:
        with optimized_guestfs(raw_path, readonly=True) as g:
            partitions = g.list_partitions()
            if not partitions:
                logger.debug("No partitions found in image")
                return None

            if partition is not None:
                if partition < 1 or partition > len(partitions):
                    logger.debug("Partition %d out of range (1-%d)", partition, len(partitions))
                    return None
                root_device = partitions[partition - 1]
            else:
                root_device = _find_largest_linux_fs(g, partitions)
                if root_device is None:
                    root_device = partitions[0]

            fs_size = _get_fs_size(g, root_device)
            g.copy_device_to_file(root_device, str(output_path))

            if fs_size > 0:
                final_size = int(fs_size * CONST_SHRINK_SAFETY_MARGIN)
                with open(output_path, "r+b") as f:
                    f.truncate(final_size)

            logger.info("Extracted root partition via guestfs: %s", output_path.name)
            return output_path

    except Exception as e:
        logger.debug("Guestfs extraction failed: %s", e)
        return None


def _find_largest_linux_fs(g: Any, partitions: list[str]) -> str | None:
    max_size = 0
    root_device = None
    for dev in partitions:
        try:
            fs_type = g.vfs_type(dev)
            if fs_type in ("ext2", "ext3", "ext4", "btrfs", "xfs"):
                g.mount(dev, "/")
                try:
                    stat = g.statvfs("/")
                    size = stat.get("fs_blocks", 0) * stat.get("fs_bsize", 4096)
                    if size > max_size:
                        max_size = size
                        root_device = dev
                finally:
                    g.umount(dev)
        except Exception:
            continue
    return root_device


def _get_fs_size(g: Any, device: str) -> int:
    g.mount(device, "/")
    try:
        stat = g.statvfs("/")
        size = stat.get("fs_blocks", 0) * stat.get("fs_bsize", 4096)
        return int(size)
    finally:
        g.umount(device)
