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
    # Firewall/nocloud orchestration
    "NocloudManager",
    "FirewallManager",
    # Exception handling
    "handle_creation_error",
    # ID resolution
    "find_by_id_prefix",
    # Signal handling
    "SigtermContext",
    # Validation
    "validate_mac",
    "validate_vm_name",
    "validate_boot_args",
    "validate_file_exists",
]
