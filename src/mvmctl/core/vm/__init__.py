"""VM domain - VM lifecycle management."""

from __future__ import annotations

from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._firecracker import FirecrackerController
from mvmctl.core.vm._guestfs import GuestfsProvisioner
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver, VMResolveResult

__all__ = [
    "VMController",
    "VMRepository",
    "VMResolver",
    "VMResolveResult",
    "FirecrackerController",
    "GuestfsProvisioner",
]
