"""Console domain - VM console management."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.console._controller import ConsoleController

__all__ = [
    "ConsoleController",
]

_LAZY_MAP = {
    "ConsoleController": (
        "mvmctl.core.console._controller",
        "ConsoleController",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
