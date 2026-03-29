"""Rootfs injection using libguestfs for reliable cloud-init seeding."""

import importlib
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    DEFAULT_LIBGUESTFS_ROOT_INDICATORS,
    DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT,
    DEFAULT_LIBGUESTFS_ROOT_DEVICE,
    DEFAULT_LIBGUESTFS_SEED_DIR,
)
from mvmctl.exceptions import (
    GuestfsLaunchError,
    GuestfsMountError,
    GuestfsNotAvailableError,
    GuestfsWriteError,
)


def check_libguestfs() -> bool:
    """Check if libguestfs Python bindings are available."""
    try:
        guestfs = importlib.import_module("guestfs")

        # Verify the module has the expected interface
        return hasattr(guestfs, "GuestFS")
    except ImportError:
        return False


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
        GuestfsLaunchError: If the guestfs appliance fails to launch
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

    guestfs = importlib.import_module("guestfs")

    g: Any = guestfs.GuestFS(python_return_dict=True)

    try:
        # Add the disk image
        g.add_drive(rootfs_path, readonly=False)

        # Launch the appliance
        try:
            g.set_timeout(DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT)
            g.launch()
        except Exception as e:
            raise GuestfsLaunchError(f"Failed to launch guestfs appliance: {e}")

        # Detect and mount root partition
        root_device = _detect_root_partition(g, rootfs_path)
        try:
            g.mount(root_device, "/")
        except Exception as e:
            raise GuestfsMountError(f"Failed to mount {root_device}: {e}")

        # Write cloud-init files
        _write_cloud_init_files(g, cloud_init_dir)

    finally:
        # Always cleanup
        try:
            g.shutdown()
        except Exception:
            pass
        try:
            g.close()
        except Exception:
            pass
