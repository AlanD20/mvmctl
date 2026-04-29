"""Kernel resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._internal._enrichment import RelationEnricher, RelationSpec
from mvmctl.core.kernel._repository import KernelRepository
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

    RELATIONS: dict[str, RelationSpec] = {}

    def __init__(
        self,
        repo: KernelRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else KernelRepository()
        self._include = include

    def _enrich(self, kernels: list[KernelItem]) -> list[KernelItem]:
        """Enrich kernels with relations if include is set."""
        if self._include and kernels:
            RelationEnricher().enrich(kernels, self._include, self.RELATIONS)
        return kernels

    def by_id(self, kernel_id: str) -> KernelItem:
        """Resolve by ID prefix."""
        matches = self._repo.find_by_prefix(kernel_id)
        if len(matches) == 0:
            raise KernelNotFoundError(f"Kernel not found: {kernel_id!r}")
        if len(matches) > 1:
            raise KernelNotFoundError(f"Kernel ID is ambiguous: {kernel_id!r}")
        return self._enrich(matches)[0]

    def by_version_type(self, version: str, type: str) -> KernelItem:
        """Resolve by version and type (both required)."""
        kernel = self._repo.get_by_version_and_type(version, type)
        if kernel is None:
            raise KernelNotFoundError(
                f"Kernel not found: version={version!r}, type={type!r}"
            )
        return self._enrich([kernel])[0]

    def get_default(self) -> KernelItem | None:
        """Resolve the default kernel, or None if not set."""
        kernel = self._repo.get_default()
        if kernel is None:
            return None
        return self._enrich([kernel])[0]

    def resolve(self, value: str) -> KernelItem:
        """Resolve kernel by ID prefix."""
        kernel = self.by_id(value)
        return kernel

    def resolve_many(
        self,
        identifiers: list[str | list[str]],
    ) -> KernelResolveResult:
        """Resolve multiple kernel identifiers by id or [version, type] pairs."""
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str | list[str]] = []
        for ident in identifiers:
            key = str(ident)
            if key not in seen_inputs:
                seen_inputs.add(key)
                unique_ids.append(ident)

        items: list[KernelItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                if isinstance(identifier, list) and len(identifier) == 2:
                    item = self.by_version_type(identifier[0], identifier[1])
                elif isinstance(identifier, str):
                    item = self.resolve(identifier)
                else:
                    raise KernelNotFoundError(
                        f"Invalid identifier format: {identifier}"
                    )

                if item.id not in resolved_ids:
                    resolved_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        items = self._enrich(items)

        exit_code = 1 if errors and not items else 0
        return KernelResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )


from mvmctl.core._internal._resolver_registry import register  # noqa: E402

register("kernel", lambda: KernelResolver)
