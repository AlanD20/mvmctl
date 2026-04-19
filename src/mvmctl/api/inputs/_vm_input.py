"""Generic VM request resolver for VM operations (start, stop, remove, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from mvmctl.core._internal._db import Database
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.exceptions import VMNotFoundError, VMRequestError
from mvmctl.models.vm import VMInstanceItem
from mvmctl.utils.network_validator import NetworkValidator
from mvmctl.utils.validation import validate_mac

if TYPE_CHECKING:
    pass

__all__ = ["VMInput", "VMRequest", "ResolvedVMInput"]


@dataclass
class VMInput:
    id: list[str] = field(default_factory=list)
    name: list[str] = field(default_factory=list)
    guest_mac: list[str] = field(default_factory=list)
    guest_ip: list[str] = field(default_factory=list)

    force: bool | None = None


@dataclass(frozen=True)
class ResolvedVMInput:
    """Immutable resolved VM request - contains the VM instance."""

    vms: list[VMInstanceItem]
    force: bool


@dataclass
class VMRequest:
    """Request to resolve a VM by name, ID, IP, or MAC.

    This is a generic request class for operations on existing VMs
    (start, stop, remove, etc.) that require resolving the VM first.
    """

    _result: ResolvedVMInput | None = None

    def __init__(self, *, inputs: VMInput, db: Database | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""

        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._vm_resolver = VMResolver(
            VMRepository(self._db),
            include=["image", "kernel", "network.leases"],
        )

    @property
    def result(self) -> ResolvedVMInput | None:
        return self._result

    def resolve(self) -> ResolvedVMInput:
        """Resolve the VM identifier to a VMInstanceItem.

        Args:
            db: Optional Database instance

        Returns:
            ResolvedVMRequest with the resolved VMInstanceItem

        Raises:
            VMNotFoundError: If VM cannot be found
        """

        identifiers = (
            self._inputs.id
            + self._inputs.name
            + self._inputs.guest_mac
            + self._inputs.guest_ip
        )

        result = self._vm_resolver.resolve_many(identifiers)

        if result.errors and not result.items:
            raise VMNotFoundError(
                f"Could not resolve any VMs: {', '.join(result.errors)}"
            )

        self._result = ResolvedVMInput(
            vms=result.items,
            force=self._inputs.force if self._inputs.force else False,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved VM inputs.

        Validates:
        - MAC addresses: must be valid format
        - IP addresses: must be valid IPv4 and internal/private
        """
        for mac in self._inputs.guest_mac:
            try:
                validate_mac(mac)
            except ValueError as exc:
                raise VMRequestError(
                    f"Invalid guest MAC address: {mac}"
                ) from exc

        for ip in self._inputs.guest_ip:
            NetworkValidator.validate_ipv4_address(
                ip, field_name="guest IP", require_private=True
            )
