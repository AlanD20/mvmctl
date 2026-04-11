"""VM API module - public surface.

This module re-exports all VM-related API functions from the submodules:
- _lifecycle.py: Simple delegation functions (stop, pause, resume, ssh, etc.)
- _query.py: Data gathering functions (inspect, export, list, etc.)
- _asset_resolution.py: Resolution wrappers (image/kernel path resolution)
- _orchestration.py: VM orchestration operations (create, remove, cleanup)
- _resolver.py: VM creation resolver
"""

from __future__ import annotations

# Resolution wrappers (image/kernel path resolution)
from mvmctl.api.vm._asset_resolution import (
    resolve_image_id_path,
    resolve_image_multi_strategy,
    resolve_image_path,
    resolve_kernel_id_path,
    resolve_kernel_multi_strategy,
    resolve_kernel_path,
    resolve_vm_selector,
)

# Exception handling helpers
from mvmctl.api.vm._exceptions import handle_creation_error

# Delegation functions (stop, pause, resume, ssh, logs, console, etc.)
from mvmctl.api.vm._lifecycle import (
    _pause_process,
    _resume_process,
    attach_console,
    check_escape_sequence,
    connect_to_relay,
    connect_to_vm,
    disconnect_from_relay,
    get_console_state,
    get_logs,
    get_vm,
    graceful_shutdown,
    kill_console,
    load_snapshot,
    pause_vm,
    read_console_output,
    reboot_vm,
    resume_vm,
    send_console_input,
    show_logs,
    snapshot_vm,
    ssh_vm,
    start_vm,
    stop_vm,
    vm_cache_dir,
)

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

# Re-export VMManager for backward compatibility
from mvmctl.core.vm_manager import VMManager, get_vm_manager

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
    # Delegation functions
    "stop_vm",
    "pause_vm",
    "resume_vm",
    "start_vm",
    "reboot_vm",
    "ssh_vm",
    "get_logs",
    "attach_console",
    "kill_console",
    "get_console_state",
    "get_vm",
    "vm_cache_dir",
    "snapshot_vm",
    "load_snapshot",
    # Console functions from core
    "check_escape_sequence",
    "connect_to_relay",
    "disconnect_from_relay",
    "read_console_output",
    "send_console_input",
    # Exported for test patching
    "graceful_shutdown",
    "show_logs",
    "connect_to_vm",
    "_pause_process",
    "_resume_process",
    # Removal context classes
    "VMRemovalContext",
    "VMBulkCleanupContext",
    # Data gathering functions
    "list_vms",
    "inspect_vm",
    "export_vm_config",
    "get_vm_status_with_exit_code",
    "compute_vm_is_missing",
    "resolve_vm_target_instances",
    "ResolveVMInstancesResult",
    # Resolution wrappers
    "resolve_image_path",
    "resolve_kernel_path",
    "resolve_image_id_path",
    "resolve_kernel_id_path",
    "resolve_image_multi_strategy",
    "resolve_kernel_multi_strategy",
    "resolve_vm_selector",
    # VMManager exports
    "VMManager",
    "get_vm_manager",
    "FirecrackerClient",
    # Internal exports for test patching
    "_write_pid_file",
    "ConsoleRelayManager",
]
