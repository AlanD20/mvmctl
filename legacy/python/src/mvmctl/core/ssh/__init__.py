"""SSH domain — stateless SSH connection service."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.ssh._cp import CPService
    from mvmctl.core.ssh._service import SSHService

__all__ = [
    "CPService",
    "SSHService",
]

_LAZY_MAP = {
    "CPService": ("mvmctl.core.ssh._cp", "CPService"),
    "SSHService": ("mvmctl.core.ssh._service", "SSHService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
