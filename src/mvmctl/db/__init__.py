"""Database module for mvmctl.

Internal database implementation — only used via core/_internal/_db.py.
No other file may import from this module directly.

Dataclass models (Binary, Image, Kernel, Network, VMInstance, etc.) have been
migrated to domain-specific files in mvmctl.models/. This module now only
re-exports the MigrationRunner and enum classes retained for archive code
compatibility.
"""

from .migrations.runner import MigrationRunner
from .models import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
)

__all__ = [
    "MigrationRunner",
    # Enums retained for archive code compatibility.
    # New code should import from mvmctl.models.network instead.
    "IPTablesChain",
    "IPTablesPort",
    "IPTablesProtocol",
    "IPTablesRuleType",
    "IPTablesTable",
    "IPTablesTarget",
    "IPTablesWildcard",
]
