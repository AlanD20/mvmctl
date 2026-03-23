"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path

from fcm.core.vm_manager import VMManager
from fcm.models.vm import VMInstance, VMState

__all__ = [
    "list_vms",
    "get_vm",
]


def list_vms(include_stopped: bool = True) -> list[VMInstance]:
    """Return all registered VMs.

    Args:
        include_stopped: When False, only return VMs with RUNNING status.

    Returns:
        List of VMInstance objects ordered by name.
    """
    manager = VMManager()
    all_vms = manager.list_all()
    if not include_stopped:
        return [vm for vm in all_vms if vm.status == VMState.RUNNING]
    return all_vms


def get_vm(name: str) -> VMInstance | None:
    """Look up a VM by name.

    Args:
        name: VM name as registered in the cache.

    Returns:
        VMInstance if found, None otherwise.
    """
    return VMManager().get(name)


def deregister_vm(name: str) -> None:
    """Remove a VM entry from the state registry.

    Does not stop the process or clean up networking — use the CLI
    ``vm remove`` command for the full teardown sequence.

    Args:
        name: VM name to deregister.

    Raises:
        fcm.exceptions.VMNotFoundError: If the VM does not exist.
    """
    VMManager().deregister(name)


def vm_cache_dir(name: str) -> Path:
    """Return the cache directory path for a given VM name.

    The directory may not yet exist (e.g. before ``vm create``).

    Args:
        name: VM name.

    Returns:
        Absolute path to ``<cache-root>/vms/<name>/``.
    """
    from fcm.utils.fs import get_vms_dir

    return get_vms_dir() / name
