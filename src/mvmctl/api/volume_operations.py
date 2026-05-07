"""Volume operations - cross-domain orchestration for volume management."""

from __future__ import annotations

import logging
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._volume_create_input import (
    VolumeCreateInput,
    VolumeCreateRequest,
)
from mvmctl.api.inputs._volume_input import VolumeInput, VolumeRequest
from mvmctl.core._shared import Database
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.core.volume._service import VolumeService
from mvmctl.exceptions import VolumeCreateError, VolumeNotFoundError
from mvmctl.models import VolumeItem
from mvmctl.models.result import BatchResult, OperationResult
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.crypto import HashGenerator

logger = logging.getLogger(__name__)

__all__ = ["VolumeOperation"]


class VolumeOperation:
    """
    Orchestration layer for volume operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def create(inputs: VolumeCreateInput) -> OperationResult[VolumeItem]:
        """Create a new volume.

        Args:
            inputs: VolumeCreateInput containing name, size, and format.

        Returns:
            OperationResult with volume metadata on success.

        """
        db = Database()
        repo = VolumeRepository(db)

        request = VolumeCreateRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        # Check for existing volume with same name
        existing = repo.get_by_name(resolved.name)
        if existing is not None:
            return OperationResult(
                status="error",
                code="volume.already_exists",
                message=f"Volume '{resolved.name}' already exists",
            )

        service = VolumeService(repo)
        service.create_disk(resolved.path, resolved.size_bytes, resolved.format)

        timestamp = datetime.now(tz=UTC).isoformat()
        volume_id = HashGenerator.volume(resolved.name, timestamp)

        volume_item = VolumeItem(
            id=volume_id,
            name=resolved.name,
            size_bytes=resolved.size_bytes,
            format=resolved.format,
            path=str(resolved.path),
            status="available",
            vm_id=None,
            created_at=timestamp,
            updated_at=timestamp,
        )

        repo.upsert(volume_item)

        AuditLog.log("volume.create", changes={"name": resolved.name})

        return OperationResult(
            status="success",
            code="volume.created",
            item=volume_item,
            message=f"Volume '{resolved.name}' created",
        )

    @staticmethod
    def remove(
        inputs: VolumeInput, force: bool = False
    ) -> BatchResult[VolumeItem]:
        """Remove volumes by name or ID prefix.

        Args:
            inputs: VolumeInput with name or id identifiers.
            force: If True, remove even if attached to VMs.

        Returns:
            BatchResult with per-item results.

        """
        db = Database()
        repo = VolumeRepository(db)

        resolved = VolumeRequest(inputs=inputs, db=db).resolve()

        results: list[OperationResult[VolumeItem]] = []
        for volume in resolved.items:
            try:
                if volume.status == "attached" and not force:
                    raise VolumeCreateError(
                        f"Volume '{volume.name}' is attached to a VM. "
                        "Use --force to remove anyway."
                    )

                service = VolumeService(repo)
                service.remove_disk(Path(volume.path))
                repo.delete(volume.id)

                AuditLog.log("volume.remove", changes={"name": volume.name})

                results.append(
                    OperationResult(
                        status="success",
                        code="volume.removed",
                        item=volume,
                        message=f"Volume '{volume.name}' removed",
                    )
                )
            except Exception as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="volume.remove_failed",
                        message=str(e),
                        item=volume,
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def list_() -> list[VolumeItem]:
        """List all volumes.

        Returns:
            List of VolumeItem records from DB.

        """
        db = Database()
        repo = VolumeRepository(db)
        return repo.list_all()

    @staticmethod
    def get(inputs: VolumeInput) -> VolumeItem:
        """Get a single volume by identifier.

        Args:
            inputs: VolumeInput with name or id identifiers.

        Returns:
            The resolved VolumeItem.

        Raises:
            VolumeNotFoundError: If volume not found or ambiguous.

        """
        resolved = VolumeRequest(inputs=inputs, db=Database()).resolve()

        if len(resolved.items) > 1:
            raise VolumeNotFoundError("Expected exactly one volume identifier")

        return resolved.items[0]

    @staticmethod
    def inspect(inputs: VolumeInput) -> dict[str, Any]:
        """Inspect a volume with disk info.

        Args:
            inputs: VolumeInput with name or id identifiers.

        Returns:
            Dictionary with volume metadata and disk information.

        """
        volume_item = VolumeOperation.get(inputs)
        service = VolumeService(VolumeRepository(Database()))
        disk_info = service.get_disk_info(Path(volume_item.path))

        return {
            "id": volume_item.id,
            "name": volume_item.name,
            "size_bytes": volume_item.size_bytes,
            "format": volume_item.format,
            "path": volume_item.path,
            "status": volume_item.status,
            "vm_id": volume_item.vm_id,
            "created_at": volume_item.created_at,
            "updated_at": volume_item.updated_at,
            "disk_info": disk_info,
        }

    @staticmethod
    def resize(inputs: VolumeCreateInput) -> OperationResult[VolumeItem]:
        """Resize a volume by name.

        Args:
            inputs: VolumeCreateInput containing name and new size.

        Returns:
            OperationResult with updated volume metadata.

        """
        db = Database()
        repo = VolumeRepository(db)

        # Resolve volume by name
        volume = VolumeResolver(repo).by_name(inputs.name)

        request = VolumeCreateRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = VolumeService(repo)
        service.resize_disk(
            Path(volume.path), resolved.size_bytes, volume.format
        )

        updated = VolumeItem(
            id=volume.id,
            name=volume.name,
            size_bytes=resolved.size_bytes,
            format=volume.format,
            path=volume.path,
            status=volume.status,
            vm_id=volume.vm_id,
            created_at=volume.created_at,
            updated_at=datetime.now(tz=UTC).isoformat(),
        )

        repo.upsert(updated)

        AuditLog.log("volume.resize", changes={"name": inputs.name})

        return OperationResult(
            status="success",
            code="volume.resized",
            item=updated,
            message=f"Volume '{inputs.name}' resized",
        )
