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


__all__ = ["BinaryController"]
