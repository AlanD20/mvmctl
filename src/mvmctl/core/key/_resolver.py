"""SSH key resolution helpers using database storage."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.core.key._repository import KeyRepository
from mvmctl.db.models import SSHKey
from mvmctl.exceptions import KeyNotFoundError, MVMKeyError

if TYPE_CHECKING:
    pass

__all__ = [
    "KeyResolver",
    "KeyResolveResult",
]


@dataclass
class KeyResolveResult:
    items: list[SSHKey]
    errors: list[str]
    exit_code: int


class KeyResolver:
    """Resolver for SSH key resources using database storage."""

    def __init__(self, repo: KeyRepository | None = None) -> None:
        self._repo = repo if repo is not None else KeyRepository()

    def by_id(self, key_id: str) -> SSHKey:
        """Resolve by ID (fingerprint) prefix."""
        matches = self._repo.find_by_prefix(key_id)
        if len(matches) == 0:
            raise KeyNotFoundError(f"Key not found: {key_id!r}")
        if len(matches) > 1:
            raise KeyNotFoundError(f"Key ID is ambiguous: {key_id!r}")
        return matches[0]

    def by_name(self, name: str) -> SSHKey:
        """Resolve by key name."""
        key = self._repo.get_by_name(name)
        if key is None:
            raise KeyNotFoundError(f"Key not found: {name!r}")
        return key

    def get_defaults(self) -> list[SSHKey]:
        """Resolve all SSH keys marked as default."""
        return self._repo.get_defaults()

    def resolve(self, value: str) -> SSHKey:
        """Resolve key by name or ID prefix."""
        try:
            return self.by_name(value)
        except KeyNotFoundError:
            pass

        try:
            return self.by_id(value)
        except KeyNotFoundError:
            pass

        candidate = Path(value)
        if candidate.exists() and candidate.suffix == ".pub":
            stem = candidate.stem
            try:
                return self.by_name(stem)
            except KeyNotFoundError:
                raise MVMKeyError(
                    f"Public key file '{value}' found on disk but key '{stem}' is not in the cache. "
                    f"Import it first with: mvm key add {stem} {value}"
                )

        raise KeyNotFoundError(
            f"Key not found: '{value}' is not a cached key name, "
            "a readable .pub file path, or a resolvable ID."
        )

    def resolve_many(self, identifiers: list[str]) -> KeyResolveResult:
        """Resolve multiple key identifiers."""
        items: list[SSHKey] = []
        errors: list[str] = []
        seen_ids: set[str] = set()

        for identifier in identifiers:
            try:
                key = self.resolve(identifier)
                if key.id not in seen_ids:
                    seen_ids.add(key.id)
                    items.append(key)
            except KeyNotFoundError as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return KeyResolveResult(items=items, errors=errors, exit_code=exit_code)
