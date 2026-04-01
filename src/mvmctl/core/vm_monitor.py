"""VM state reconciliation from live signals.

This module provides on-demand VM state reconciliation by checking live
process status and Firecracker API responses, rather than relying on
stale state.json values.
"""

from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.exceptions import FirecrackerError, SocketNotFoundError
from mvmctl.models.vm import VMInstance, VMState

if TYPE_CHECKING:
    from mvmctl.core.vm_manager import VMManager

logger = logging.getLogger(__name__)


def reconcile_vm(vm: VMInstance, manager: "VMManager") -> VMState:
    """Determine actual VM state from live signals and persist it.

    Detection priority:
    1. If pid is None → return vm.status as-is (never launched or already cleaned up)
    2. os.kill(pid, 0) — is the Firecracker process alive?
       - If alive: query FirecrackerClient.describe_instance() via socket_path
         - FC state "Running" → VMState.RUNNING
         - FC state "Paused"  → VMState.PAUSED
         - socket unreachable/no socket → VMState.RUNNING (process alive, socket issue)
       - If dead: read exit code
         - exit_code == 0     → VMState.STOPPED
         - exit_code != 0     → VMState.CRASHED
         - exit_code is None  → VMState.ERROR
    3. If current state == new state: skip write to state.json
    4. Otherwise: manager.update_status(vm.name, new_state) and return new_state

    Args:
        vm: The VM instance to reconcile
        manager: The VM manager for persisting state changes

    Returns:
        The reconciled VM state
    """
    # 1. If pid is None, return status as-is
    if vm.pid is None:
        return vm.status

    # 2. Check if process is alive using os.kill(pid, 0)
    try:
        os.kill(vm.pid, 0)
        process_alive = True
    except ProcessLookupError:
        # Process is dead
        process_alive = False
    except PermissionError:
        # Process exists but we don't have permission to signal it
        # This means it's alive
        process_alive = True
    except OSError:
        # Other OS errors, treat as dead
        process_alive = False

    new_state: VMState

    if process_alive:
        # Process is alive - query Firecracker API
        new_state = _check_firecracker_state(vm)
    else:
        # Process is dead - determine state from exit code
        new_state = _determine_state_from_exit_code(vm)

    # 3. If state hasn't changed, skip the write
    if vm.status == new_state:
        return new_state

    # 4. Persist the new state
    try:
        manager.update_status(vm.name, new_state)
    except Exception:
        logger.exception("Failed to update VM status for %s", vm.name)

    vm.status = new_state
    return new_state


def _check_firecracker_state(vm: VMInstance) -> VMState:
    """Query Firecracker API to determine VM state.

    Args:
        vm: The VM instance with socket_path

    Returns:
        VMState based on Firecracker response
    """
    socket_path = vm.socket_path

    # If no socket path, we can't query FC, but process is alive
    if socket_path is None:
        return VMState.RUNNING

    try:
        from mvmctl.core.firecracker import FirecrackerClient

        with FirecrackerClient(Path(socket_path)) as client:
            instance_info = client.describe_instance()

        if instance_info is None:
            # Socket reachable but no response - assume running
            return VMState.RUNNING

        fc_state = instance_info.get("state", "")
        fc_state_str = str(fc_state) if fc_state else ""

        if fc_state_str == "Paused":
            return VMState.PAUSED
        elif fc_state_str == "Running":
            return VMState.RUNNING
        else:
            # Unknown state but process alive - assume running
            return VMState.RUNNING

    except (FirecrackerError, SocketNotFoundError, OSError):
        # Socket not ready or unreachable, but process is alive
        return VMState.RUNNING
    except Exception:
        # Unexpected error - process is alive so assume running
        logger.exception("Unexpected error checking Firecracker state for %s", vm.name)
        return VMState.RUNNING


def _determine_state_from_exit_code(vm: VMInstance) -> VMState:
    """Determine VM state from process exit code.

    Args:
        vm: The VM instance with exit_code

    Returns:
        VMState based on exit code
    """
    if vm.exit_code is None:
        return VMState.ERROR

    if vm.exit_code == 0:
        return VMState.STOPPED

    return VMState.CRASHED
