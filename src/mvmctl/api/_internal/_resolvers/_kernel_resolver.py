"""Kernel resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import KernelNotFoundError
from mvmctl.models.kernel import KernelItem

__all__ = [
    "KernelResolver",
    "KernelResolveResult",
]


@dataclass
class KernelResolveResult:
    items: list[KernelItem]
    errors: list[str]
    exit_code: int


class KernelResolver:
    """Resolver for kernel resources."""

    def __init__(self) -> None:
        self._db = MVMDatabase()

    def by_id(self, kernel_id: str) -> KernelItem:
        """Resolve by ID prefix."""
        matches = self._db.find_kernels_by_prefix(kernel_id)
        if len(matches) == 0:
            raise KernelNotFoundError(f"Kernel not found: {kernel_id!r}")
        if len(matches) > 1:
            raise KernelNotFoundError(f"Kernel ID is ambiguous: {kernel_id!r}")
        return KernelItem.from_db(matches[0])

    def by_version_type(self, version: str, type: str) -> KernelItem:
        """Resolve by version and type (both required)."""
        kernel = self._db.get_kernel_by_version_and_type(version, type)
        if kernel is None:
            raise KernelNotFoundError(f"Kernel not found: version={version!r}, type={type!r}")
        return KernelItem.from_db(kernel)

    def resolve(self, value: str) -> KernelItem:
        """Resolve kernel by ID prefix."""
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str | list[str]]) -> KernelResolveResult:
        """Resolve multiple kernel identifiers by id or [version, type] pairs."""
        items: list[KernelItem] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                if isinstance(identifier, list) and len(identifier) == 2:
                    item = self.by_version_type(identifier[0], identifier[1])
                elif isinstance(identifier, str):
                    item = self.resolve(identifier)
                else:
                    raise KernelNotFoundError(f"Invalid identifier format: {identifier}")

                if item not in items:
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return KernelResolveResult(items=items, errors=errors, exit_code=exit_code)
