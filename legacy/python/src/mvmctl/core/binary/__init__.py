"""Binary domain - Firecracker binary management."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.binary._controller import BinaryController
    from mvmctl.core.binary._repository import BinaryRepository
    from mvmctl.core.binary._resolver import BinaryResolver, BinaryResolveResult
    from mvmctl.core.binary._service import BinaryService

__all__ = [
    "BinaryController",
    "BinaryRepository",
    "BinaryResolver",
    "BinaryResolveResult",
    "BinaryService",
]

_LAZY_MAP = {
    "BinaryController": ("mvmctl.core.binary._controller", "BinaryController"),
    "BinaryRepository": ("mvmctl.core.binary._repository", "BinaryRepository"),
    "BinaryResolver": ("mvmctl.core.binary._resolver", "BinaryResolver"),
    "BinaryResolveResult": (
        "mvmctl.core.binary._resolver",
        "BinaryResolveResult",
    ),
    "BinaryService": ("mvmctl.core.binary._service", "BinaryService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
