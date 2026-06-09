"""Kernel controller — stateful kernel operations bound to a single instance."""

from __future__ import annotations

from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.models import KernelItem


class KernelController:
    """
    Stateful kernel controller — bound to a single kernel instance.

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
