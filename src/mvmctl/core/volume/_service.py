"""Volume processing service - handles disk creation, removal, and inspection."""

from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Any

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.exceptions import ProcessError, VolumeError
from mvmctl.models import DriveConfig, VolumeItem, VolumeStatus
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)


class VolumeService:
    """
    Stateless disk operations for volume management.

    Args:
        repo: VolumeRepository for DB operations. Must be provided.

    """

    def __init__(self, repo: VolumeRepository) -> None:
        self._repo = repo
        self._resolver = VolumeResolver(self._repo)

    def create_disk(self, volume: VolumeItem) -> VolumeItem:
        """Create a disk file and persist the volume record.

        Args:
            volume: VolumeItem with id, name, size_bytes, format, path, and status.

        Returns:
            The persisted VolumeItem.

        Raises:
            VolumeError: If the disk creation fails.

        """
        disk_path = Path(volume.path)
        disk_path.parent.mkdir(parents=True, exist_ok=True)

        if volume.format == "raw":
            try:
                run_cmd(
                    ["fallocate", "-l", str(volume.size_bytes), str(disk_path)],
                )
            except ProcessError as e:
                raise VolumeError(f"fallocate failed: {e}") from e
        elif volume.format == "qcow2":
            try:
                run_cmd(
                    [
                        "qemu-img",
                        "create",
                        "-f",
                        "qcow2",
                        str(disk_path),
                        str(volume.size_bytes),
                    ],
                )
            except ProcessError as e:
                raise VolumeError(f"qemu-img create failed: {e}") from e
        else:
            raise VolumeError(f"Unsupported format: {volume.format}")

        self._repo.upsert(volume)
        return volume

    def remove(self, volume: VolumeItem) -> None:
        """Remove a disk file and its DB record.

        Args:
            volume: VolumeItem to remove (file and DB record).

        """
        self._repo.delete(volume.id)
        disk_path = Path(volume.path)
        if disk_path.exists():
            disk_path.unlink(missing_ok=True)

    def resize_disk(
        self, volume: VolumeItem, new_size_bytes: int
    ) -> VolumeItem:
        """Resize a disk file and update the DB record.

        For raw format, uses fallocate (grow only).
        For qcow2 format, uses qemu-img resize.

        Args:
            volume: VolumeItem to resize.
            new_size_bytes: New size of the disk in bytes.

        Returns:
            The updated VolumeItem with new size and timestamp.

        Raises:
            VolumeError: If the resize fails.

        """
        from datetime import UTC, datetime

        disk_path = Path(volume.path)
        if not disk_path.exists():
            raise VolumeError(f"Disk file not found: {disk_path}")

        if volume.format == "raw":
            try:
                run_cmd(
                    ["fallocate", "-l", str(new_size_bytes), str(disk_path)],
                )
            except ProcessError as e:
                raise VolumeError(f"fallocate resize failed: {e}") from e
        elif volume.format == "qcow2":
            try:
                run_cmd(
                    [
                        "qemu-img",
                        "resize",
                        str(disk_path),
                        str(new_size_bytes),
                    ],
                )
            except ProcessError as e:
                raise VolumeError(f"qemu-img resize failed: {e}") from e
        else:
            raise VolumeError(f"Unsupported format: {volume.format}")

        # Update and persist DB record
        volume.size_bytes = new_size_bytes
        volume.updated_at = datetime.now(tz=UTC).isoformat()
        self._repo.upsert(volume)
        return volume

    @staticmethod
    def get_disk_info(path: Path) -> dict[str, Any]:
        """Get disk information using qemu-img info.

        Args:
            path: Path to the disk file.

        Returns:
            Dictionary with disk information parsed from qemu-img JSON output.

        Raises:
            VolumeError: If qemu-img is not found or fails.

        """
        if not path.exists():
            raise VolumeError(f"Disk file not found: {path}")

        try:
            result = run_cmd(
                ["qemu-img", "info", "--output=json", str(path)],
            )
        except ProcessError as e:
            raise VolumeError(f"qemu-img info failed: {e}") from e

        data: dict[str, Any] = json.loads(result.stdout)
        return data

    @staticmethod
    def volumes_to_drives(volumes: list[VolumeItem]) -> list[DriveConfig]:
        """Convert VolumeItems to Firecracker drive configurations.

        Validates each volume is available and builds the drive config
        dicts needed by the Firecracker boot config.

        Args:
            volumes: Pre-resolved VolumeItem objects.

        Returns:
            List of drive config dicts (drive_id, path_on_host, etc.).

        Raises:
            VolumeError: If any volume is not available.

        """
        drives: list[DriveConfig] = []
        for vol in volumes:
            if vol.status not in (
                VolumeStatus.AVAILABLE,
                VolumeStatus.ATTACHED,
            ):
                raise VolumeError(
                    f"Volume '{vol.name}' is not available "
                    f"(status: {vol.status})"
                )
            drives.append(
                {
                    "drive_id": vol.id,
                    "path_on_host": vol.path,
                    "is_root_device": False,
                    "is_read_only": False,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                }
            )
        return drives

    def set_volumes_state(
        self,
        volumes: list[VolumeItem],
        state: VolumeStatus,
        vm_id: str | None = None,
    ) -> None:
        """Set all volumes in the given list to the target state.

        For ``ATTACHED`` state, requires ``vm_id`` and calls
        ``VolumeController.attach()`` on each volume. Skips volumes that
        are already attached (idempotent).

        For ``AVAILABLE`` state (detach), calls ``VolumeController.detach()``
        on each volume that is currently attached. Skips already-detached
        volumes (idempotent).

        Args:
            volumes: Pre-resolved VolumeItem objects.
            state: Target state (VolumeStatus.ATTACHED or VolumeStatus.AVAILABLE).
            vm_id: Required when state is ATTACHED. The VM to attach to.

        Raises:
            ValueError: If state is ATTACHED and vm_id is None.

        """
        if not volumes:
            return
        from mvmctl.core.volume._controller import VolumeController

        if state == VolumeStatus.ATTACHED:
            if vm_id is None:
                raise ValueError("vm_id is required when state is ATTACHED")
            for vol in volumes:
                try:
                    controller = VolumeController(vol, self._repo)
                    controller.attach(vm_id)
                except Exception as exc:
                    logger.warning(
                        "Failed to attach volume '%s': %s",
                        vol.name,
                        exc,
                    )
        elif state == VolumeStatus.AVAILABLE:
            for vol in volumes:
                try:
                    if vol.status == VolumeStatus.ATTACHED.value:
                        controller = VolumeController(vol, self._repo)
                        controller.detach()
                except Exception as exc:
                    logger.warning(
                        "Failed to detach volume '%s': %s", vol.name, exc
                    )
