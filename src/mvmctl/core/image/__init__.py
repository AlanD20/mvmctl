"""Image domain - Image management and resolution."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.image._controller import ImageController
    from mvmctl.core.image._repository import ImageRepository
    from mvmctl.core.image._resolver import ImageResolver, ImageResolveResult
    from mvmctl.core.image._service import ImageService

__all__ = [
    "ImageController",
    "ImageRepository",
    "ImageResolver",
    "ImageResolveResult",
    "ImageService",
]

_LAZY_MAP = {
    "ImageController": ("mvmctl.core.image._controller", "ImageController"),
    "ImageRepository": ("mvmctl.core.image._repository", "ImageRepository"),
    "ImageResolver": ("mvmctl.core.image._resolver", "ImageResolver"),
    "ImageResolveResult": ("mvmctl.core.image._resolver", "ImageResolveResult"),
    "ImageService": ("mvmctl.core.image._service", "ImageService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
