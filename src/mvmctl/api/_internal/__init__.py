"""Internal shared utilities for API modules.

WARNING: This module is INTERNAL to the API layer. It is NOT public API.
- Other _internal/* modules can import from here
- api/* modules can import from here
- cli/* and core/* modules CANNOT import from here

See docs/plans/vms-api-refactoring.md for boundary rules.
"""

from __future__ import annotations

__all__ = [
    # Resolvers
    "resolve_vm_selector",
    "resolve_vm_targets",
    "ResolveVMTargetsResult",
    # Network helpers
    "generate_mac_address",
    "generate_tap_device_name",
]
