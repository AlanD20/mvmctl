"""VM API module - public surface.

This module re-exports all VM-related API functions from the submodules:
- _manager.py: VMManager class for VM lifecycle operations
- _query.py: Data gathering functions (inspect, export, list, etc.)
- _orchestration.py: VM orchestration operations (create, remove, cleanup)
- _resolver.py: VM creation resolver
"""

from __future__ import annotations

# Exception handling helpers
from mvmctl.api.vm._exceptions import handle_creation_error

# VMManager class for VM lifecycle operations
from mvmctl.api.vm._manager import VMManager

# Orchestration operations (create, remove, cleanup)
from mvmctl.api.vm._orchestration import (
    cleanup_vms,
    create_vm,
    remove_vm,
)

# Data gathering functions (inspect, export, list, status, etc.)
from mvmctl.api.vm._query import (
    ResolveVMInstancesResult,
    compute_vm_is_missing,
    export_vm_config,
    get_vm_status_with_exit_code,
    inspect_vm,
    list_vms,
    resolve_vm_target_instances,
)

# Removal context classes (pure state trackers)
from mvmctl.api.vm._removal import VMBulkCleanupContext, VMRemovalContext

# Re-export FirecrackerClient for test patching backward compatibility
from mvmctl.core.firecracker import FirecrackerClient

# Re-export _write_pid_file for test patching backward compatibility
from mvmctl.core.vm_process import _write_pid_file

# Re-export ConsoleRelayManager for test patching backward compatibility
from mvmctl.services.console_relay import ConsoleRelayManager

__all__ = [
    # Exception handling
    "handle_creation_error",
    # Orchestration operations
    "create_vm",
    "remove_vm",
    "cleanup_vms",
    # VMManager class
    "VMManager",
    # Data gathering functions
    "list_vms",
    "inspect_vm",
    "export_vm_config",
    "get_vm_status_with_exit_code",
    "compute_vm_is_missing",
    "resolve_vm_target_instances",
    "ResolveVMInstancesResult",
    # Removal context classes
    "VMRemovalContext",
    "VMBulkCleanupContext",
    # Re-exports for test patching backward compatibility
    "FirecrackerClient",
    "_write_pid_file",
    "ConsoleRelayManager",
]
