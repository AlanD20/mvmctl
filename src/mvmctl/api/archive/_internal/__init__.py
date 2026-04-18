"""Internal shared utilities for API modules.

WARNING: This module is INTERNAL to the API layer. It is NOT public API.
- Other _internal/* modules can import from here
- api/* modules can import from here
- cli/* and core/* modules CANNOT import from here

See docs/plans/vms-api-refactoring.md for boundary rules.
"""

from __future__ import annotations

from mvmctl.api._internal._asset_manager import AssetManager
from mvmctl.api._internal._iptables_tracker import IPTablesRuleResult, IPTablesTracker
from mvmctl.api._internal._key_manager import KeyManager
from mvmctl.api._internal._network_ip_lease import NetworkIPLeaseManager
from mvmctl.api._internal._network_manager import NetworkManager

__all__ = [
    "AssetManager",
    "IPTablesRuleResult",
    "IPTablesTracker",
    "KeyManager",
    "NetworkIPLeaseManager",
    "NetworkManager",
]
