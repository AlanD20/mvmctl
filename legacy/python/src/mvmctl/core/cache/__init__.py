"""Cache maintenance operations — isolated, domain-agnostic cleanup."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from ._service import CacheService

__all__ = ["CacheService"]

_LAZY_MAP = {
    "CacheService": ("mvmctl.core.cache._service", "CacheService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
