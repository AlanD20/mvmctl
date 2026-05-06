from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    import guestfs

from mvmctl.constants import (
    CONST_GUESTFS_OS_RELEASE_PATH,
    CONST_SHRINK_SAFETY_MARGIN,
)
from mvmctl.exceptions import (
    GuestfsError,
    GuestfsNotAvailableError,
    MVMRuntimeError,
)

from ._kernel_detector import KernelDetector


class OptimizedGuestfs:
    def __init__(self, disk_path: Path, readonly: bool = False) -> None:
        self.disk_path = disk_path
        self.readonly = readonly
        self._g: guestfs.GuestFS | None = None
        self._orig_env: dict[str, str | None] = {}

        # Verify guestfs is available at construction time
        try:
            import importlib

            guestfs = importlib.import_module("guestfs")
            if not hasattr(guestfs, "GuestFS"):
                raise GuestfsNotAvailableError("libguestfs is not available")
        except ImportError:
            raise GuestfsNotAvailableError("libguestfs is not available")

    def _setup_environment(self) -> None:
        self._orig_env = {
            "LIBGUESTFS_BACKEND": os.environ.get("LIBGUESTFS_BACKEND"),
            "LIBGUESTFS_CACHEDIR": os.environ.get("LIBGUESTFS_CACHEDIR"),
            "QEMU_LOCKING": os.environ.get("QEMU_LOCKING"),
            "SUPERMIN_KERNEL": os.environ.get("SUPERMIN_KERNEL"),
            "SUPERMIN_MODULES": os.environ.get("SUPERMIN_MODULES"),
        }
        os.environ["LIBGUESTFS_BACKEND"] = "direct"
        if Path("/dev/shm").exists():
            os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"
        # Disable QEMU file locking — prevents stale lock issues from crashed
        # guestfs sessions on shared images (ready pool, etc.)
        os.environ["QEMU_LOCKING"] = "off"

        # Force a known-good kernel with virtio drivers instead of relying on
        # libguestfs auto-detection, which may pick a kernel without virtio
        # and cause QEMU to hang on launch.
        kernel_info = KernelDetector.find_best_kernel()
        if kernel_info is not None:
            kernel_path, modules_dir = kernel_info
            os.environ["SUPERMIN_KERNEL"] = str(kernel_path)
            os.environ["SUPERMIN_MODULES"] = str(modules_dir)

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
        if hasattr(g, "set_backend"):
            g.set_backend("direct")

        g.add_drive_opts(
            str(self.disk_path),
            format="raw",
            readonly=self.readonly,
            cachemode="writeback",
        )

        return g

    def __enter__(self) -> OptimizedGuestfs:
        import time

        self._setup_environment()
        last_error: Exception | None = None
        for attempt in range(3):
            try:
                self._g = self._create_handle()
                if self._g is None:
                    raise MVMRuntimeError("guestfs handle not initialized")
                self._g.launch()
                return self
            except Exception as e:
                last_error = e
                if self._g is not None:
                    try:
                        self._g.close()
                    except Exception:
                        pass
                    self._g = None
                if attempt < 2:
                    time.sleep(0.5 * (attempt + 1))
        self._restore_environment()
        raise GuestfsError(
            f"Failed to launch guestfs: {last_error}"
        ) from last_error

    @property
    def _handle(self) -> guestfs.GuestFS:
        """Return the raw guestfs handle, raising if not initialized."""
        if self._g is None:
            raise GuestfsError(
                "Guestfs handle not initialized. "
                "Use 'with OptimizedGuestfs(...)' to properly initialize."
            )
        return self._g

    def mount_rootfs(self) -> str:
        """Mount the root filesystem and return the root device."""
        filesystems = self._handle.list_filesystems()
        root_device: str | None = None
        for candidate in ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"]:
            if candidate in filesystems:
                root_device = candidate
                break
        if root_device is None and filesystems:
            assert isinstance(filesystems, dict)
            root_device = str(list(filesystems.keys())[0])
        if root_device is None:
            raise GuestfsError(f"No filesystem found in {self.disk_path}")
        self._handle.mount(root_device, "/")
        return root_device

    def find_largest_linux_fs(self, partitions: list[str]) -> str | None:
        """Find the largest Linux filesystem among partitions."""
        max_size = 0
        root_device = None
        for dev in partitions:
            try:
                fs_type = self._handle.vfs_type(dev)
                if fs_type in ("ext2", "ext3", "ext4", "btrfs", "xfs"):
                    self._handle.mount(dev, "/")
                    try:
                        stat = self._handle.statvfs("/")
                        size = stat.get("blocks", 0) * stat.get("bsize", 4096)
                        if size > max_size:
                            max_size = size
                            root_device = dev
                    finally:
                        self._handle.umount(dev)
            except Exception:
                continue
        return root_device

    def get_fs_size(self, device: str) -> int:
        """Get the size of a filesystem."""
        self._handle.mount(device, "/")
        try:
            stat = self._handle.statvfs("/")
            size = stat.get("blocks", 0) * stat.get("bsize", 4096)
            return int(size)
        finally:
            self._handle.umount(device)

    def deblob(self) -> None:
        """Run OS-specific and common cleanup commands inside a mounted guestfs image.

        Detects the guest OS via ``/etc/os-release`` and dispatches to the
        appropriate private ``_deblob_<os>()`` method for OS-specific tasks
        (package cache scrub, network configuration deblobbing, etc.).

        Common cross-distro cleanup (doc/man files, logs, tmp) always runs
        regardless of OS.
        """
        logger = logging.getLogger(__name__)
        try:
            os_id, os_id_like = self._parse_os_release()
            logger.debug(
                "Deblo bing guest OS: id=%s id_like=%s", os_id, os_id_like
            )

            if "alpine" in (os_id, os_id_like):
                self._deblob_alpine()
            elif "ubuntu" in (os_id, os_id_like) or "debian" in (
                os_id,
                os_id_like,
            ):
                self._deblob_debian()
            elif "arch" in (os_id, os_id_like) or "manjaro" in (
                os_id,
                os_id_like,
            ):
                self._deblob_arch()
            elif (
                "fedora" in (os_id, os_id_like)
                or "rhel" in (os_id, os_id_like)
                or "centos" in (os_id, os_id_like)
            ):
                self._deblob_fedora()
            else:
                logger.debug(
                    "No OS-specific deblob for id=%s id_like=%s",
                    os_id,
                    os_id_like,
                )

            # Fix fstab: partition table is always stripped during image
            # conversion (qcow2 → raw ext4), so any PARTUUID= references in
            # fstab refer to non-existent partitions.  The root entry is
            # replaced with /dev/vda (the single Firecracker virtio block
            # device).  Non-root PARTUUID entries (e.g. /boot/efi) are
            # commented out — they have no counterpart in Firecracker.
            self._fix_fstab()

            # Common cleanup — every distro
            self._handle.sh(
                "rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/*"
            )
            self._handle.sh(
                "rm -rf /var/log/*.log /var/log/*.gz /var/log/journal/*"
            )
            self._handle.sh("rm -rf /tmp/*")
            self._handle.sh("find /var/log -type f -delete 2>/dev/null || true")
            self._handle.sh("sync")
        except Exception as e:
            logger.debug("Cleanup phase encountered issue (non-fatal): %s", e)

    def _parse_os_release(self) -> tuple[str, str]:
        """Parse ``/etc/os-release`` and return ``(ID, ID_LIKE)``.

        Returns ``("", "")`` if the file cannot be read or parsed.
        """
        try:
            raw = self._handle.cat(CONST_GUESTFS_OS_RELEASE_PATH) or ""
            id_val = ""
            id_like_val = ""
            for line in raw.splitlines():
                line = line.strip()
                if line.startswith("ID="):
                    id_val = line.split("=", 1)[1].strip("\"'").lower()
                elif line.startswith("ID_LIKE="):
                    id_like_val = line.split("=", 1)[1].strip("\"'").lower()
            return id_val, id_like_val
        except Exception:
            return "", ""

    def _deblob_alpine(self) -> None:
        """Alpine-specific deblob: APK cache scrub + prevent dhcpcd on eth0.

        The kernel already assigns the static IP via the ``ip=`` boot parameter,
        but Alpine's OpenRC still starts ``dhcpcd`` on ``eth0``, which waits
        ~10s for a DHCP lease, times out, falls back to IPv4LL, and only then
        starts sshd.  Adding ``denyinterfaces eth0`` to ``/etc/dhcpcd.conf``
        tells dhcpcd to skip eth0 entirely, saving ~10s per boot.
        """
        self._handle.sh("rm -rf /var/cache/apk/*")
        # Prevent dhcpcd from touching the statically-configured eth0
        self._handle.sh(
            "grep -qxF 'denyinterfaces eth0' /etc/dhcpcd.conf "
            "2>/dev/null || echo 'denyinterfaces eth0' >> /etc/dhcpcd.conf"
        )
        logging.getLogger(__name__).debug(
            "Alpine deblob: APK cache cleared, denyinterfaces eth0 applied"
        )

    def _deblob_debian(self) -> None:
        """Debian/Ubuntu-specific deblob: APT cache scrub."""
        self._handle.sh("apt-get clean")
        self._handle.sh("rm -rf /var/lib/apt/lists/*")
        self._handle.sh("rm -rf /var/cache/debconf/*")

    def _deblob_arch(self) -> None:
        """Arch-specific deblob: pacman cache scrub."""
        self._handle.sh("pacman -Sc --noconfirm || true")
        self._handle.sh("rm -rf /var/cache/pacman/pkg/*")

    def _deblob_fedora(self) -> None:
        """Fedora/RHEL/CentOS-specific deblob: DNF/YUM cache scrub."""
        self._handle.sh("dnf clean all || yum clean all || true")
        self._handle.sh("rm -rf /var/cache/dnf/* /var/cache/yum/*")

    def _fix_fstab(self) -> None:
        """Fix fstab entries that reference non-existent partitions.

        Image conversion (qcow2 → raw ext4) strips the partition table, so
        any ``PARTUUID=...`` refer to partitions that no longer exist.
        systemd waits up to 90s for them, then drops to emergency mode.

        Fixes applied:
        - Root mount: ``PARTUUID=<uuid> / ...`` → ``/dev/vda / ...``
        - Non-root mounts with ``PARTUUID=`` (e.g. ``/boot/efi``):
          commented out — they have no Firecracker counterpart.
        - ``UUID=`` and other references are left untouched (filesystem UUIDs
          are preserved through conversion).
        """
        fstab_path = "/etc/fstab"
        logger = logging.getLogger(__name__)
        try:
            raw = self._handle.read_file(fstab_path)
        except Exception:
            logger.debug("No /etc/fstab found — skipping fstab fix")
            return

        new_lines: list[str] = []
        modified = False
        for line in raw.decode().splitlines(keepends=True):
            stripped = line.strip()

            # Skip comments and blank lines
            if not stripped or stripped.startswith("#"):
                new_lines.append(line)
                continue

            fields = stripped.split()
            if len(fields) < 2:
                new_lines.append(line)
                continue

            spec = fields[0]  # First field: device/identifier
            mount_point = fields[1]  # Second field: mount point

            if not spec.startswith("PARTUUID="):
                new_lines.append(line)
                continue

            if mount_point == "/":
                # Root mount: replace PARTUUID with /dev/vda
                new_lines.append(line.replace(spec, "/dev/vda", 1))
                logger.debug(
                    "fstab: replaced %s with /dev/vda for root mount", spec
                )
                modified = True
            else:
                # Non-root PARTUUID mount → comment out
                new_lines.append(f"# {line}")
                logger.debug(
                    "fstab: commented out %s mount at %s", spec, mount_point
                )
                modified = True

        if modified:
            self._handle.write(fstab_path, "".join(new_lines).encode())
            logger.info("Fixed PARTUUID references in /etc/fstab")
        else:
            logger.debug("fstab already clean — no changes")

    def shrink_ext4(self, device: str) -> None:
        """Shrink an ext4 filesystem to minimum size.

        Note: ``deblob()`` is expected to have been called *before* this
        method — it is NOT called here to avoid duplicate work.
        """
        self._handle.mount(device, "/")
        self._handle.zero_free_space(device)
        self._handle.umount(device)
        self._handle.e2fsck(device, correct=True)
        self._handle.umount(device)
        self._handle.resize2fs_size(device, 0)

    def shrink_btrfs(self, device: str) -> None:
        """Shrink a btrfs filesystem to minimum size.

        Note: ``deblob()`` is expected to have been called *before* this
        method — it is NOT called here to avoid duplicate work.
        """
        self._handle.mount(device, "/")
        self._handle.sh("fstrim -av / 2>/dev/null || true")
        self._handle.btrfs_filesystem_sync("/")
        self._handle.btrfs_filesystem_resize("/", 0)
        self._handle.umount(device)

    def grow_fs(self, device: str, target_size_bytes: int) -> None:
        """
        Grow a filesystem to fill the allocated space.

        This is the inverse of shrink_ext4/shrink_btrfs. It resizes a filesystem
        to occupy the full allocated space after the backing file has been
        truncated to a larger size.

        Args:
            device: The filesystem device (e.g., "/dev/sda1")
            target_size_bytes: The target size to grow the filesystem to

        Raises:
            MVMError: If the filesystem type is not supported for growing

        """
        fs_type = self._handle.vfs_type(device)
        if fs_type in ("ext2", "ext3", "ext4"):
            self._handle.resize2fs(device)
        elif fs_type == "btrfs":
            self._handle.mount(device, "/")
            try:
                self._handle.btrfs_filesystem_resize("/", target_size_bytes)
            finally:
                self._handle.umount(device)
        else:
            raise GuestfsError(
                f"Cannot grow {fs_type} filesystem: not supported"
            )

    def list_partitions(self) -> list[str]:
        """List partitions in the disk image."""
        return self._handle.list_partitions()

    def vfs_type(self, device: str) -> str:
        """Get the filesystem type of a device."""
        return self._handle.vfs_type(device)

    def blockdev_getsize64(self, device: str) -> int:
        """Get the size of a block device in bytes."""
        return self._handle.blockdev_getsize64(device)

    def copy_device_to_file(self, device: str, output_path: str) -> None:
        """Copy a device to a file."""
        self._handle.copy_device_to_file(device, output_path)

    @classmethod
    def extract_partition(
        cls,
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
    ) -> Path | None:
        """
        Extract root partition using libguestfs for reliable VHD handling.

        Uses guestfs to reliably extract partitions from VHD-converted images
        that may have non-standard partition tables.

        Args:
            raw_path: Path to the raw disk image
            output_path: Path to write the extracted filesystem image
            partition: Partition number (1-indexed), or None for auto-detect

        Returns:
            Path to the extracted filesystem image, or None if guestfs unavailable/fails

        """
        logger = logging.getLogger(__name__)

        try:
            og = cls(raw_path, readonly=True)
        except GuestfsNotAvailableError:
            return None

        try:
            with og as og:
                partitions = og.list_partitions()
                if not partitions:
                    # No partition table — check if image is a direct filesystem (superfloppy)
                    try:
                        fs_type = og.vfs_type("/dev/sda")
                        logger.debug("Superfloppy image detected: %s", fs_type)
                        # Copy the whole file as-is
                        import shutil

                        shutil.copy2(raw_path, output_path)
                        logger.info(
                            "Copied superfloppy image: %s",
                            output_path.name,
                        )
                        return output_path
                    except Exception:
                        logger.debug(
                            "No partitions and not a superfloppy filesystem"
                        )
                        return None

                root_device: str | None = None
                if partition is not None:
                    if partition < 1 or partition > len(partitions):
                        logger.debug(
                            "Partition %d out of range (1-%d)",
                            partition,
                            len(partitions),
                        )
                        return None
                    root_device = partitions[partition - 1]
                else:
                    root_device = og.find_largest_linux_fs(partitions)

                if root_device is None:
                    root_device = partitions[0]

                fs_size = og.get_fs_size(root_device)
                og.copy_device_to_file(root_device, str(output_path))

                if fs_size > 0:
                    final_size = int(fs_size * CONST_SHRINK_SAFETY_MARGIN)
                    with open(output_path, "r+b") as f:
                        f.truncate(final_size)

                logger.info(
                    "Extracted root partition via guestfs: %s", output_path.name
                )
                return output_path
        except Exception as e:
            logger.debug("Guestfs extraction failed: %s", e)
            return None

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        try:
            if self._g is not None:
                try:
                    self._g.shutdown()
                except Exception:
                    pass
                try:
                    self._g.close()
                except Exception:
                    pass
        finally:
            self._g = None
            self._restore_environment()
