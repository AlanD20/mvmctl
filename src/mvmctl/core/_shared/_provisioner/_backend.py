"""
Backend classes for rootfs provisioning — shared between VMProvisioner and ImageProvisioner.

Two backends are available:

- ``_LoopMountBackend``: Uses the compiled ``mvm-provision`` binary
  (via ``LoopMountProvisioner``) to loop-mount the image, write files,
  run chroot commands, and resize filesystems.  ~200ms per VM.

- ``_GuestfsBackend``: Uses ``libguestfs`` (via ``GuestfsProvisioner``)
  to mount the image in a QEMU appliance.  ~2600ms per VM.  Used only
  as fallback when the loop-mount binary is unavailable.

Use ``ProvisionerBackend`` to construct the right backend::

    from mvmctl.core._shared._provisioner._backend import ProvisionerBackend

    # For VM provisioning
    backend = ProvisionerBackend.get_vm(
        rootfs_path=...,
        provisioner_type=ProvisionerType.LOOP_MOUNT,
        fs_type="ext4",
    )

    # For image optimization
    backend = ProvisionerBackend.get_image(
        image_path=...,
        provisioner_type=ProvisionerType.LOOP_MOUNT,
        fs_type="ext4",
    )
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from mvmctl.exceptions import ProcessError
from mvmctl.models.provisioner import ProvisionerType
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)


class _NoPartitionTable:
    """Sentinel: raw image has no partition table and should be used as-is."""


_NO_PARTITION_TABLE = _NoPartitionTable()

# =========================================================================
# Backend: loop-mount (mvm-provision binary)
# =========================================================================


class _LoopMountBackend:
    """Delegates all operations to ``LoopMountProvisioner``."""

    def __init__(self, rootfs_path: Path, fs_type: str) -> None:
        from mvmctl.core._shared._loopmount import LoopMountProvisioner

        self._lp: Any = LoopMountProvisioner(rootfs_path, fs_type)

    def resize(self, target_size_bytes: int) -> None:
        """Queue a rootfs resize operation (0 = shrink to minimum)."""
        from mvmctl.core._shared._provisioner._content import (
            ProvisionerContent,
        )

        if target_size_bytes == 0:
            self._lp._ops.extend(ProvisionerContent.build_shrink_ops())
        else:
            self._lp.resize(target_size_bytes)

    def set_hostname(self, hostname: str) -> None:
        """Queue hostname + /etc/hosts setup."""
        self._lp.set_hostname(hostname)

    def inject_dns(self, *, dns_server: str) -> None:
        """Queue DNS resolver injection."""
        self._lp.inject_dns(dns_server=dns_server)

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        """Queue SSH key, config, and host-key generation."""
        self._lp.setup_ssh(user, ssh_pubkeys)

    def disable_cloud_init(self) -> None:
        """Queue cloud-init datasource blocking + service masking."""
        self._lp.disable_cloud_init()

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        """Queue cloud-init seed directory injection."""
        self._lp.inject_cloud_init(cloud_init_dir)

    def detect_os(self) -> str:
        """Detect OS type from the rootfs via the loop-mount binary.

        Falls back to ``"linux"`` if the binary is unavailable or returns
        an error (e.g. in test environments with mocked subprocess).
        """
        from mvmctl.core._shared._loopmount import LoopMountManager
        from mvmctl.exceptions import LoopMountError

        try:
            return LoopMountManager.detect_os(
                str(self._lp._rootfs_path), self._lp._fs_type
            )
        except (LoopMountError, OSError, RuntimeError) as exc:
            logger.debug(
                "OS detection via loop-mount binary failed, "
                "falling back to 'linux': %s: %s",
                type(exc).__name__,
                exc,
            )
            return "linux"

    def deblob(self, os_type: str | None = None) -> None:
        """Queue deblob (OS cache cleanup) operations.

        If ``os_type`` is provided, it is used directly to select the
        correct deblob operations, skipping an extra ``detect_os()`` call.
        This eliminates the dual loop-mount cycle in the VM provisioner.

        Note: ``fix_fstab`` is NOT called here — it is queued separately
        by the caller (``vm_operations.py``) to avoid duplicate execution.
        """
        if os_type is None:
            os_type = self.detect_os()
        from mvmctl.core._shared._provisioner._content import (
            ProvisionerContent,
        )

        ops = ProvisionerContent.build_deblob_ops(os_type)
        self._queue_ops(ops)

    def fix_fstab(self) -> None:
        """Queue fstab fix for Firecracker (PARTUUID → /dev/vda)."""
        from mvmctl.core._shared._provisioner._content import (
            ProvisionerContent,
        )

        self._queue_ops(ProvisionerContent.build_fix_fstab_ops())

    def shrink(self) -> None:
        """Queue filesystem shrink to minimum size."""
        self.resize(0)

    def extract_partition(
        self,
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Extract root partition from a raw disk image.

        Uses sfdisk/parted for partition table parsing and dd for extraction.
        This is the LOOP_MOUNT backend's partition extraction path.
        """
        import logging
        import shutil

        from mvmctl.utils._disk import RootPartitionDetector
        from mvmctl.utils.common import CommonUtils

        _SECTOR_SIZE = 512
        log = logging.getLogger(__name__)

        try:
            # Check if the image is a direct filesystem (superfloppy) using blkid
            fs_type = self._detect_filesystem_type(raw_path)
            if fs_type in ("ext4", "ext3", "ext2", "btrfs", "xfs"):
                log.info("Image is %s filesystem, using as-is", fs_type)
                try:
                    run_cmd(
                        [
                            "cp",
                            "--sparse=always",
                            str(raw_path),
                            str(output_path),
                        ],
                    )
                except (ProcessError, FileNotFoundError):
                    self._copy_bytes_dd(raw_path, output_path, 0, None)
                ext_map = {
                    "ext4": ".ext4",
                    "ext3": ".ext4",
                    "ext2": ".ext4",
                    "btrfs": ".btrfs",
                    "xfs": ".xfs",
                }
                ext = ext_map.get(fs_type, ".img")
                final_path = output_path.with_suffix(ext)
                output_path.rename(final_path)
                return final_path

            parsed = self._parse_partitions_sfdisk(raw_path, partition)
            if parsed is None:
                parsed = self._parse_partitions_parted(raw_path, partition)

            if parsed is None:
                raise RuntimeError(
                    "Failed to parse partition table: neither sfdisk nor parted is available or succeeded"
                )

            if isinstance(parsed, _NoPartitionTable):
                log.info("No partition table found, using image as-is")
                shutil.move(str(raw_path), str(output_path))
                return output_path

            if not isinstance(parsed, tuple):
                raise RuntimeError(
                    f"Unexpected parse result type: {type(parsed).__name__}"
                )

            partitions, requested_partition = parsed

            if len(partitions) == 0:
                log.info("No partitions found, using image as-is")
                shutil.move(str(raw_path), str(output_path))
                return output_path

            # Determine which partition to extract
            if len(partitions) > 1 and requested_partition is None:
                log.info("Found %d partitions:", len(partitions))
                for i, p in enumerate(partitions, 1):
                    log.debug(
                        "  %d: start=%s size=%s type=%s",
                        i,
                        p.get("start"),
                        p.get("size"),
                        p.get("type", "?"),
                    )
                detector = RootPartitionDetector(
                    disabled_detectors=disabled_detectors
                )
                chosen_idx = detector.detect(partitions)
                log.info("Detector selected partition %d as root", chosen_idx)
                chosen = partitions[chosen_idx - 1]
                partition_num = chosen_idx
            elif requested_partition is not None:
                if requested_partition < 1 or requested_partition > len(
                    partitions
                ):
                    raise RuntimeError(
                        f"Partition {requested_partition} out of range (1-{len(partitions)})"
                    )
                log.info("Found %d partitions:", len(partitions))
                log.info("Using partition %d as root", requested_partition)
                chosen = partitions[requested_partition - 1]
                partition_num = requested_partition
            else:
                chosen = partitions[0]
                partition_num = 1

            start_sector = CommonUtils.safe_int(chosen.get("start"), 0)
            size_val = chosen.get("size")
            sector_count: int | None = (
                CommonUtils.safe_int(size_val, 0) if size_val else None
            )

            skip_bytes = start_sector * _SECTOR_SIZE
            count_bytes = sector_count * _SECTOR_SIZE if sector_count else None

            # Validate extraction is within file bounds
            raw_file_size = raw_path.stat().st_size
            if skip_bytes >= raw_file_size:
                raise RuntimeError(
                    f"Partition {partition_num} start sector ({start_sector}) "
                    f"offset ({skip_bytes} bytes) exceeds file size ({raw_file_size} bytes). "
                    f"Partition table may be corrupted or in unsupported format."
                )

            log.info(
                "Extracting partition %d (start=%d, offset=%d bytes)...",
                partition_num,
                start_sector,
                skip_bytes,
            )

            self._copy_bytes_dd(raw_path, output_path, skip_bytes, count_bytes)

            output_path = self._detect_and_rename_fs(output_path)

            log.info("Extracted to %s", output_path.name)
            return output_path

        except OSError as e:
            raise RuntimeError("Extraction failed") from e
        except (IndexError, ValueError) as e:
            raise RuntimeError("Failed to parse partition table") from e

    @staticmethod
    def _copy_bytes_dd(
        src: Path, dst: Path, skip_bytes: int, count_bytes: int | None
    ) -> None:
        """Copy bytes from *src* starting at *skip_bytes* into *dst* using dd."""
        cmd = [
            "dd",
            f"if={src}",
            f"of={dst}",
            "bs=1M",
            f"skip={skip_bytes}",
            "iflag=skip_bytes,count_bytes",
            "conv=sparse,fsync",
            "status=none",
        ]
        if count_bytes is not None:
            cmd.append(f"count={count_bytes}")
        try:
            run_cmd(cmd)
        except ProcessError as e:
            raise RuntimeError(f"dd failed: {e}") from e

    @staticmethod
    def _detect_filesystem_type(image_path: Path) -> str | None:
        """Detect filesystem type using blkid."""
        try:
            blkid_result = run_cmd(
                ["blkid", "-o", "value", "-s", "TYPE", str(image_path)],
                check=False,
            )
            fs_type = blkid_result.stdout.strip()
            return fs_type if fs_type else None
        except ProcessError:
            return None

    @staticmethod
    def _parse_partitions_sfdisk(
        raw_path: Path,
        partition: int | None,
    ) -> tuple[list[dict[str, object]], int | None] | object | None:
        """Parse partition table using sfdisk."""
        import json as json_mod

        try:
            sfdisk_result = run_cmd(
                ["sfdisk", "--json", str(raw_path)],
                check=False,
            )
            if sfdisk_result.returncode != 0:
                return _NO_PARTITION_TABLE

            table = json_mod.loads(sfdisk_result.stdout)
            partitions_raw = table.get("partitiontable", {}).get(
                "partitions", []
            )

            if not partitions_raw:
                return _NO_PARTITION_TABLE

            partitions: list[dict[str, object]] = []
            for p in partitions_raw:
                start = p.get("start")
                size = p.get("size")
                if not isinstance(start, (int, float)) or not isinstance(
                    size, (int, float)
                ):
                    raise RuntimeError("Failed to parse partition table")
                partitions.append(
                    {
                        "start": int(start),
                        "size": int(size),
                        "type": p.get("type", ""),
                        "node": p.get("node", ""),
                    }
                )

            return partitions, partition

        except (
            json_mod.JSONDecodeError,
            KeyError,
        ):
            return None

    @staticmethod
    def _parse_partitions_parted(
        raw_path: Path,
        partition: int | None,
    ) -> tuple[list[dict[str, object]], int | None] | object | None:
        """Parse partition table using parted (fallback when sfdisk unavailable)."""
        try:
            result = run_cmd(
                ["parted", "-sm", str(raw_path), "unit", "B", "print"],
                check=False,
            )
        except ProcessError:
            return None

        _SECTOR_SIZE = 512
        lines = result.stdout.strip().split("\n")
        if not lines or lines[0] != "BYT;":
            return None

        partitions: list[dict[str, object]] = []
        for line in lines[2:]:
            line = line.rstrip(";")
            if not line:
                continue
            parts = line.split(":")
            if len(parts) < 6:
                continue
            try:
                number = parts[0]
                start_bytes = int(parts[1].rstrip("B"))
                size_bytes = int(parts[3].rstrip("B"))
                filesystem = parts[4]
                part_type = parts[5]
            except (ValueError, IndexError):
                return None

            start_sector = start_bytes // _SECTOR_SIZE
            size_sector = size_bytes // _SECTOR_SIZE
            partitions.append(
                {
                    "start": start_sector,
                    "size": size_sector,
                    "type": part_type,
                    "node": number,
                    "fstype": filesystem,
                }
            )

        if not partitions:
            return _NO_PARTITION_TABLE

        return partitions, partition

    @staticmethod
    def _detect_and_rename_fs(output_path: Path) -> Path:
        """Detect filesystem type via blkid and rename output file accordingly."""
        import logging

        try:
            blkid_result = run_cmd(
                ["blkid", "-o", "value", "-s", "TYPE", str(output_path)],
                check=False,
            )
            fs_type = blkid_result.stdout.strip()
            if fs_type:
                ext_map = {
                    "ext4": ".ext4",
                    "btrfs": ".btrfs",
                    "xfs": ".xfs",
                }
                ext = ext_map.get(fs_type, ".img")
                final_path = output_path.with_suffix(ext)
                output_path.rename(final_path)
                output_path = final_path
                logging.getLogger(__name__).info(
                    "Detected filesystem: %s", fs_type
                )
        except ProcessError:
            pass
        return output_path

    def _queue_ops(self, ops: list[Any]) -> None:
        """Queue raw operation objects for execution."""
        self._lp._ops.extend(ops)

    def convert_to(self, target_fs: str) -> None:
        """Convert the image filesystem to *target_fs* via loop-mount.

        Delegates to ``LoopMountProvisioner.convert_to()`` which calls the
        ``mvm-provision`` binary directly (bypassing the regular ops flow).
        The image file is replaced in-place.
        """
        from mvmctl.exceptions import LoopMountError

        try:
            self._lp.convert_to(target_fs)
        except (LoopMountError, OSError, RuntimeError) as exc:
            logger.error("Filesystem conversion failed: %s", exc)
            raise

    def run(self) -> None:
        """Execute all queued operations."""
        self._lp.run()


# =========================================================================
# Backend: guestfs (libguestfs appliance)
# =========================================================================


class _GuestfsBackend:
    """Delegates all operations to ``GuestfsProvisioner``."""

    def __init__(
        self,
        rootfs_path: Path,
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> None:
        from mvmctl.core._shared._guestfs import GuestfsProvisioner

        self._gp: Any = GuestfsProvisioner(
            rootfs_path,
            readonly=False,
            root_uid=root_uid,
            root_gid=root_gid,
            user_uid=user_uid,
            user_gid=user_gid,
        )
        self._rootfs_path = rootfs_path

    def resize(self, target_size_bytes: int) -> None:
        """Queue a resize operation."""
        self._gp.resize(target_size_bytes)

    def set_hostname(self, hostname: str) -> None:
        """Queue hostname setup."""
        self._gp.set_hostname(hostname)

    def inject_dns(self, *, dns_server: str) -> None:
        """Queue DNS resolver injection."""
        self._gp.inject_dns(dns_server=dns_server)

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        """Queue SSH key, config, and host-key generation."""
        self._gp.setup_ssh(user, ssh_pubkeys)

    def disable_cloud_init(self) -> None:
        """Queue cloud-init datasource blocking + service masking."""
        self._gp.disable_cloud_init()

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        """Queue cloud-init seed directory injection."""
        self._gp.inject_cloud_init(cloud_init_dir)

    def detect_os(self) -> str:
        """Detect OS type from the rootfs via guestfs."""
        from mvmctl.core._shared._guestfs import OptimizedGuestfs

        try:
            og = OptimizedGuestfs(self._rootfs_path, readonly=True)
        except Exception:
            return "linux"

        with og:
            og.mount_rootfs()
            os_release_content: bytes | None = None
            if og._handle.is_file("/etc/os-release"):
                raw = og._handle.read_file("/etc/os-release")
                os_release_content = raw if isinstance(raw, bytes) else None
            if os_release_content is None and og._handle.is_file(
                "/usr/lib/os-release"
            ):
                raw = og._handle.read_file("/usr/lib/os-release")
                os_release_content = raw if isinstance(raw, bytes) else None
            if os_release_content is None:
                return "linux"

            text = os_release_content.decode("utf-8", errors="replace")
            for line in text.splitlines():
                if line.startswith("ID="):
                    return line.split("=", 1)[1].strip().strip('"')

        return "linux"

    def extract_partition(
        self,
        raw_path: Path,
        output_path: Path,
        partition: int | None = None,
        disabled_detectors: list[str] | None = None,
    ) -> Path:
        """Extract root partition from a raw disk image using guestfs."""
        from mvmctl.core._shared._guestfs import OptimizedGuestfs

        result = OptimizedGuestfs.extract_partition(
            raw_path, output_path, partition
        )
        if result is None:
            raise RuntimeError("Guestfs partition extraction failed")
        return result

    def deblob(self, os_type: str | None = None) -> None:
        """Queue deblob (OS cache cleanup + fstab fix) operation.

        Args:
            os_type: Ignored for guestfs backend (it detects OS internally).
                Accepted for interface compatibility with ``_LoopMountBackend``.

        """
        self._gp.deblob()

    def fix_fstab(self) -> None:
        """Queue fstab fix for Firecracker (PARTUUID → /dev/vda)."""
        self._gp.fix_fstab()

    def shrink(self) -> None:
        """Queue shrink-to-minimum operation."""
        self._gp.shrink()

    def convert_to(self, target_fs: str) -> None:
        """Convert the image filesystem to *target_fs* via guestfs.

        Delegates to ``GuestfsProvisioner.convert_to()`` which opens a
        fresh guestfs session, creates a new ext4 image, copies all
        files, and replaces the original.
        """
        self._gp.convert_to(target_fs)

    def run(self) -> None:
        """Execute all queued operations."""
        self._gp.run()


# =========================================================================
# Factory
# =========================================================================


class ProvisionerBackend:
    """Factory for constructing the correct backend based on ProvisionerType."""

    @staticmethod
    def _ensure_guestfs_appliance() -> None:
        """Verify the libguestfs fixed appliance cache exists.

        Raises ``MVMError`` with a helpful message if the appliance has
        not been built yet.
        """
        from mvmctl.utils.common import CacheUtils

        appliance_dir = CacheUtils.get_cache_dir() / "appliance"
        required = {"kernel", "initrd", "root"}
        if not appliance_dir.is_dir() or not required.issubset(
            {p.name for p in appliance_dir.iterdir()}
        ):
            from mvmctl.exceptions import GuestfsError

            raise GuestfsError(
                "libguestfs appliance cache not found. Run:  mvm cache init"
            )

    @staticmethod
    def get_vm(
        rootfs_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str,
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> _LoopMountBackend | _GuestfsBackend:
        """Construct a backend for VM provisioning."""
        if provisioner_type == ProvisionerType.LOOP_MOUNT:
            return _LoopMountBackend(rootfs_path, fs_type)
        elif provisioner_type == ProvisionerType.GUESTFS:
            ProvisionerBackend._ensure_guestfs_appliance()
            return _GuestfsBackend(
                rootfs_path,
                root_uid=root_uid,
                root_gid=root_gid,
                user_uid=user_uid,
                user_gid=user_gid,
            )
        else:
            raise ValueError(f"Unknown provisioner type: {provisioner_type!r}")

    @staticmethod
    def get_image(
        image_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str = "ext4",
    ) -> _LoopMountBackend | _GuestfsBackend:
        """Construct a backend for image optimization.

        ``fs_type`` is only meaningful for the LOOP_MOUNT backend (needed
        by the mount command).  Callers that know the filesystem type
        (e.g. ``ImageProvisioner``) should pass it explicitly.  Callers
        that only call ``extract_partition()`` may omit it — the default
        ``"ext4"`` is a harmless placeholder for extraction.
        """
        if provisioner_type == ProvisionerType.LOOP_MOUNT:
            return _LoopMountBackend(image_path, fs_type)
        elif provisioner_type == ProvisionerType.GUESTFS:
            ProvisionerBackend._ensure_guestfs_appliance()
            return _GuestfsBackend(image_path)
        else:
            raise ValueError(f"Unknown provisioner type: {provisioner_type!r}")
