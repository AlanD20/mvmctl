"""Key domain - SSH key management and resolution."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.key._controller import KeyController
    from mvmctl.core.key._repository import KeyRepository
    from mvmctl.core.key._resolver import KeyResolver, KeyResolveResult
    from mvmctl.core.key._service import KeyService

__all__ = [
    "KeyController",
    "KeyRepository",
    "KeyResolver",
    "KeyResolveResult",
    "KeyService",
]

_LAZY_MAP = {
    "KeyController": ("mvmctl.core.key._controller", "KeyController"),
    "KeyRepository": ("mvmctl.core.key._repository", "KeyRepository"),
    "KeyResolver": ("mvmctl.core.key._resolver", "KeyResolver"),
    "KeyResolveResult": ("mvmctl.core.key._resolver", "KeyResolveResult"),
    "KeyService": ("mvmctl.core.key._service", "KeyService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
