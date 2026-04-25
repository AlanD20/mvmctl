"""Kernel controller — stateful kernel operations bound to a single instance."""

from __future__ import annotations

from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.exceptions import KernelError
from mvmctl.models.kernel import KernelItem


class KernelController:
    """Stateful kernel controller — bound to a single kernel instance.

    Args:
        entity: KernelItem or string identifier (ID prefix) to resolve.
        repo: KernelRepository for DB operations.
    """

    def __init__(
        self, entity: str | KernelItem, repo: KernelRepository
    ) -> None:
        self._repo = repo
        if isinstance(entity, KernelItem):
            self._kernel = entity
        else:
            resolver = KernelResolver(repo)
            self._kernel = resolver.resolve(entity)

    def get(self) -> KernelItem:
        """Return the bound kernel item."""
        return self._kernel

    def set_default(self) -> None:
        """Set this kernel as the default."""
        self._repo.set_default(self._kernel.id)

    def remove(self, *, force: bool = False) -> None:
        """Remove this kernel from disk and database.

        Hard-deletes when no VMs reference the kernel.
        Soft-deletes only when VMs still reference it (to preserve history).

        Args:
            force: If True, remove even if referenced by VMs.

        Raises:
            KernelError: If kernel is referenced by VMs and force is False.
        """

        vms = self._repo.query_vms_by_kernel(self._kernel.id)
        has_vms = bool(vms)

        # 1. VM reference check
        if has_vms and not force:
            raise KernelError(
                f"Kernel referenced by VMs: {', '.join(v.name for v in vms)}"
            )

        # 2. Delete file from disk
        kernel_path = self._kernel.resolved_path
        if kernel_path.exists():
            kernel_path.unlink()

        # 3. Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(self._kernel.id)
        else:
            self._repo.delete(self._kernel.id)
