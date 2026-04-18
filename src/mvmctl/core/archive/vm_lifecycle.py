from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.constants import CONST_MEBIBYTE_BYTES
from mvmctl.exceptions import MVMError, VMCreateError
from mvmctl.utils.fs import secure_mkdir
from mvmctl.utils.guestfs import check_libguestfs, optimized_guestfs

logger = logging.getLogger(__name__)


def grow_rootfs_with_guestfs(image_path: Path, target_size_bytes: int) -> None:
    if not check_libguestfs():
        raise VMCreateError("libguestfs required for disk resize")

    try:
        current_size = image_path.stat().st_size
    except (OSError, AttributeError):
        return

    if current_size >= target_size_bytes:
        raise VMCreateError(
            f"Requested disk size ({target_size_bytes // CONST_MEBIBYTE_BYTES} MB) is smaller than "
            f"current image size ({current_size // CONST_MEBIBYTE_BYTES} MB). "
            "Cannot shrink filesystem. Use a larger size or recreate VM with smaller image."
        )

    try:
        with open(image_path, "r+b") as file_handle:
            file_handle.truncate(target_size_bytes)

        with optimized_guestfs(image_path, readonly=False) as guestfs_handle:
            partitions = guestfs_handle._g.list_partitions()
            root_device = partitions[0] if partitions else "/dev/sda"
            fs_type = guestfs_handle._g.vfs_type(root_device)

            if fs_type in ("ext2", "ext3", "ext4"):
                guestfs_handle._g.resize2fs(root_device)
            elif fs_type == "btrfs":
                guestfs_handle._g.mount(root_device, "/")
                guestfs_handle._g.btrfs_filesystem_resize("/", target_size_bytes)
                guestfs_handle._g.umount(root_device)
            else:
                logger.warning("Cannot resize %s filesystem", fs_type)

        logger.info(
            "Grew rootfs: %d MB → %d MB",
            current_size // CONST_MEBIBYTE_BYTES,
            target_size_bytes // CONST_MEBIBYTE_BYTES,
        )
    except Exception as exc:
        raise VMCreateError(f"Failed to grow rootfs: {exc}") from exc


def _secure_mkdir_vm(vm_dir: Path, name: str) -> None:
    try:
        secure_mkdir(vm_dir, name)
    except MVMError:
        raise
