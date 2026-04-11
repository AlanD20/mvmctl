"""VM resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.db.models import VMInstance
from mvmctl.exceptions import VMNotFoundError

__all__ = [
    "VMResolver",
    "VMResolveResult",
]


@dataclass
class VMResolveResult:
    items: list[VMInstance]
    errors: list[str]
    exit_code: int


class VMResolver:
    """Resolver for VM resources."""

    def __init__(self) -> None:
        from mvmctl.core.mvm_db import MVMDatabase

        self._db = MVMDatabase()

    def by_id(self, vm_id: str) -> VMInstance:
        """Resolve VM by ID prefix."""
        matches = self._db.find_vms_by_prefix(vm_id)
        if len(matches) == 0:
            raise VMNotFoundError(f"VM not found: {vm_id}")
        if len(matches) > 1:
            names = ", ".join(vm.name for vm in matches)
            raise VMNotFoundError(f"ID {vm_id} matches multiple VMs: {names}")
        return matches[0]

    def by_name(self, name: str) -> VMInstance:
        """Resolve VM by name."""
        vm = self._db.find_vm_by_name(name)
        if vm is None:
            raise VMNotFoundError(f"VM not found: {name}")
        return vm

    def by_ip(self, ip: str) -> VMInstance:
        """Resolve VM by IP address via DB lookup."""
        vm = self._db.find_vm_by_ip(ip)
        if vm is None:
            raise VMNotFoundError(f"No VM found with IP: {ip}")
        return vm

    def by_mac(self, mac: str) -> VMInstance:
        """Resolve VM by MAC address via DB lookup."""
        vm = self._db.find_vm_by_mac(mac)
        if vm is None:
            raise VMNotFoundError(f"No VM found with MAC: {mac}")
        return vm

    def resolve(self, value: str) -> VMInstance:
        """Resolve VM by name, ip, mac, or id prefix."""
        try:
            return self.by_name(value)
        except VMNotFoundError:
            pass
        if "." in value:
            return self.by_ip(value)
        if ":" in value:
            return self.by_mac(value)
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str]) -> VMResolveResult:
        """Resolve multiple VM identifiers by name, ip, mac, or id."""
        items: list[VMInstance] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                item = self.resolve(identifier)
                if item not in items:
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return VMResolveResult(items=items, errors=errors, exit_code=exit_code)
