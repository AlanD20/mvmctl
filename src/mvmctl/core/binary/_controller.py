"""Binary lifecycle operations.

This module contains the BinaryController class for managing binary lifecycle
operations like set_default, get, etc.
"""

from __future__ import annotations

from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.models.binary import BinaryItem


class BinaryController:
    """Stateful binary manager.

    Resolves binary entity in __init__ and operates on cached BinaryItem.
    """

    def __init__(
        self,
        entity: str | BinaryItem,
        repo: BinaryRepository,
    ) -> None:
        self._repo = repo

        if isinstance(entity, BinaryItem):
            self._binary = entity
        else:
            self._resolver = BinaryResolver(self._repo)
            self._binary = self._resolver.resolve(entity)

    def get(self) -> BinaryItem:
        """Return the resolved binary."""
        return self._binary

    def set_default(self) -> None:
        """Set this binary as default (clears others with same name)."""
        self._repo.set_default(
            name=self._binary.name,
            version=self._binary.version,
            path=self._binary.path,
        )

    def remove(self, *, force: bool = False) -> None:
        """Remove this binary from disk and database.

        Hard-deletes when no VMs reference the binary.
        Soft-deletes only when VMs still reference it (to preserve history).

        Args:
            force: If True, remove even if referenced by VMs.

        Raises:
            BinaryError: If binary is referenced by VMs and force is False.
        """
        from pathlib import Path

        from mvmctl.exceptions import BinaryError

        vms = self._repo.query_vms_by_binary(self._binary.id)
        has_vms = bool(vms)

        # 1. VM reference check
        if has_vms and not force:
            raise BinaryError(
                f"Binary referenced by VMs: {', '.join(v.name for v in vms)}"
            )

        # 2. Delete file from disk
        binary_path = Path(self._binary.path)
        if binary_path.exists():
            binary_path.unlink()

        # 3. Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(self._binary.id)
        else:
            self._repo.delete(self._binary.id)


__all__ = ["BinaryController"]
