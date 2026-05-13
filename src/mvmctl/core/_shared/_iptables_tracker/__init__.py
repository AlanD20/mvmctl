"""IPTables / NFTables tracker — idempotent firewall rule management with DB persistence."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from ._repository import IPTablesRuleRepository
    from ._resolver import IPTablesRuleResolver
    from ._tracker import IPTablesTracker

__all__ = [
    "IPTablesRuleRepository",
    "IPTablesRuleResolver",
    "IPTablesTracker",
]

_LAZY_MAP = {
    "IPTablesRuleRepository": (
        "mvmctl.core._shared._iptables_tracker._repository",
        "IPTablesRuleRepository",
    ),
    "IPTablesRuleResolver": (
        "mvmctl.core._shared._iptables_tracker._resolver",
        "IPTablesRuleResolver",
    ),
    "IPTablesTracker": (
        "mvmctl.core._shared._iptables_tracker._tracker",
        "IPTablesTracker",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
