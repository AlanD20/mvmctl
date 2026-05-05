"""VM domain - VM lifecycle management."""

from __future__ import annotations

from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._firecracker import FirecrackerSpawner
from mvmctl.core.vm._provisioner import VMProvisioner
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver, VMResolveResult

__all__ = [
    "VMController",
    "VMProvisioner",
    "VMRepository",
    "VMResolver",
    "VMResolveResult",
    "FirecrackerSpawner",
]
