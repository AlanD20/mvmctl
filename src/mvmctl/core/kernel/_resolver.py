"""Kernel resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from mvmctl.core._shared import RelationEnricher, RelationSpec, VersionResolver
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.exceptions import KernelNotFoundError
from mvmctl.models import KernelItem

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

    RELATIONS: dict[str, RelationSpec] = {
        "vm": RelationSpec(
            fk_field="id",
            resolver="vm",
            method="find_by_kernel_id",
            relation_name="vms",
            is_reverse=True,
            batch_method="by_kernel_id_batch",
        ),
    }

    def __init__(
        self,
        repo: KernelRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else KernelRepository()
        self._include = include

    def enrich(self, kernels: list[KernelItem]) -> list[KernelItem]:
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
        return self.enrich(matches)[0]

    def by_version_type(self, version: str, type: str) -> KernelItem:
        """Resolve by version and type (both required)."""
        kernel = self._repo.get_by_version_and_type(version, type)
        if kernel is None:
            raise KernelNotFoundError(
                f"Kernel not found: version={version!r}, type={type!r}"
            )
        return self.enrich([kernel])[0]

    def by_type(self, type_str: str) -> KernelItem:
        """Resolve by kernel type name."""
        kernel = self._repo.get_by_type(type_str)
        if kernel is None:
            raise KernelNotFoundError(f"Kernel not found: type={type_str!r}")
        return self.enrich([kernel])[0]

    def get_default(self) -> KernelItem | None:
        """Resolve the default kernel, or None if not set."""
        kernel = self._repo.get_default()
        if kernel is None:
            return None
        return self.enrich([kernel])[0]

    def resolve(self, value: str) -> KernelItem:
        """Resolve kernel by ID prefix, ``type:version`` (e.g. ``official:6.19.9``), or file path."""
        prefix, rest = VersionResolver.parse_selector(value)
        if prefix is not None:
            return self.by_version_type(rest, prefix)

        try:
            return self.by_id(value)
        except KernelNotFoundError:
            pass

        try:
            return self.by_type(value)
        except KernelNotFoundError:
            pass

        # Fallback: treat value as a filesystem path to a vmlinux binary.
        path = Path(value)
        if path.exists():
            return self._item_from_path(path)

        raise KernelNotFoundError(f"Kernel not found: {value!r}")

    @staticmethod
    def _item_from_path(path: Path) -> KernelItem:
        """Construct a KernelItem from an existing file path.

        The resulting item is ephemeral — it is not stored in the DB but
        carries the absolute path so the VM creation pipeline can use it.
        """
        resolved = path.expanduser().resolve()
        name = resolved.name
        now = datetime.now(UTC).isoformat()

        return KernelItem(
            id=name,
            name=name,
            base_name=name,
            version="unknown",
            arch="unknown",
            type="external",
            path=str(resolved),
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )

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

        items = self.enrich(items)

        exit_code = 1 if errors and not items else 0
        return KernelResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )


from mvmctl.core._shared import register  # noqa: E402

register("kernel", lambda: KernelResolver)
