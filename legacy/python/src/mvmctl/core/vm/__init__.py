"""VM domain - VM lifecycle management."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
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

_LAZY_MAP = {
    "VMController": ("mvmctl.core.vm._controller", "VMController"),
    "FirecrackerSpawner": ("mvmctl.core.vm._firecracker", "FirecrackerSpawner"),
    "VMProvisioner": ("mvmctl.core.vm._provisioner", "VMProvisioner"),
    "VMRepository": ("mvmctl.core.vm._repository", "VMRepository"),
    "VMResolver": ("mvmctl.core.vm._resolver", "VMResolver"),
    "VMResolveResult": ("mvmctl.core.vm._resolver", "VMResolveResult"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
