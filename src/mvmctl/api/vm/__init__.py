"""VM API module - public surface.

This module re-exports all VM-related API functions from the submodules:
- _delegates.py: Simple delegation functions (stop, pause, resume, ssh, etc.)
- _data.py: Data gathering functions (inspect, export, list, etc.)
- _resolve.py: Resolution wrappers (image/kernel path resolution)
- _registry.py: VM registry operations (create, remove, cleanup)
"""

from __future__ import annotations

# Data gathering functions (inspect, export, list, status, etc.)
from mvmctl.api.vm._data import (
    ResolveVMTargetsResult,
    compute_vm_is_missing,
    export_vm_config,
    get_vm_status_with_exit_code,
    inspect_vm,
    list_vms,
    resolve_vm_targets,
)

# Delegation functions (stop, pause, resume, ssh, logs, console, etc.)
from mvmctl.api.vm._delegates import (
    attach_console,
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    get_console_state,
    get_logs,
    get_vm,
    kill_console,
    load_snapshot,
    pause_vm,
    read_console_output,
    reboot_vm,
    resume_vm,
    send_console_input,
    snapshot_vm,
    ssh_vm,
    start_vm,
    stop_vm,
    vm_cache_dir,
)

# Registry operations (create, remove, cleanup)
from mvmctl.api.vm._registry import (
    cleanup_vms,
    create_vm,
    remove_vm,
)

# Resolution wrappers (image/kernel path resolution)
from mvmctl.api.vm._resolve import (
    resolve_image_id_path,
    resolve_image_multi_strategy,
    resolve_image_path,
    resolve_kernel_id_path,
    resolve_kernel_multi_strategy,
    resolve_kernel_path,
    resolve_vm_selector,
)

# Re-export VMManager for backward compatibility
from mvmctl.core.vm_manager import VMManager, get_vm_manager

__all__ = [
    # Registry operations
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
    # Data gathering functions
    "list_vms",
    "inspect_vm",
    "export_vm_config",
    "get_vm_status_with_exit_code",
    "compute_vm_is_missing",
    "resolve_vm_targets",
    "ResolveVMTargetsResult",
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
]
