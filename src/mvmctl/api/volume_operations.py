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
from mvmctl.core.volume._service import VolumeService
from mvmctl.exceptions import VolumeCreateError, VolumeNotFoundError
from mvmctl.models import VolumeItem, VolumeStatus
from mvmctl.models.result import BatchResult, OperationResult
from mvmctl.utils._disk import DiskUtils
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
        try:
            resolved = request.resolve()
        except VolumeCreateError as e:
            return OperationResult(
                status="error",
                code="volume.already_exists",
                message=str(e),
            )

        timestamp = datetime.now(tz=UTC).isoformat()
        volume_id = HashGenerator.volume(resolved.name, timestamp)

        volume_item = VolumeItem(
            id=volume_id,
            name=resolved.name,
            size_bytes=resolved.size_bytes,
            format=resolved.format,
            path=str(resolved.path),
            status=VolumeStatus.AVAILABLE,
            vm_id=None,
            created_at=timestamp,
            updated_at=timestamp,
        )

        VolumeService(repo).create_disk(volume_item)

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

        request = VolumeRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        results: list[OperationResult[VolumeItem]] = []

        # Surface partial-match errors from resolver (identifiers that didn't
        # match any volume, e.g. "nonexistent-volume").
        for error_msg in request.errors:
            results.append(
                OperationResult(
                    status="error",
                    code="volume.not_found",
                    message=error_msg,
                )
            )

        if not resolved.volumes and not results:
            return BatchResult(
                items=[
                    OperationResult(
                        status="error",
                        code="volume.not_found",
                        message="No volumes found matching the given identifiers",
                    )
                ]
            )
        for volume in resolved.volumes:
            try:
                if volume.status == VolumeStatus.ATTACHED and not force:
                    raise VolumeCreateError(
                        f"Volume '{volume.name}' is attached to a VM. "
                        "Use --force to remove anyway."
                    )

                VolumeService(repo).remove_disk(volume)

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

        if len(resolved.volumes) > 1:
            raise VolumeNotFoundError("Expected exactly one volume identifier")

        return resolved.volumes[0]

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

        # Resolve VM name if attached (cross-domain enrichment)
        vm_name: str | None = None
        if volume_item.vm_id:
            from mvmctl.core.vm._repository import VMRepository

            vm = VMRepository(Database()).get(volume_item.vm_id)
            vm_name = vm.name if vm is not None else None

        return {
            "id": volume_item.id,
            "name": volume_item.name,
            "size_bytes": volume_item.size_bytes,
            "format": volume_item.format,
            "path": volume_item.path,
            "status": volume_item.status,
            "vm_id": volume_item.vm_id,
            "vm_name": vm_name,
            "created_at": volume_item.created_at,
            "updated_at": volume_item.updated_at,
            "disk_info": disk_info,
        }

    @staticmethod
    def resize(inputs: VolumeCreateInput) -> OperationResult[VolumeItem]:
        """Resize a volume by name or ID prefix.

        Args:
            inputs: VolumeCreateInput containing the target identifier
                    (via ``name``) and new size.

        Returns:
            OperationResult with updated volume metadata.

        """
        db = Database()
        repo = VolumeRepository(db)

        # ── Resolve via VolumeInput + VolumeRequest pipeline ─────────────
        # The user may pass a name OR an ID prefix.  VolumeRequest handles
        # both through VolumeResolver.resolve_many().
        vol_input = VolumeInput(identifiers=[inputs.name])
        resolved_vol = VolumeRequest(inputs=vol_input, db=db).resolve()
        volume = resolved_vol.volumes[0]

        # ── Parse size directly ──────────────────────────────────────────
        # VolumeCreateRequest is NOT used here because its ensure_validate()
        # rejects existing volume names (creation-side check).  Resize
        # operates on an existing volume, so we parse size directly.
        size_bytes = DiskUtils.parse_disk_size_to_bytes(inputs.size)

        updated = VolumeService(repo).resize_disk(volume, size_bytes)

        AuditLog.log("volume.resize", changes={"name": volume.name})

        return OperationResult(
            status="success",
            code="volume.resized",
            item=updated,
            message=f"Volume '{volume.name}' resized",
        )
