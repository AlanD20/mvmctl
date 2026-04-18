"""Binary resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.db.models import Binary
from mvmctl.exceptions import BinaryNotFoundError

__all__ = [
    "BinaryResolver",
    "BinaryResolveResult",
]


@dataclass
class BinaryResolveResult:
    items: list[Binary]
    errors: list[str]
    exit_code: int


class BinaryResolver:
    """Resolver for firecracker binary."""

    def __init__(self, repo: BinaryRepository | None = None) -> None:
        self._repo = repo if repo is not None else BinaryRepository()

    def by_id(self, binary_id: str) -> Binary:
        """Resolve binary by ID prefix."""
        matches = self._repo.find_by_prefix(binary_id)
        if len(matches) == 0:
            raise BinaryNotFoundError(f"Binary not found: {binary_id}")
        if len(matches) > 1:
            raise BinaryNotFoundError(f"Binary ID is ambiguous: {binary_id}")
        return matches[0]

    def by_name_version(self, name: str, version: str) -> Binary:
        """Resolve binary by name and version (both required)."""
        binary = self._repo.get_by_name_and_version(name, version)
        if binary is None:
            raise BinaryNotFoundError(f"Binary not found: name={name!r}, version={version!r}")
        return binary

    def get_default(self, name: str) -> Binary | None:
        """Resolve the default binary for a given name, or None if not set."""
        return self._repo.get_default(name)

    def resolve(self, value: str) -> Binary:
        """Resolve binary by ID prefix."""
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str | list[str]]) -> BinaryResolveResult:
        """Resolve multiple binary identifiers by id or [name, version] pairs."""
        items: list[Binary] = []
        errors: list[str] = []
        seen_ids: set[str] = set()

        for identifier in identifiers:
            try:
                if isinstance(identifier, list) and len(identifier) == 2:
                    item = self.by_name_version(identifier[0], identifier[1])
                elif isinstance(identifier, str):
                    item = self.resolve(identifier)
                else:
                    raise BinaryNotFoundError(f"Invalid identifier format: {identifier}")

                if item.id not in seen_ids:
                    seen_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return BinaryResolveResult(items=items, errors=errors, exit_code=exit_code)
