from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import Any, Self

from mvmctl.constants import (
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_EXECUTABLE,
    CONST_FILE_PERMS_PRIVATE_KEY,
    CONST_FILE_PERMS_PUBLIC_KEY,
    CONST_FILE_PERMS_SHADOW,
    CONST_FILE_PERMS_SUDOERS,
    CONST_SHADOW_DAYS_SINCE_EPOCH,
    CONST_SHADOW_MAX_DAYS,
    CONST_SHADOW_MIN_DAYS,
    CONST_SHADOW_WARN_DAYS,
    CONST_SHRINK_SAFETY_MARGIN,
    DEFAULT_LIBGUESTFS_SEED_DIR,
)
from mvmctl.core._shared._guestfs import OptimizedGuestfs
from mvmctl.exceptions import (
    GuestfsWriteError,
    VMBuilderError,
)

logger = logging.getLogger(__name__)

__all__ = ["GuestfsProvisioner"]


class GuestfsProvisioner:
    """All guestfs setup operations. Stateful - holds guestfs handle."""

    def __init__(
        self,
        rootfs_path: Path,
        *,
        readonly: bool = False,
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> None:
        """
        Initialize the GuestfsProvisioner.

        Args:
            rootfs_path: Path to the root filesystem image.
            readonly: Whether to open guestfs in read-only mode.
            root_uid: Root user UID in guest (default: 0).
            root_gid: Root group GID in guest (default: 0).
            user_uid: Default non-root user UID in guest (default: 1000).
            user_gid: Default non-root user GID in guest (default: 1000).

        """
        self._rootfs_path = rootfs_path
        self._readonly = readonly
        self._root_uid = root_uid
        self._root_gid = root_gid
        self._user_uid = user_uid
        self._user_gid = user_gid
        self._target_size: int | None = None
        self._hostname: str | None = None
        self._user: str | None = None
        self._ssh_pubkeys: list[str] = []
        self._cloud_init_dir: Path | None = None
        self._shrink_result: int | None = None
        self._ops: list[str] = []

    # =====================================================================
    # Builder methods — queue operations for a single guestfs session
    # =====================================================================

    def resize(self, target_size_bytes: int) -> Self:
        """Queue a resize operation."""
        self._target_size = target_size_bytes
        return self

    def set_hostname(self, hostname: str) -> Self:
        """Queue hostname setup."""
        self._hostname = hostname
        self._ops.append("set_hostname")
        return self

    def inject_dns(self, *, dns_server: str) -> Self:
        """Queue DNS injection."""
        self._dns_server = dns_server
        self._ops.append("inject_dns")
        return self

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> Self:
        """Queue SSH setup."""
        self._user = user
        self._ssh_pubkeys = ssh_pubkeys
        self._ops.append("setup_ssh")
        return self

    def inject_cloud_init(self, cloud_init_dir: Path) -> Self:
        """Queue cloud-init seed file injection."""
        self._cloud_init_dir = cloud_init_dir
        self._ops.append("inject_cloud_init")
        return self

    def disable_cloud_init(self) -> Self:
        """Queue cloud-init disable (datasource block + service masking)."""
        self._ops.append("disable_cloud_init")
        return self

    def shrink(self) -> Self:
        """Queue shrink-to-minimum operation."""
        self._ops.append("shrink")
        return self

    def deblob(self) -> Self:
        """Queue deblob (OS cache cleanup + fstab fix) operation."""
        self._ops.append("deblob")
        return self

    def fix_fstab(self) -> Self:
        """Queue fstab fix for Firecracker (PARTUUID → /dev/vda)."""
        self._ops.append("fix_fstab")
        return self

    # =====================================================================
    # Filesystem conversion (independent — not an _op)
    # =====================================================================

    def convert_to(self, target_fs: str) -> None:
        """Convert the image filesystem to *target_fs* using guestfs.

        Opens a fresh guestfs session with both drives, copies all files
        via ``tar --one-file-system`` (preserves attributes, auto-skips
        cross-device mount points), then replaces the original.

        Args:
            target_fs: Target filesystem type (e.g. ``"ext4"``).

        """

        import importlib as _importlib

        from mvmctl.utils._system import run_cmd as _run_cmd

        # Output path: same directory, .ext4 suffix
        output_path = self._rootfs_path.with_suffix(".ext4")

        from mvmctl.constants import CONST_ROOTFS_MIN_HEADROOM_BYTES

        _MEBI = 1024 * 1024

        # Size: source partition size + 150 MiB headroom for ext4 overhead,
        # rounded up to next MiB boundary.
        data_size = self._rootfs_path.stat().st_size
        size_bytes = data_size + CONST_ROOTFS_MIN_HEADROOM_BYTES
        size_bytes = ((size_bytes + _MEBI - 1) // _MEBI) * _MEBI
        size_mib = size_bytes // _MEBI

        # Create sparse output file
        _run_cmd(["truncate", "-s", f"{size_mib}M", str(output_path)])

        # Fresh guestfs session with two drives
        guestfs_mod = _importlib.import_module("guestfs")
        g = guestfs_mod.GuestFS(python_return_dict=True)

        # Mirror environment setup from OptimizedGuestfs._setup_environment
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

        orig_env: dict[str, str | None] = {
            "LIBGUESTFS_BACKEND": os.environ.get("LIBGUESTFS_BACKEND"),
            "QEMU_LOCKING": os.environ.get("QEMU_LOCKING"),
        }
        os.environ["LIBGUESTFS_BACKEND"] = "direct"
        os.environ["QEMU_LOCKING"] = "off"

        try:
            # Source drive added FIRST (/dev/sda), target drive SECOND (/dev/sdb).
            # Mount source at /, then mount target at /ext4 under the source.
            # Use tar --one-file-system to copy: it does NOT descend into
            # mount points on a different filesystem, so /ext4 (on /dev/sdb)
            # is automatically skipped — no infinite recursion.
            g.add_drive_opts(
                str(self._rootfs_path),
                format="raw",
                readonly=True,
            )
            g.add_drive_opts(
                str(output_path),
                format="raw",
                readonly=False,
            )

            g.launch()

            # Detect and mount the source filesystem
            filesystems: dict[str, str] = g.list_filesystems()
            root_dev: str | None = None
            for candidate in ["/dev/sda", "/dev/sda1", "/dev/vda", "/dev/vda1"]:
                if candidate in filesystems:
                    root_dev = candidate
                    break
            if root_dev is None and filesystems:
                root_dev = str(list(filesystems.keys())[0])
            if root_dev is None:
                raise RuntimeError(
                    f"No filesystem found in {self._rootfs_path}"
                )

            g.mount(root_dev, "/")

            # Create target filesystem and mount at /ext4
            g.mkfs(target_fs, "/dev/sdb")
            g.mkdir_p("/ext4")
            g.mount("/dev/sdb", "/ext4")

            # Copy all files preserving attributes.
            # tar --one-file-system does NOT descend into mount points on
            # a different filesystem, so /ext4 (on /dev/sdb) is automatically
            # skipped.  Virtual filesystems (/sys, /proc) from the appliance
            # overlay are also skipped, avoiding I/O errors.
            g.sh("tar cf - --one-file-system / | tar xf - -C /ext4")

            # Cleanup mounts inside guestfs
            g.umount("/ext4")
            g.umount("/")
            g.shutdown()
        except Exception:
            output_path.unlink(missing_ok=True)
            raise
        finally:
            try:
                g.close()
            except Exception:
                pass
            for key, value in orig_env.items():
                if value is not None:
                    os.environ[key] = value
                elif key in os.environ:
                    del os.environ[key]

        # Replace original with the new ext4 file
        output_path.rename(self._rootfs_path)

    # =====================================================================
    # Execution — single guestfs session for all queued operations
    # =====================================================================

    def run(self) -> None:
        """Execute all queued operations in a single guestfs session."""
        target_size: int | None = self._target_size
        needs_resize = target_size is not None
        if needs_resize:
            # Check if file already large enough — skip guestfs if so
            try:
                current_size = self._rootfs_path.stat().st_size
                if (
                    isinstance(current_size, int)
                    and target_size is not None
                    and current_size >= target_size
                ):
                    needs_resize = False
            except (OSError, AttributeError):
                pass

        if not self._ops and not needs_resize:
            return  # nothing to do

        # Phase 0: file truncation (before guestfs mount) — only when resizing
        if needs_resize:
            if target_size is None:  # pragma: no cover
                raise VMBuilderError(
                    "Internal error: target_size is None during resize"
                )
            self._do_truncate_file(self._rootfs_path, target_size)

        # Phase 1: guestfs session
        with OptimizedGuestfs(self._rootfs_path, readonly=self._readonly) as og:
            og.mount_rootfs()
            handle: Any = None
            try:
                handle = og._handle

                # Phase 1a: filesystem resize
                if needs_resize:
                    if target_size is None:  # pragma: no cover
                        raise VMBuilderError(
                            "Internal error: target_size is None during resize"
                        )
                    self._do_filesystem_resize(
                        handle, self._rootfs_path, target_size
                    )

                # Phase 1b: queued operations
                for op_name in self._ops:
                    getattr(self, f"_do_{op_name}")(handle)
            finally:
                if handle is not None:
                    try:
                        handle.umount("/")
                    except Exception:
                        pass

        # Phase 2: post-session truncation (shrink-to-minimum resize)
        if self._shrink_result is not None:
            final_size = int(self._shrink_result * CONST_SHRINK_SAFETY_MARGIN)
            with open(self._rootfs_path, "r+b") as f:
                f.truncate(final_size)

    @staticmethod
    def _do_truncate_file(path: Path, target_size: int) -> None:
        try:
            current_size = path.stat().st_size
            if isinstance(current_size, int) and current_size < target_size:
                with open(path, "r+b") as f:
                    f.truncate(target_size)
        except (OSError, AttributeError):
            pass

    @staticmethod
    def _do_filesystem_resize(
        handle: Any, rootfs_path: Path, target_size: int
    ) -> None:
        filesystems: dict[str, str] = handle.list_filesystems()
        root_device: str | None = None
        for candidate in ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"]:
            if candidate in filesystems:
                root_device = candidate
                break
        if root_device is None and filesystems:
            root_device = str(list(filesystems.keys())[0])
        if root_device is None:
            raise VMBuilderError(f"No filesystem found in {rootfs_path}")

        fs_type = handle.vfs_type(root_device)
        if fs_type in ("ext2", "ext3", "ext4"):
            handle.resize2fs(root_device)
        elif fs_type == "btrfs":
            handle.mount(root_device, "/")
            handle.btrfs_filesystem_resize("/", target_size)
            handle.umount(root_device)

    def _do_shrink(self, handle: Any) -> None:
        """Shrink filesystem to minimum size. Stores result for post-session truncation.

        Only shrinks when there is significant free space to reclaim (>2% free).
        """
        filesystems: dict[str, str] = handle.list_filesystems()
        root_device: str | None = None
        for candidate in ("/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"):
            if candidate in filesystems:
                root_device = candidate
                break
        if root_device is None and filesystems:
            root_device = str(list(filesystems.keys())[0])
        if root_device is None:
            raise VMBuilderError("No filesystem found for shrink")

        fs_type = handle.vfs_type(root_device)

        # Skip shrink for unsupported filesystem types
        if fs_type not in ("ext2", "ext3", "ext4", "btrfs"):
            logger.debug(
                "Skipping shrink: %s filesystem not supported",
                fs_type,
            )
            return

        # Check free space — skip if there isn't enough to reclaim
        stat = handle.statvfs("/")
        free_ratio = stat.get("bfree", 0) / max(stat.get("blocks", 1), 1)
        if free_ratio <= 0.02:
            logger.debug(
                "Filesystem has %.1f%% free space after deblob, skipping shrink",
                free_ratio * 100,
            )
            return

        if fs_type in ("ext2", "ext3", "ext4"):
            handle.zero_free_space("/")
            handle.umount("/")
            handle.e2fsck(root_device, correct=True)
            handle.resize2fs_size(root_device, 0)
        else:  # btrfs
            handle.sh("fstrim -av / 2>/dev/null || true")
            handle.btrfs_filesystem_sync("/")
            handle.btrfs_filesystem_resize("/", 0)
            handle.umount("/")

        self._shrink_result = handle.blockdev_getsize64(root_device)

    def _do_deblob(self, handle: Any) -> None:
        """Run OS cache cleanup and fstab fix inside the mounted guestfs image."""
        from mvmctl.core._shared._provisioner._content import (
            ChrootOp,
            FileOp,
            Operation,
            ProvisionerContent,
        )

        # Detect OS from mounted filesystem
        os_id, os_id_like = self._parse_os_release(handle)
        os_type = os_id if os_id else os_id_like

        ops: list[Operation] = list(
            ProvisionerContent.build_deblob_ops(os_type or "linux")
        )
        ops.extend(ProvisionerContent.build_fix_fstab_ops())

        for op in ops:
            match op:
                case ChrootOp():
                    handle.sh(op.command)
                case FileOp():
                    data_str: str = (
                        op.data.decode("utf-8", errors="replace")
                        if isinstance(op.data, bytes)
                        else str(op.data)
                    )
                    # Ensure parent directory exists before writing
                    if "/" in op.path:
                        parent = op.path.rsplit("/", 1)[0]
                        if parent:
                            handle.mkdir_p(parent)
                    handle.write(op.path, data_str)

    @staticmethod
    def _do_fix_fstab(handle: Any) -> None:
        """Fix /etc/fstab for Firecracker (PARTUUID → /dev/vda)."""
        from mvmctl.core._shared._provisioner._content import (
            ChrootOp,
            FileOp,
            Operation,
            ProvisionerContent,
        )

        ops: list[Operation] = list(ProvisionerContent.build_fix_fstab_ops())
        for op in ops:
            match op:
                case ChrootOp():
                    handle.sh(op.command)
                case FileOp():
                    data_str: str = (
                        op.data.decode("utf-8", errors="replace")
                        if isinstance(op.data, bytes)
                        else str(op.data)
                    )
                    # Ensure parent directory exists before writing
                    if "/" in op.path:
                        parent = op.path.rsplit("/", 1)[0]
                        if parent:
                            handle.mkdir_p(parent)
                    handle.write(op.path, data_str)

    @staticmethod
    def _parse_os_release(handle: Any) -> tuple[str, str]:
        """Parse ``/etc/os-release`` from the mounted filesystem.

        Returns ``("", "")`` if the file cannot be read or parsed.
        """
        try:
            raw = handle.read_file("/etc/os-release")
            if isinstance(raw, bytes):
                raw = raw.decode("utf-8", errors="replace")
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

    def _do_setup_ssh(self, handle: Any) -> None:
        """Configure SSH, user, host keys, and first-boot services."""
        if not self._ssh_pubkeys:
            logger.debug(
                "Skipping SSH setup for %s — no SSH pubkeys provided",
                self._rootfs_path.name,
            )
            return

        ssh_home_dir = (
            "/root" if self._user == "root" else f"/home/{self._user}"
        )
        self.ensure_user(handle)
        self.configure_ssh_keys(handle)
        self.generate_host_keys(handle)

        if not handle.exists("/root"):
            handle.mkdir_p("/root")
            handle.chmod(CONST_DIR_PERMS_CACHE, "/root")
            handle.chown(self._root_uid, self._root_gid, "/root")

        handle.mkdir_p(f"{ssh_home_dir}/.ssh")
        handle.chmod(CONST_DIR_PERMS_CACHE, f"{ssh_home_dir}/.ssh")
        handle.chown(self._root_uid, self._root_gid, f"{ssh_home_dir}/.ssh")
        handle.sync()

        existing_keys = ""
        auth_keys_path = f"{ssh_home_dir}/.ssh/authorized_keys"
        if handle.exists(auth_keys_path):
            existing_keys = handle.read_file(auth_keys_path)
            if isinstance(existing_keys, bytes):
                existing_keys = existing_keys.decode("utf-8", errors="replace")

        existing_set = (
            set(existing_keys.strip().split("\n"))
            if existing_keys.strip()
            else set()
        )
        new_keys = [
            key
            for key in self._ssh_pubkeys
            if key.strip() and key.strip() not in existing_set
        ]
        if new_keys:
            combined = existing_keys
            if combined and not combined.endswith("\n"):
                combined += "\n"
            combined += "\n".join(new_keys) + "\n"
            handle.write(auth_keys_path, combined)
            handle.chmod(CONST_FILE_PERMS_PRIVATE_KEY, auth_keys_path)
            handle.sync()

        self.enable_ssh(handle)

        from mvmctl.core._shared._provisioner._content import (
            ProvisionerContent,
        )

        handle.mkdir_p("/usr/local/bin")
        handle.write(
            "/usr/local/bin/first-boot-ssh-installer.sh",
            ProvisionerContent.first_boot_installer(),
        )
        handle.chmod(
            CONST_FILE_PERMS_EXECUTABLE,
            "/usr/local/bin/first-boot-ssh-installer.sh",
        )
        handle.mkdir_p("/etc/systemd/system")
        handle.write(
            "/etc/systemd/system/first-boot-ssh-installer.service",
            ProvisionerContent.first_boot_service(),
        )
        handle.chmod(
            CONST_FILE_PERMS_PUBLIC_KEY,
            "/etc/systemd/system/first-boot-ssh-installer.service",
        )
        handle.mkdir_p("/etc/systemd/system/multi-user.target.wants")
        handle.ln_s(
            "/etc/systemd/system/first-boot-ssh-installer.service",
            "/etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service",
        )
        logger.info(
            "Created first-boot SSH installer for %s",
            self._rootfs_path.name,
        )

    @staticmethod
    def _do_disable_cloud_init(handle: Any) -> None:
        """Block cloud-init datasources and mask cloud-init services."""
        handle.mkdir_p("/etc/cloud/cloud.cfg.d")
        handle.write(
            "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
            "datasource_list: [None]\n",
        )
        handle.write("/etc/cloud/cloud-init.disabled", "disabled by mvmctl\n")
        handle.mkdir_p("/etc/systemd/system/snapd.seeded.service.d")
        handle.write(
            "/etc/systemd/system/snapd.seeded.service.d/override.conf",
            "[Service]\nExecStart=\nExecStart=/bin/true\n",
        )
        handle.mkdir_p(
            "/etc/systemd/system/systemd-networkd-wait-online.service.d"
        )
        handle.write(
            "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
            "[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n",
        )

        for service_name in [
            "cloud-init.service",
            "cloud-init-local.service",
            "cloud-config.service",
            "cloud-final.service",
        ]:
            handle.ln_sf("/dev/null", f"/etc/systemd/system/{service_name}")

    def _do_inject_dns(self, handle: Any) -> None:
        resolv_path = "/etc/resolv.conf"
        needs_dns = True

        if handle.exists(resolv_path):
            try:
                existing_content = handle.read_file(resolv_path)
                if isinstance(existing_content, bytes):
                    existing_content = existing_content.decode(
                        "utf-8", errors="replace"
                    )
                stripped = existing_content.strip()
                if stripped and "nameserver" in stripped.lower():
                    needs_dns = False
            except RuntimeError:
                needs_dns = True

        if needs_dns:
            dns_content = f"nameserver {self._dns_server}\n"
            try:
                handle.write(resolv_path, dns_content)
            except RuntimeError:
                handle.rm(resolv_path)
                handle.write(resolv_path, dns_content)
            logger.debug("Injected default DNS into %s", resolv_path)

    def _do_set_hostname(self, handle: Any) -> None:
        hostname = self._hostname
        if not hostname:
            return

        handle.write("/etc/hostname", hostname)

        existing_hosts = ""
        if handle.exists("/etc/hosts"):
            existing_hosts = handle.read_file("/etc/hosts")
            if isinstance(existing_hosts, bytes):
                existing_hosts = existing_hosts.decode(
                    "utf-8", errors="replace"
                )

        lines = existing_hosts.splitlines() if existing_hosts else []
        new_lines = []
        found_host_entry = False
        for line in lines:
            stripped = line.strip()
            if stripped.startswith("#") or not stripped:
                new_lines.append(line)
            elif stripped.startswith("127.0.1.1"):
                new_lines.append(f"127.0.1.1\t{hostname}")
                found_host_entry = True
            else:
                new_lines.append(line)

        if not found_host_entry:
            new_lines.append(f"127.0.1.1\t{hostname}")

        handle.write("/etc/hosts", "\n".join(new_lines) + "\n")
        handle.sync()

    def _do_inject_cloud_init(self, handle: Any) -> None:
        """Inject cloud-init seed files into the mounted rootfs."""

        if self._cloud_init_dir is None:
            return

        seed_dir = DEFAULT_LIBGUESTFS_SEED_DIR
        handle.mkdir_p(seed_dir)

        required_files = ["meta-data", "user-data"]
        optional_files = ["network-config"]

        for filename in required_files:
            src = self._cloud_init_dir / filename
            if not src.exists():
                raise GuestfsWriteError(
                    f"Required cloud-init file not found: {src}"
                )
            dest = f"{seed_dir}/{filename}"
            try:
                handle.write(dest, src.read_bytes())
            except Exception as e:
                raise GuestfsWriteError(f"Failed to write {filename}: {e}")

        for filename in optional_files:
            src = self._cloud_init_dir / filename
            if src.exists():
                dest = f"{seed_dir}/{filename}"
                try:
                    handle.write(dest, src.read_bytes())
                except Exception as e:
                    raise GuestfsWriteError(f"Failed to write {filename}: {e}")

    def enable_ssh(self, guestfs_handle: Any) -> bool:
        """Detect init system and enable SSH service."""
        init_system = "unknown"

        if guestfs_handle.exists(
            "/lib/systemd/systemd"
        ) or guestfs_handle.exists("/usr/lib/systemd/systemd"):
            init_system = "systemd"
        elif guestfs_handle.exists("/sbin/openrc") or guestfs_handle.exists(
            "/usr/sbin/openrc"
        ):
            init_system = "openrc"
        elif guestfs_handle.exists("/etc/init.d/"):
            init_system = "sysvinit"
        else:
            logger.warning("Unknown init system in %s", self._rootfs_path.name)
            return False

        try:
            if init_system == "systemd":
                ssh_services = [
                    "/usr/lib/systemd/system/ssh.service",
                    "/lib/systemd/system/ssh.service",
                    "/etc/systemd/system/ssh.service",
                    "/usr/lib/systemd/system/sshd.service",
                    "/lib/systemd/system/sshd.service",
                    "/etc/systemd/system/sshd.service",
                ]

                ssh_service_path = None
                for service_path in ssh_services:
                    if guestfs_handle.exists(service_path):
                        ssh_service_path = service_path
                        break

                if ssh_service_path:
                    service_name = ssh_service_path.split("/")[-1]
                    target = f"/etc/systemd/system/multi-user.target.wants/{service_name}"
                    guestfs_handle.mkdir_p(
                        "/etc/systemd/system/multi-user.target.wants"
                    )
                    if not guestfs_handle.exists(target):
                        guestfs_handle.ln_s(ssh_service_path, target)
                    logger.info(
                        "Enabled SSH service (systemd) for %s",
                        self._rootfs_path.name,
                    )
                    return True
                logger.warning(
                    "SSH service unit not found in %s", self._rootfs_path.name
                )
                return False

            if init_system == "openrc":
                guestfs_handle.mkdir_p("/etc/runlevels/default")
                if guestfs_handle.exists("/etc/init.d/sshd"):
                    if not guestfs_handle.exists("/etc/runlevels/default/sshd"):
                        guestfs_handle.ln_s(
                            "/etc/init.d/sshd", "/etc/runlevels/default/sshd"
                        )
                    logger.info(
                        "Enabled SSH service (OpenRC) for %s",
                        self._rootfs_path.name,
                    )
                    return True
                if guestfs_handle.exists("/etc/init.d/ssh"):
                    if not guestfs_handle.exists("/etc/runlevels/default/ssh"):
                        guestfs_handle.ln_s(
                            "/etc/init.d/ssh", "/etc/runlevels/default/ssh"
                        )
                    logger.info(
                        "Enabled SSH service (OpenRC) for %s",
                        self._rootfs_path.name,
                    )
                    return True
                logger.warning(
                    "SSH init script not found for OpenRC in %s",
                    self._rootfs_path.name,
                )
                return False

            if guestfs_handle.exists("/etc/init.d/ssh"):
                for level in ["2", "3", "4", "5"]:
                    guestfs_handle.mkdir_p(f"/etc/rc{level}.d")
                    link_path = f"/etc/rc{level}.d/S02ssh"
                    if not guestfs_handle.exists(link_path):
                        guestfs_handle.ln_s("../init.d/ssh", link_path)
                logger.info(
                    "Enabled SSH service (sysvinit) for %s",
                    self._rootfs_path.name,
                )
                return True

            logger.warning(
                "SSH init script not found for sysvinit in %s",
                self._rootfs_path.name,
            )
            return False
        except Exception as exc:
            logger.error(
                "Failed to enable SSH for %s: %s", self._rootfs_path.name, exc
            )
            return False

    def configure_ssh_keys(self, guestfs_handle: Any) -> None:
        """Configure SSH key authentication in guest."""
        try:
            if not guestfs_handle.exists("/etc/ssh/sshd_config"):
                logger.warning(
                    "sshd_config not found in %s", self._rootfs_path.name
                )
                return

            from mvmctl.core._shared._provisioner._content import (
                ProvisionerContent,
            )

            sshd_config_dir = "/etc/ssh/sshd_config.d"
            guestfs_handle.mkdir_p(sshd_config_dir)
            user = self._user or "root"
            guestfs_handle.write(
                f"{sshd_config_dir}/mvm.conf",
                ProvisionerContent.sshd_config(user),
            )
            guestfs_handle.chmod(
                CONST_FILE_PERMS_PUBLIC_KEY, f"{sshd_config_dir}/mvm.conf"
            )
            logger.info(
                "Configured SSH key authentication for user '%s' in %s",
                self._user,
                self._rootfs_path.name,
            )
        except Exception as exc:
            logger.warning("Failed to configure sshd: %s", exc)

    def ensure_user(self, guestfs_handle: Any) -> None:
        """Create user in guest with sudoers."""
        if self._user == "root":
            return

        try:
            passwd_content = ""
            if guestfs_handle.exists("/etc/passwd"):
                passwd_content = guestfs_handle.read_file("/etc/passwd")
                if isinstance(passwd_content, bytes):
                    passwd_content = passwd_content.decode(
                        "utf-8", errors="replace"
                    )

            for line in passwd_content.strip().split("\n"):
                if line.startswith(f"{self._user}:"):
                    logger.debug(
                        "User '%s' already exists in %s",
                        self._user,
                        self._rootfs_path.name,
                    )
                    return

            home_dir = f"/home/{self._user}"
            guestfs_handle.mkdir_p(home_dir)
            guestfs_handle.mkdir_p(f"{home_dir}/.ssh")
            guestfs_handle.write(
                "/etc/passwd",
                f"{self._user}:!:{self._user_uid}:{self._user_gid}::{home_dir}:/bin/bash\n",
                mode="a",
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/passwd")
            guestfs_handle.write(
                "/etc/shadow",
                f"{self._user}:!:{CONST_SHADOW_DAYS_SINCE_EPOCH}:{CONST_SHADOW_MIN_DAYS}:{CONST_SHADOW_MAX_DAYS}:{CONST_SHADOW_WARN_DAYS}:::\n",
                mode="a",
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_SHADOW, "/etc/shadow")
            guestfs_handle.write(
                "/etc/group",
                f"{self._user}:x:{self._user_gid}:\n",
                mode="a",
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/group")
            guestfs_handle.mkdir_p("/etc/sudoers.d")
            guestfs_handle.write(
                f"/etc/sudoers.d/{self._user}",
                f"{self._user} ALL=(ALL) NOPASSWD: ALL\n",
            )
            guestfs_handle.chmod(
                CONST_FILE_PERMS_SUDOERS, f"/etc/sudoers.d/{self._user}"
            )
            guestfs_handle.chown(self._user_uid, self._user_gid, home_dir)
            guestfs_handle.chown(
                self._user_uid,
                self._user_gid,
                f"{home_dir}/.ssh",
            )
            logger.info(
                "Created user '%s' with UID/GID 1000 in %s",
                self._user,
                self._rootfs_path.name,
            )
        except Exception as exc:
            logger.warning("Failed to create user '%s': %s", self._user, exc)

    def generate_host_keys(self, guestfs_handle: Any) -> None:
        """Set up SSH host key generation service."""
        try:
            key_types = [
                "ssh_host_rsa_key",
                "ssh_host_ecdsa_key",
                "ssh_host_ed25519_key",
            ]
            missing_keys = [
                key
                for key in key_types
                if not guestfs_handle.exists(f"/etc/ssh/{key}")
            ]
            if not missing_keys:
                logger.debug(
                    "All SSH host keys already exist in %s",
                    self._rootfs_path.name,
                )
                return

            guestfs_handle.mkdir_p("/etc/local.d")
            guestfs_handle.write(
                "/etc/local.d/ssh-keygen.start",
                "#!/bin/bash\n"
                'SSH_KEYDIR="/etc/ssh"\n'
                "for key_type in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do\n"
                '  key_path="$SSH_KEYDIR/$key_type"\n'
                '  if [ ! -f "$key_path" ]; then\n'
                '    case "$key_type" in\n'
                '      ssh_host_rsa_key) ssh-keygen -t rsa -f "$key_path" -N "" -q 2>/dev/null ;;\n'
                '      ssh_host_ecdsa_key) ssh-keygen -t ecdsa -f "$key_path" -N "" -q 2>/dev/null ;;\n'
                '      ssh_host_ed25519_key) ssh-keygen -t ed25519 -f "$key_path" -N "" -q 2>/dev/null ;;\n'
                "    esac\n"
                '    chmod 600 "$key_path" 2>/dev/null\n'
                '    chmod 644 "${key_path}.pub" 2>/dev/null\n'
                "  fi\n"
                "done\n"
                "rm -f /etc/local.d/ssh-keygen.start 2>/dev/null\n"
                "exit 0\n",
            )
            guestfs_handle.chmod(
                CONST_FILE_PERMS_EXECUTABLE, "/etc/local.d/ssh-keygen.start"
            )
            if guestfs_handle.exists("/sbin/openrc") or guestfs_handle.exists(
                "/usr/sbin/openrc"
            ):
                guestfs_handle.mkdir_p("/etc/runlevels/default")
                if not guestfs_handle.exists("/etc/runlevels/default/local"):
                    guestfs_handle.ln_s(
                        "/sbin/openrc-local", "/etc/runlevels/default/local"
                    )

            guestfs_handle.mkdir_p("/etc/systemd/system")
            guestfs_handle.write(
                "/etc/systemd/system/ssh-hostkeygen.service",
                "[Unit]\nDescription=SSH Host Key Generation\n"
                "Before=ssh.service\nAfter=local-fs.target\n\n"
                "[Service]\nType=oneshot\nExecStart=/bin/bash /etc/local.d/ssh-keygen.start\nRemainAfterExit=yes\n\n"
                "[Install]\nWantedBy=multi-user.target\n",
            )
            guestfs_handle.chmod(
                CONST_FILE_PERMS_PUBLIC_KEY,
                "/etc/systemd/system/ssh-hostkeygen.service",
            )
            guestfs_handle.mkdir_p(
                "/etc/systemd/system/multi-user.target.wants"
            )
            guestfs_handle.ln_s(
                "/etc/systemd/system/ssh-hostkeygen.service",
                "/etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service",
            )
            logger.info(
                "Created SSH host key generation service in %s",
                self._rootfs_path.name,
            )
        except Exception as exc:
            logger.warning("Failed to setup SSH host key generation: %s", exc)
