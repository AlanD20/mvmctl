"""Generic VM request resolver for VM operations (start, stop, remove, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from mvmctl.core._internal._db import Database
from mvmctl.db.models import VMInstance
from mvmctl.exceptions import VMNotFoundError

if TYPE_CHECKING:
    pass

__all__ = ["VMRequest", "ResolvedVMRequest"]


@dataclass(frozen=True)
class ResolvedVMRequest:
    """Immutable resolved VM request - contains the VM instance."""

    vm: VMInstance
    # Allow for future expansion with additional resolved fields
    extra: dict = field(default_factory=dict)


@dataclass
class VMRequest:
    """Request to resolve a VM by name, ID, IP, or MAC.

    This is a generic request class for operations on existing VMs
    (start, stop, remove, etc.) that require resolving the VM first.
    """

    identifier: str  # name, ID prefix, IP, or MAC

    def __post_init__(self) -> None:
        if not self.identifier:
            raise VMNotFoundError("VM identifier is required")

    def resolve(self, db: Database | None = None) -> ResolvedVMRequest:
        """Resolve the VM identifier to a VMInstance.

        Args:
            db: Optional Database instance

        Returns:
            ResolvedVMRequest with the resolved VMInstance

        Raises:
            VMNotFoundError: If VM cannot be found
        """
        from mvmctl.core.vm._resolver import VMResolver

        database = db if db is not None else Database()
        resolver = VMResolver(database)
        vm = resolver.resolve(self.identifier)

        return ResolvedVMRequest(vm=vm)
