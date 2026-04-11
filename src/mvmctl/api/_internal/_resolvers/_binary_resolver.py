"""Binary resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.exceptions import BinaryNotFoundError
from mvmctl.models.binary import BinaryItem

__all__ = [
    "BinaryResolver",
    "BinaryResolveResult",
]


@dataclass
class BinaryResolveResult:
    items: list[BinaryItem]
    errors: list[str]
    exit_code: int


class BinaryResolver:
    """Resolver for firecracker binary."""

    def __init__(self) -> None:
        from mvmctl.core.mvm_db import MVMDatabase

        self._db = MVMDatabase()

    def by_id(self, binary_id: str) -> BinaryItem:
        """Resolve binary by ID prefix."""
        matches = self._db.find_binaries_by_prefix(binary_id)
        if len(matches) == 0:
            raise BinaryNotFoundError(f"Binary not found: {binary_id}")
        if len(matches) > 1:
            raise BinaryNotFoundError(f"Binary ID is ambiguous: {binary_id}")
        return BinaryItem.from_db(matches[0])

    def by_name_version(self, name: str, version: str) -> BinaryItem:
        """Resolve binary by name and version (both required)."""
        binary = self._db.get_binary_by_name_and_version(name, version)
        if binary is None:
            raise BinaryNotFoundError(f"Binary not found: name={name!r}, version={version!r}")
        return BinaryItem.from_db(binary)

    def resolve(self, value: str) -> BinaryItem:
        """Resolve binary by ID prefix."""
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str | list[str]]) -> BinaryResolveResult:
        """Resolve multiple binary identifiers by id or [name, version] pairs."""
        items: list[BinaryItem] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                if isinstance(identifier, list) and len(identifier) == 2:
                    item = self.by_name_version(identifier[0], identifier[1])
                elif isinstance(identifier, str):
                    item = self.resolve(identifier)
                else:
                    raise BinaryNotFoundError(f"Invalid identifier format: {identifier}")

                if item not in items:
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return BinaryResolveResult(items=items, errors=errors, exit_code=exit_code)
