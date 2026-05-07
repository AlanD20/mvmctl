"""Volume processing service - handles disk creation, removal, and inspection."""

from __future__ import annotations

import json
import logging
import subprocess
from pathlib import Path
from typing import Any

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.exceptions import VolumeCreateError

logger = logging.getLogger(__name__)


class VolumeService:
    """
    Stateless disk operations for volume management.

    Args:
        repo: VolumeRepository for DB operations. Must be provided.

    """

    def __init__(self, repo: VolumeRepository) -> None:
        self._repo = repo

    def create_disk(
        self, path: Path, size_bytes: int, format: str = "raw"
    ) -> None:
        """Create a disk file at the specified path.

        Args:
            path: Path where the disk file should be created.
            size_bytes: Size of the disk in bytes.
            format: Disk format, either "raw" or "qcow2". Defaults to "raw".

        Raises:
            VolumeCreateError: If the disk creation fails.

        """
        path.parent.mkdir(parents=True, exist_ok=True)

        if format == "raw":
            try:
                subprocess.run(
                    ["fallocate", "-l", str(size_bytes), str(path)],
                    check=True,
                    capture_output=True,
                )
            except subprocess.CalledProcessError as e:
                stderr = e.stderr.decode() if e.stderr else "no details"
                raise VolumeCreateError(f"fallocate failed: {stderr}") from e
            except FileNotFoundError as e:
                raise VolumeCreateError(
                    "fallocate not found. Install util-linux."
                ) from e
        elif format == "qcow2":
            try:
                subprocess.run(
                    [
                        "qemu-img",
                        "create",
                        "-f",
                        "qcow2",
                        str(path),
                        str(size_bytes),
                    ],
                    check=True,
                    capture_output=True,
                )
            except subprocess.CalledProcessError as e:
                stderr = e.stderr.decode() if e.stderr else "no details"
                raise VolumeCreateError(
                    f"qemu-img create failed: {stderr}"
                ) from e
            except FileNotFoundError as e:
                raise VolumeCreateError(
                    "qemu-img not found. Install qemu-utils."
                ) from e
        else:
            raise VolumeCreateError(f"Unsupported format: {format}")

    def remove_disk(self, path: Path) -> None:
        """Remove a disk file from disk.

        Args:
            path: Path to the disk file to remove.

        """
        if path.exists():
            path.unlink(missing_ok=True)

    def resize_disk(
        self, path: Path, size_bytes: int, format: str = "raw"
    ) -> None:
        """Resize a disk file.

        For raw format, uses fallocate (grow only).
        For qcow2 format, uses qemu-img resize.

        Args:
            path: Path to the disk file to resize.
            size_bytes: New size of the disk in bytes.
            format: Disk format, either "raw" or "qcow2". Defaults to "raw".

        Raises:
            VolumeCreateError: If the resize fails.

        """
        if not path.exists():
            raise VolumeCreateError(f"Disk file not found: {path}")

        if format == "raw":
            try:
                subprocess.run(
                    ["fallocate", "-l", str(size_bytes), str(path)],
                    check=True,
                    capture_output=True,
                )
            except subprocess.CalledProcessError as e:
                stderr = e.stderr.decode() if e.stderr else "no details"
                raise VolumeCreateError(
                    f"fallocate resize failed: {stderr}"
                ) from e
            except FileNotFoundError as e:
                raise VolumeCreateError(
                    "fallocate not found. Install util-linux."
                ) from e
        elif format == "qcow2":
            try:
                subprocess.run(
                    [
                        "qemu-img",
                        "resize",
                        str(path),
                        str(size_bytes),
                    ],
                    check=True,
                    capture_output=True,
                )
            except subprocess.CalledProcessError as e:
                stderr = e.stderr.decode() if e.stderr else "no details"
                raise VolumeCreateError(
                    f"qemu-img resize failed: {stderr}"
                ) from e
            except FileNotFoundError as e:
                raise VolumeCreateError(
                    "qemu-img not found. Install qemu-utils."
                ) from e
        else:
            raise VolumeCreateError(f"Unsupported format: {format}")

    def get_disk_info(self, path: Path) -> dict[str, Any]:
        """Get disk information using qemu-img info.

        Args:
            path: Path to the disk file.

        Returns:
            Dictionary with disk information parsed from qemu-img JSON output.

        Raises:
            VolumeCreateError: If qemu-img is not found or fails.

        """
        if not path.exists():
            raise VolumeCreateError(f"Disk file not found: {path}")

        try:
            result = subprocess.run(
                ["qemu-img", "info", "--output=json", str(path)],
                check=True,
                capture_output=True,
                text=True,
            )
        except subprocess.CalledProcessError as e:
            stderr = e.stderr.strip() if e.stderr else "no details"
            raise VolumeCreateError(f"qemu-img info failed: {stderr}") from e
        except FileNotFoundError as e:
            raise VolumeCreateError(
                "qemu-img not found. Install qemu-utils."
            ) from e

        data: dict[str, Any] = json.loads(result.stdout)
        return data
