"""Binary resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._shared import RelationEnricher, RelationSpec, VersionResolver
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.exceptions import BinaryNotFoundError
from mvmctl.models import BinaryItem

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

    RELATIONS: dict[str, RelationSpec] = {
        "vm": RelationSpec(
            fk_field="id",
            resolver="vm",
            method="find_by_binary_id",
            relation_name="vms",
            is_reverse=True,
            batch_method="by_binary_id_batch",
        ),
    }

    def __init__(
        self,
        repo: BinaryRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else BinaryRepository()
        self._include = include

    def enrich(self, binaries: list[BinaryItem]) -> list[BinaryItem]:
        """Enrich binaries with relations if include is set."""
        if self._include and binaries:
            RelationEnricher().enrich(binaries, self._include, self.RELATIONS)
        return binaries

    def by_id(self, binary_id: str) -> BinaryItem:
        """Resolve binary by ID prefix."""
        matches = self._repo.find_by_prefix(binary_id)
        if len(matches) == 0:
            raise BinaryNotFoundError(f"Binary not found: {binary_id}")
        if len(matches) > 1:
            raise BinaryNotFoundError(f"Binary ID is ambiguous: {binary_id}")
        return self.enrich(matches)[0]

    def by_name_version(self, name: str, version: str) -> BinaryItem:
        """Resolve binary by name and version (both required)."""
        binary = self._repo.get_by_name_and_version(name, version)
        if binary is None:
            raise BinaryNotFoundError(
                f"Binary not found: name={name!r}, version={version!r}"
            )
        return self.enrich([binary])[0]

    def by_name_latest(self, name: str) -> BinaryItem:
        """Resolve binary by name — returns the highest local version."""
        matches = self._repo.list_by_name(name)
        if not matches:
            raise BinaryNotFoundError(f"Binary not found by name: {name}")
        if len(matches) == 1:
            return self.enrich(matches)[0]

        matches.sort(
            key=lambda b: VersionResolver.semver_key(b.version),
            reverse=True,
        )
        return self.enrich(matches)[0]

    def get_default(self, name: str) -> BinaryItem | None:
        """Resolve the default binary for a given name, or None if not set."""
        binary = self._repo.get_default(name)
        if binary is None:
            return None
        return self.enrich([binary])[0]

    def resolve(self, value: str) -> BinaryItem:
        """Resolve binary by ID prefix or name (latest version)."""
        try:
            return self.by_id(value)
        except BinaryNotFoundError:
            pass
        return self.by_name_latest(value)

    def resolve_many(
        self,
        identifiers: list[str | list[str]],
    ) -> BinaryResolveResult:
        """Resolve multiple binary identifiers by id or [name, version] pairs."""
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str | list[str]] = []
        for ident in identifiers:
            key = str(ident)
            if key not in seen_inputs:
                seen_inputs.add(key)
                unique_ids.append(ident)

        items: list[BinaryItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                if isinstance(identifier, list) and len(identifier) == 2:
                    item = self.by_name_version(identifier[0], identifier[1])
                elif isinstance(identifier, str):
                    item = self.resolve(identifier)
                else:
                    raise BinaryNotFoundError(
                        f"Invalid identifier format: {identifier}"
                    )

                if item.id not in resolved_ids:
                    resolved_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        items = self.enrich(items)

        exit_code = 1 if errors and not items else 0
        return BinaryResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )


from mvmctl.core._shared import register  # noqa: E402

register("binary", lambda: BinaryResolver)
