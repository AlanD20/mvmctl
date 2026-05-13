"""Host domain - Host state and privilege management."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.host._controller import HostController
    from mvmctl.core.host._helper import HostPrivilegeHelper
    from mvmctl.core.host._repository import HostRepository
    from mvmctl.core.host._service import HostService

__all__ = [
    "HostController",
    "HostPrivilegeHelper",
    "HostRepository",
    "HostService",
]

_LAZY_MAP = {
    "HostController": ("mvmctl.core.host._controller", "HostController"),
    "HostPrivilegeHelper": ("mvmctl.core.host._helper", "HostPrivilegeHelper"),
    "HostRepository": ("mvmctl.core.host._repository", "HostRepository"),
    "HostService": ("mvmctl.core.host._service", "HostService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
