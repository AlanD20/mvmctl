"""Volume management controller."""

from __future__ import annotations

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.models import VolumeItem, VolumeStatus


class VolumeController:
    """
    Manages volume operations for a specific volume.

    Args:
        entity: Volume name, ID prefix, or VolumeItem instance.
        repo: VolumeRepository for DB operations.

    Raises:
        VolumeNotFoundError: If the volume cannot be resolved.

    """

    def __init__(
        self, entity: str | VolumeItem, repo: VolumeRepository
    ) -> None:
        self._repo = repo

        if isinstance(entity, VolumeItem):
            self._volume = entity
        else:
            self._resolver = VolumeResolver(self._repo)
            self._volume = self._resolver.resolve(entity)

    def get(self) -> VolumeItem:
        """Return the resolved VolumeItem."""
        return self._volume

    def attach(self, vm_id: str) -> None:
        """Attach volume to a VM.

        Updates DB status to "attached" and sets vm_id.

        Args:
            vm_id: ID of the VM to attach the volume to.

        """
        updated = VolumeItem(
            id=self._volume.id,
            name=self._volume.name,
            size_bytes=self._volume.size_bytes,
            format=self._volume.format,
            path=self._volume.path,
            status=VolumeStatus.ATTACHED,
            vm_id=vm_id,
            created_at=self._volume.created_at,
            updated_at=self._volume.updated_at,
        )
        self._repo.upsert(updated)
        self._volume = updated

    def detach(self) -> None:
        """Detach volume from any VM.

        Updates DB status to "available" and clears vm_id.

        """
        updated = VolumeItem(
            id=self._volume.id,
            name=self._volume.name,
            size_bytes=self._volume.size_bytes,
            format=self._volume.format,
            path=self._volume.path,
            status=VolumeStatus.AVAILABLE,
            vm_id=None,
            created_at=self._volume.created_at,
            updated_at=self._volume.updated_at,
        )
        self._repo.upsert(updated)
        self._volume = updated


__all__ = [
    "VolumeController",
]
