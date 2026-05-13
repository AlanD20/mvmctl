"""Network domain - Network configuration and IP lease management."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core._shared import IPTablesRuleResolver
    from mvmctl.core.network._controller import NetworkController
    from mvmctl.core.network._lease_resolver import NetworkLeaseResolver
    from mvmctl.core.network._lease_service import LeaseService
    from mvmctl.core.network._repository import (
        LeaseRepository,
        NetworkRepository,
    )
    from mvmctl.core.network._resolver import (
        NetworkResolver,
        NetworkResolveResult,
    )
    from mvmctl.core.network._service import NetworkService

__all__ = [
    "NetworkController",
    "NetworkRepository",
    "LeaseRepository",
    "NetworkResolver",
    "NetworkResolveResult",
    "NetworkService",
    "LeaseService",
    "NetworkLeaseResolver",
    "IPTablesRuleResolver",
]

_LAZY_MAP = {
    "NetworkController": (
        "mvmctl.core.network._controller",
        "NetworkController",
    ),
    "NetworkLeaseResolver": (
        "mvmctl.core.network._lease_resolver",
        "NetworkLeaseResolver",
    ),
    "LeaseService": ("mvmctl.core.network._lease_service", "LeaseService"),
    "LeaseRepository": ("mvmctl.core.network._repository", "LeaseRepository"),
    "NetworkRepository": (
        "mvmctl.core.network._repository",
        "NetworkRepository",
    ),
    "NetworkResolver": ("mvmctl.core.network._resolver", "NetworkResolver"),
    "NetworkResolveResult": (
        "mvmctl.core.network._resolver",
        "NetworkResolveResult",
    ),
    "NetworkService": ("mvmctl.core.network._service", "NetworkService"),
    "IPTablesRuleResolver": (
        "mvmctl.core._shared._iptables_tracker._resolver",
        "IPTablesRuleResolver",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
