"""Generic VM request resolver for VM operations (start, stop, remove, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from mvmctl.core._internal._db import Database
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.models.vm import VMInput, VMInstance

if TYPE_CHECKING:
    pass

__all__ = ["VMRequest", "ResolvedVMRequest"]


@dataclass(frozen=True)
class ResolvedVMRequest:
    """Immutable resolved VM request - contains the VM instance."""

    vms: list[VMInstance]
    # Allow for future expansion with additional resolved fields
    extra: dict = field(default_factory=dict)


@dataclass
class VMRequest:
    """Request to resolve a VM by name, ID, IP, or MAC.

    This is a generic request class for operations on existing VMs
    (start, stop, remove, etc.) that require resolving the VM first.
    """

    _result: ResolvedVMRequest | None = None

    def __init__(self, *, inputs: VMInput, db: Database | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""

        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._vm_resolver = VMResolver(VMRepository(self._db))

    @property
    def result(self) -> ResolvedVMRequest | None:
        return self._result

    def resolve(self) -> ResolvedVMRequest:
        """Resolve the VM identifier to a VMInstance.

        Args:
            db: Optional Database instance

        Returns:
            ResolvedVMRequest with the resolved VMInstance

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
        self._result = ResolvedVMRequest(vms=result.items)
        return self._result
