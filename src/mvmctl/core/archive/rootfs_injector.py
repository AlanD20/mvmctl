"""Rootfs injection using libguestfs for reliable cloud-init seeding."""

from pathlib import Path
from typing import Any

from mvmctl.constants import (
    DEFAULT_LIBGUESTFS_ROOT_DEVICE,
    DEFAULT_LIBGUESTFS_ROOT_INDICATORS,
    DEFAULT_LIBGUESTFS_SEED_DIR,
)
from mvmctl.exceptions import (
    GuestfsMountError,
    GuestfsNotAvailableError,
    GuestfsWriteError,
)
from mvmctl.utils.guestfs import check_libguestfs, optimized_guestfs


def _detect_root_partition(g: Any, rootfs_path: str) -> str:
    """
    Detect the root partition in the disk image.

    Returns device path like '/dev/sda1' or raises GuestfsMountError.
    """
    filesystems: dict[str, str] = g.list_filesystems()

    # Strategy 1: Look for partitions labeled "root" or "/"
    for device, fstype in filesystems.items():
        try:
            g.mount(device, "/")
            # Check if this looks like a root filesystem
            if any(g.exists(path) for path in DEFAULT_LIBGUESTFS_ROOT_INDICATORS):
                g.umount("/")
                return str(device)
            g.umount("/")
        except Exception:
            continue

    # Strategy 2: Fallback to configured device if it exists
    if DEFAULT_LIBGUESTFS_ROOT_DEVICE in filesystems:
        return DEFAULT_LIBGUESTFS_ROOT_DEVICE

    # Strategy 3: Use first available partition
    if filesystems:
        return str(list(filesystems.keys())[0])

    raise GuestfsMountError(f"No suitable root partition found in {rootfs_path}")


def _write_cloud_init_files(g: Any, cloud_init_dir: str) -> None:
    """Write cloud-init files to the mounted rootfs."""
    seed_dir = DEFAULT_LIBGUESTFS_SEED_DIR

    # Create seed directory if it doesn't exist
    g.mkdir_p(seed_dir)

    required_files = ["meta-data", "user-data"]
    optional_files = ["network-config"]

    # Write required files
    for filename in required_files:
        src = Path(cloud_init_dir) / filename
        if not src.exists():
            raise GuestfsWriteError(f"Required cloud-init file not found: {src}")

        dest = f"{seed_dir}/{filename}"
        try:
            g.write(dest, src.read_text())
        except Exception as e:
            raise GuestfsWriteError(f"Failed to write {filename}: {e}")

    # Write optional files
    for filename in optional_files:
        src = Path(cloud_init_dir) / filename
        if src.exists():
            dest = f"{seed_dir}/{filename}"
            try:
                g.write(dest, src.read_text())
            except Exception as e:
                raise GuestfsWriteError(f"Failed to write {filename}: {e}")


def inject_cloud_init(rootfs_path: str, cloud_init_dir: str) -> None:
    """
    Inject cloud-init files into a rootfs image using libguestfs.

    Args:
        rootfs_path: Path to the rootfs disk image file
        cloud_init_dir: Directory containing cloud-init files

    Raises:
        GuestfsNotAvailableError: If libguestfs Python bindings are not installed
        GuestfsMountError: If unable to detect or mount the root partition
        GuestfsWriteError: If writing cloud-init files fails
        FileNotFoundError: If rootfs_path or cloud_init_dir does not exist
    """
    # Validate inputs
    if not Path(rootfs_path).exists():
        raise FileNotFoundError(f"Rootfs image not found: {rootfs_path}")
    if not Path(cloud_init_dir).exists():
        raise FileNotFoundError(f"Cloud-init directory not found: {cloud_init_dir}")

    # Check libguestfs availability
    if not check_libguestfs():
        raise GuestfsNotAvailableError(
            "libguestfs Python bindings not available. Install python3-libguestfs package."
        )

    with optimized_guestfs(Path(rootfs_path), readonly=False) as g:
        # Detect and mount root partition
        root_device = _detect_root_partition(g._g, rootfs_path)
        try:
            g._g.mount(root_device, "/")
        except Exception as e:
            raise GuestfsMountError(f"Failed to mount {root_device}: {e}")

        # Write cloud-init files
        _write_cloud_init_files(g._g, cloud_init_dir)

        # Explicit umount before shutdown (required when autosync is disabled)
        try:
            g._g.umount("/")
        except Exception:
            pass  # Already unmounted or not mounted
