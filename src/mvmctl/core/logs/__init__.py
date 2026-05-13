"""Log retrieval domain — VM boot and OS log viewing."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.logs._controller import LogController
    from mvmctl.core.logs._service import LogService

__all__ = [
    "LogController",
    "LogService",
]

_LAZY_MAP = {
    "LogController": ("mvmctl.core.logs._controller", "LogController"),
    "LogService": ("mvmctl.core.logs._service", "LogService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
