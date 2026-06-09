"""Generic VM request resolver for VM operations (start, stop, remove, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.exceptions import VMNotFoundError, VMRequestError
from mvmctl.models import VMInstanceItem
from mvmctl.utils._validators import NetworkValidator
from mvmctl.utils.common import CommonUtils

__all__ = ["VMInput", "VMRequest", "ResolvedVMInput"]


@dataclass
class VMInput:
    """
    Input for operations on existing VMs.

    Identifiers can be any of: VM ID, VM name, guest MAC address,
    or guest IP address. The resolver auto-detects the type.
    """

    identifiers: list[str] = field(default_factory=list)

    force: bool | None = None


@dataclass(frozen=True)
class ResolvedVMInput:
    """Immutable resolved VM request - contains the VM instance."""

    vms: list[VMInstanceItem]
    force: bool


@dataclass
class VMRequest:
    """
    Request to resolve a VM by name, ID, IP, or MAC.

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
            include=[
                "image",
                "kernel",
                "network",
                "network.leases",
                "volumes",
                "binary",
            ],
        )

    @property
    def result(self) -> ResolvedVMInput | None:
        return self._result

    def resolve(self) -> ResolvedVMInput:
        """
        Resolve the VM identifier to a VMInstanceItem.

        Args:
            db: Optional Database instance

        Returns:
            ResolvedVMRequest with the resolved VMInstanceItem

        Raises:
            VMNotFoundError: If VM cannot be found

        """

        self._validate_identifiers()
        result = self._vm_resolver.resolve_many(self._inputs.identifiers)

        if result.errors and not result.items:
            raise VMNotFoundError(
                f"Could not resolve any VMs: {', '.join(result.errors)}"
            )

        self._result = ResolvedVMInput(
            vms=result.items,
            force=self._inputs.force if self._inputs.force else False,
        )

        return self._result

    @staticmethod
    def _is_mac(identifier: str) -> bool:
        """Check if identifier looks like a MAC address."""
        parts = identifier.split(":")
        return len(parts) == 6 and all(
            len(p) == 2 and all(c in "0123456789abcdefABCDEF" for c in p)
            for p in parts
        )

    def _validate_identifiers(self) -> None:
        """
        Validate each identifier based on detected type.

        Raises:
            VMRequestError: If any identifier fails validation.

        """
        for identifier in self._inputs.identifiers:
            if self._is_mac(identifier):
                try:
                    NetworkValidator.validate_mac(identifier)
                except ValueError as exc:
                    raise VMRequestError(
                        f"Invalid MAC address: {identifier}"
                    ) from exc

            elif NetworkValidator.is_ip_address(identifier):
                try:
                    NetworkValidator.validate_ipv4_address(
                        identifier,
                        field_name="guest IP",
                        require_private=True,
                    )
                except ValueError as exc:
                    raise VMRequestError(
                        f"Invalid guest IP: {identifier}"
                    ) from exc

            else:
                # Name or ID — validate as entity name
                try:
                    CommonUtils.validate_entity_name(
                        identifier, entity_type="VM"
                    )
                except ValueError as exc:
                    raise VMRequestError(
                        f"Invalid VM identifier: {identifier}"
                    ) from exc
