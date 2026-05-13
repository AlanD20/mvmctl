"""Utility modules."""

from __future__ import annotations

from mvmctl.utils._lazy_import import resolve_lazy

__all__ = [
    "_disk",
    "_io",
    "_system",
    "_validators",
    "crypto",
    "fs",
    "http",
    "TimingLog",
]

_LAZY_MAP: dict[str, tuple[str, str]] = {
    "TimingLog": ("mvmctl.utils.timinglog", "TimingLog"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
