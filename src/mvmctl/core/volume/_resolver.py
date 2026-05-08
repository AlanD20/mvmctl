"""Volume resolution helpers."""

from __future__ import annotations

import json
from dataclasses import dataclass

from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.exceptions import VolumeNotFoundError
from mvmctl.models import VolumeItem

__all__ = [
    "VolumeResolver",
    "VolumeResolveResult",
]


@dataclass
class VolumeResolveResult:
    items: list[VolumeItem]
    errors: list[str]
    exit_code: int


class VolumeResolver:
    """Resolver for volume resources."""

    def __init__(
        self,
        repo: VolumeRepository | None = None,
    ) -> None:
        self._repo = repo if repo is not None else VolumeRepository()

    def by_id(self, volume_id: str) -> VolumeItem:
        """Resolve by full ID or prefix."""
        matches = self._repo.find_by_prefix(volume_id)
        if len(matches) == 0:
            raise VolumeNotFoundError(f"Volume not found: {volume_id!r}")
        if len(matches) > 1:
            raise VolumeNotFoundError(f"Volume ID is ambiguous: {volume_id!r}")
        return matches[0]

    def by_name(self, name: str) -> VolumeItem:
        """Resolve by name."""
        db_volume = self._repo.get_by_name(name)
        if db_volume is None:
            raise VolumeNotFoundError(f"Volume not found by name: {name!r}")
        return db_volume

    def resolve(self, value: str) -> VolumeItem:
        """Resolve volume by name, then by ID prefix."""
        try:
            return self.by_name(value)
        except VolumeNotFoundError:
            return self.by_id(value)

    def resolve_many(self, identifiers: list[str]) -> VolumeResolveResult:
        """Resolve multiple volume identifiers by name or id."""
        seen_inputs: set[str] = set()
        unique_ids: list[str] = []
        for ident in identifiers:
            if ident not in seen_inputs:
                seen_inputs.add(ident)
                unique_ids.append(ident)

        items: list[VolumeItem] = []
        errors: list[str] = []
        resolved_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                item = self.resolve(identifier)
                if item.id not in resolved_ids:
                    resolved_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return VolumeResolveResult(
            items=items, errors=errors, exit_code=exit_code
        )

    def resolve_by_ids(self, volume_ids: list[str]) -> dict[str, VolumeItem]:
        """Batch-resolve volume IDs to VolumeItems.

        Args:
            volume_ids: List of full 64-char volume IDs.

        Returns:
            Dict mapping each found volume ID to its VolumeItem.

        """
        return {vol.id: vol for vol in self._repo.find_by_ids(volume_ids)}

    def resolve_by_vm_volume_ids(
        self, json_ids_list: list[str]
    ) -> dict[str, list[VolumeItem]]:
        """Resolve volumes from VM ``volume_ids`` JSON strings.

        Designed as a batch_method for the ``volumes`` relation enrichment
        in ``VMResolver.RELATIONS``. Takes a list of JSON arrays (one per
        VM) and returns a dict mapping each JSON string to its resolved
        ``VolumeItem`` list.

        Args:
            json_ids_list: List of JSON strings, each a list of volume IDs.
                Example: ``['["id1","id2"]', '["id3"]']``

        Returns:
            Dict mapping each input JSON string to its list of VolumeItems.

        """
        all_ids: set[str] = set()
        for json_str in json_ids_list:
            try:
                parsed = json.loads(json_str)
                if isinstance(parsed, list):
                    all_ids.update(parsed)
            except (json.JSONDecodeError, TypeError):
                continue

        resolved = self.resolve_by_ids(list(all_ids))

        result: dict[str, list[VolumeItem]] = {}
        for json_str in json_ids_list:
            vols: list[VolumeItem] = []
            try:
                parsed = json.loads(json_str)
                if isinstance(parsed, list):
                    for vid in parsed:
                        if vid in resolved:
                            vols.append(resolved[vid])
            except (json.JSONDecodeError, TypeError):
                pass
            result[json_str] = vols

        return result


from mvmctl.core._shared import register  # noqa: E402

register("volume", lambda: VolumeResolver)
