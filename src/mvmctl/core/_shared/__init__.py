"""Shared infrastructure for core domains.

All public infrastructure classes are re-exported here so consumers can
import from the package level rather than relying on internal file layout::

    from mvmctl.core._shared import Database, AssetManager, ParallelExecutor

Sub-packages that are heavy or have deep internal structure
(``_guestfs``, ``_iptables_tracker``) may still be imported by their
full path when their sub-modules are needed.
"""

from __future__ import annotations

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
