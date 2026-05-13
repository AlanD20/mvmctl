"""
Shared infrastructure for core domains.

All public infrastructure classes are re-exported here so consumers can
import from the package level rather than relying on internal file layout::

    from mvmctl.core._shared import Database, AssetManager, ParallelExecutor

Sub-packages that are heavy or have deep internal structure
(``_guestfs``, ``_iptables_tracker``) may still be imported by their
full path when their sub-modules are needed.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core._shared._asset_manager import AssetManager
    from mvmctl.core._shared._db import Database
    from mvmctl.core._shared._enrichment import RelationEnricher, RelationSpec
    from mvmctl.core._shared._iptables_tracker import (
        IPTablesRuleRepository,
        IPTablesRuleResolver,
        IPTablesRuleResult,
        IPTablesTracker,
    )
    from mvmctl.core._shared._parallel import ParallelExecutor
    from mvmctl.core._shared._resolver_registry import get as get_resolver
    from mvmctl.core._shared._resolver_registry import register

__all__ = [
    "AssetManager",
    "Database",
    "get_resolver",
    "IPTablesRuleRepository",
    "IPTablesRuleResolver",
    "IPTablesRuleResult",
    "IPTablesTracker",
    "ParallelExecutor",
    "register",
    "RelationEnricher",
    "RelationSpec",
]

_LAZY_MAP = {
    "AssetManager": ("mvmctl.core._shared._asset_manager", "AssetManager"),
    "Database": ("mvmctl.core._shared._db", "Database"),
    "RelationEnricher": ("mvmctl.core._shared._enrichment", "RelationEnricher"),
    "RelationSpec": ("mvmctl.core._shared._enrichment", "RelationSpec"),
    "IPTablesRuleRepository": (
        "mvmctl.core._shared._iptables_tracker._repository",
        "IPTablesRuleRepository",
    ),
    "IPTablesRuleResolver": (
        "mvmctl.core._shared._iptables_tracker._resolver",
        "IPTablesRuleResolver",
    ),
    "IPTablesRuleResult": (
        "mvmctl.core._shared._iptables_tracker._tracker",
        "IPTablesRuleResult",
    ),
    "IPTablesTracker": (
        "mvmctl.core._shared._iptables_tracker._tracker",
        "IPTablesTracker",
    ),
    "ParallelExecutor": ("mvmctl.core._shared._parallel", "ParallelExecutor"),
    "get_resolver": ("mvmctl.core._shared._resolver_registry", "get"),
    "register": ("mvmctl.core._shared._resolver_registry", "register"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
