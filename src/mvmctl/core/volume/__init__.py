from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.volume._controller import VolumeController
    from mvmctl.core.volume._repository import VolumeRepository
    from mvmctl.core.volume._resolver import VolumeResolver
    from mvmctl.core.volume._service import VolumeService

__all__ = [
    "VolumeController",
    "VolumeRepository",
    "VolumeResolver",
    "VolumeService",
]

_LAZY_MAP = {
    "VolumeController": ("mvmctl.core.volume._controller", "VolumeController"),
    "VolumeRepository": ("mvmctl.core.volume._repository", "VolumeRepository"),
    "VolumeResolver": ("mvmctl.core.volume._resolver", "VolumeResolver"),
    "VolumeService": ("mvmctl.core.volume._service", "VolumeService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
