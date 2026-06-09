"""NFTables rule management — tracker + repository for nftables rules."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from ._repository import NFTablesRuleRepository
    from ._resolver import NFTablesRuleResolver
    from ._tracker import NFTablesTracker

__all__ = [
    "NFTablesRuleRepository",
    "NFTablesRuleResolver",
    "NFTablesTracker",
]

_LAZY_MAP = {
    "NFTablesRuleRepository": (
        "mvmctl.core._shared._nftables_tracker._repository",
        "NFTablesRuleRepository",
    ),
    "NFTablesRuleResolver": (
        "mvmctl.core._shared._nftables_tracker._resolver",
        "NFTablesRuleResolver",
    ),
    "NFTablesTracker": (
        "mvmctl.core._shared._nftables_tracker._tracker",
        "NFTablesTracker",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)
