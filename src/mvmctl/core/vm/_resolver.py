"""VM resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.core._shared import RelationEnricher, RelationSpec
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem

__all__ = [
    "VMResolver",
    "VMResolveResult",
]


@dataclass
class VMResolveResult:
    items: list[VMInstanceItem]
    errors: list[str]
    exit_code: int


class VMResolver:
    """Resolver for VM resources."""

    RELATIONS: dict[str, RelationSpec] = {
        "kernel": RelationSpec(
            fk_field="kernel_id",
            resolver="kernel",
            method="resolve",
            relation_name="kernel",
        ),
        "image": RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="resolve",
            relation_name="image",
        ),
        "binary": RelationSpec(
            fk_field="binary_id",
            resolver="binary",
            method="resolve",
            relation_name="binary",
        ),
        "network": RelationSpec(
            fk_field="network_id",
            resolver="network",
            method="resolve",
            relation_name="network",
        ),
        "network.leases": RelationSpec(
            fk_field="network",
            resolver="network_lease",
            method="list_by_network_id",
            relation_name="leases",
        ),
    }

    def __init__(
        self,
        repo: VMRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo if repo is not None else VMRepository()
        self._include = include

    def _enrich(self, vms: list[VMInstanceItem]) -> list[VMInstanceItem]:
        """Enrich VMs with relations if include is set."""
        if self._include and vms:
            RelationEnricher().enrich(vms, self._include, self.RELATIONS)
        return vms

    def by_id(self, vm_id: str) -> VMInstanceItem:
        """Resolve VM by ID prefix."""
        matches = self._repo.find_by_prefix(vm_id)
        if len(matches) == 0:
            raise VMNotFoundError(f"VM not found: {vm_id}")
        if len(matches) > 1:
            names = ", ".join(vm.name for vm in matches)
            raise VMNotFoundError(f"ID {vm_id} matches multiple VMs: {names}")
        return self._enrich(matches)[0]

    def by_name(self, name: str) -> VMInstanceItem:
        """Resolve VM by name."""
        vm = self._repo.get_by_name(name)
        if vm is None:
            raise VMNotFoundError(f"VM not found: {name}")
        return self._enrich([vm])[0]

    def by_ip(self, ip: str) -> VMInstanceItem:
        """Resolve VM by IP address via DB lookup."""
        vm = self._repo.find_by_ip(ip)
        if vm is None:
            raise VMNotFoundError(f"No VM found with IP: {ip}")
        return self._enrich([vm])[0]

    def by_mac(self, mac: str) -> VMInstanceItem:
        """Resolve VM by MAC address via DB lookup."""
        vm = self._repo.find_by_mac(mac)
        if vm is None:
            raise VMNotFoundError(f"No VM found with MAC: {mac}")
        return self._enrich([vm])[0]

    def by_image_id(self, image_id: str) -> list[VMInstanceItem]:
        """Resolve VMs by image ID."""
        vms = self._repo.get_by_image_ids([image_id])
        return self._enrich(vms)

    def by_image_id_batch(
        self, image_ids: list[str]
    ) -> dict[str, list[VMInstanceItem]]:
        """Batch-resolve VMs by image IDs. Returns dict mapping image_id -> VM list."""
        vms = self._repo.get_by_image_ids(image_ids)
        results: dict[str, list[VMInstanceItem]] = {
            img_id: [] for img_id in image_ids
        }
        for vm in vms:
            if vm.image_id in results:
                results[vm.image_id].append(vm)
        return results

    def resolve(self, identifier: str) -> VMInstanceItem:
        """Resolve VM by name, ip, mac, or id prefix."""
        try:
            vm = self.by_name(identifier)
        except VMNotFoundError:
            pass
        else:
            return vm
        if "." in identifier:
            return self.by_ip(identifier)
        if ":" in identifier:
            return self.by_mac(identifier)
        return self.by_id(identifier)

    def resolve_many(self, identifiers: list[str]) -> VMResolveResult:
        """Resolve multiple VM identifiers by name, ip, mac, or id."""
        # Deduplicate identifiers while preserving order
        seen_inputs: set[str] = set()
        unique_ids: list[str] = []
        for ident in identifiers:
            if ident not in seen_inputs:
                seen_inputs.add(ident)
                unique_ids.append(ident)

        items: list[VMInstanceItem] = []
        errors: list[str] = []
        resolved_vm_ids: set[str] = set()

        for identifier in unique_ids:
            try:
                item = self.resolve(identifier)
                if item.id not in resolved_vm_ids:
                    resolved_vm_ids.add(item.id)
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        items = self._enrich(items)

        exit_code = 1 if errors and not items else 0
        return VMResolveResult(items=items, errors=errors, exit_code=exit_code)


from mvmctl.core._shared import register  # noqa: E402

register("vm", lambda: VMResolver)
