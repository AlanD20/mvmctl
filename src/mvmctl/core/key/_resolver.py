"""SSH key resolution helpers using database storage."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.core._internal._enrichment import RelationEnricher, RelationSpec
from mvmctl.core.key._repository import KeyRepository
from mvmctl.exceptions import KeyNotFoundError, MVMKeyError
from mvmctl.models.key import SSHKeyItem

if TYPE_CHECKING:
    pass

__all__ = [
    "KeyResolver",
    "KeyResolveResult",
]


@dataclass
class KeyResolveResult:
    items: list[SSHKeyItem]
    errors: list[str]
    exit_code: int


class KeyResolver:
    """Resolver for SSH key resources using database storage."""

    RELATIONS: dict[str, RelationSpec] = {}

    def __init__(
        self,
        repo: KeyRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else KeyRepository()
        self._include = include

    def _enrich(self, keys: list[SSHKeyItem]) -> list[SSHKeyItem]:
        """Enrich keys with relations if include is set."""
        if self._include and keys:
            RelationEnricher().enrich(
                keys, self._include, self.RELATIONS
            )
        return keys

    def by_id(self, key_id: str) -> SSHKeyItem:
        """Resolve by ID (fingerprint) prefix.

        Accepts both full ID with prefix (``SHA256:abc...``) and bare
        fingerprint (``abc...``) by automatically prepending ``SHA256:``
        when the prefix is missing.
        """
        candidates = [key_id]
        if not key_id.startswith("SHA256:"):
            candidates.append(f"SHA256:{key_id}")

        for candidate in candidates:
            matches = self._repo.find_by_prefix(candidate)
            if len(matches) == 1:
                return self._enrich(matches)[0]
            if len(matches) > 1:
                raise KeyNotFoundError(f"Key ID is ambiguous: {key_id!r}")

        raise KeyNotFoundError(f"Key not found: {key_id!r}")

    def by_name(self, name: str) -> SSHKeyItem:
        """Resolve by key name."""
        key = self._repo.get_by_name(name)
        if key is None:
            raise KeyNotFoundError(f"Key not found: {name!r}")
        return self._enrich([key])[0]

    def get_defaults(self) -> list[SSHKeyItem]:
        """Resolve all SSH keys marked as default."""
        keys = self._repo.get_defaults()
        return self._enrich(keys)

    def resolve(self, value: str) -> SSHKeyItem:
        """Resolve key by name or ID prefix."""
        try:
            key = self.by_name(value)
        except KeyNotFoundError:
            pass
        else:
            return key

        try:
            key = self.by_id(value)
        except KeyNotFoundError:
            pass
        else:
            return key

        candidate = Path(value)
        if candidate.exists() and candidate.suffix == ".pub":
            stem = candidate.stem
            try:
                key = self.by_name(stem)
                return key
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
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str] = []
        for ident in identifiers:
            if ident not in seen_inputs:
                seen_inputs.add(ident)
                unique_ids.append(ident)

        items: list[SSHKeyItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                key = self.resolve(identifier)
                if key.id not in resolved_ids:
                    resolved_ids.add(key.id)
                    items.append(key)
            except KeyNotFoundError as e:
                errors.append(f"{identifier}: {e}")

        items = self._enrich(items)

        exit_code = 1 if errors and not items else 0
        return KeyResolveResult(items=items, errors=errors, exit_code=exit_code)


from mvmctl.core._internal._resolver_registry import register  # noqa: E402

register("key", lambda: KeyResolver)
