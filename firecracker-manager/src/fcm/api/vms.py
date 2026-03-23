"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path

from fcm.core.vm_manager import VMManager
from fcm.models.vm import VMInstance, VMState
from fcm.core.vm_lifecycle import (
    create_vm,
    remove_vm,
    snapshot_vm,
    load_snapshot,
)
from fcm.core.ssh import connect_to_vm
from fcm.core.logs import show_logs

__all__ = [
    "list_vms",
    "get_vm",
    "deregister_vm",
    "vm_cache_dir",
    "create_vm",
    "remove_vm",
    "snapshot_vm",
    "load_snapshot",
    "ssh_vm",
    "get_logs",
    "cleanup_vms",
]

def list_vms(include_stopped: bool = True) -> list[VMInstance]:
    """Return all registered VMs."""
    manager = VMManager()
    all_vms = manager.list_all()
    if not include_stopped:
        return [vm for vm in all_vms if vm.status == VMState.RUNNING]
    return all_vms

def get_vm(name: str) -> VMInstance | None:
    """Look up a VM by name."""
    return VMManager().get(name)

def deregister_vm(name: str) -> None:
    """Remove a VM entry from the state registry."""
    VMManager().deregister(name)

def vm_cache_dir(name: str) -> Path:
    """Return the cache directory path for a given VM name."""
    from fcm.utils.fs import get_vms_dir
    return get_vms_dir() / name

def ssh_vm(
    name: str,
    user: str = "root",
    key: Path | None = None,
    cmd: str | None = None,
) -> int:
    """Open SSH session or execute command on a VM."""
    return connect_to_vm(
        vm_name_or_ip=name,
        user=user,
        key_path=key,
        command=cmd,
        exec_mode=cmd is None,
    )

def get_logs(name: str, log_type: str = "os", lines: int = 50, follow: bool = False) -> int:
    """View VM logs."""
    return show_logs(
        vm_name=name,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )

def cleanup_vms(all_vms: bool = False, dry_run: bool = False) -> list[VMInstance]:
    """Remove stopped VMs and stale directories. Returns list of affected VMs."""
    import os
    import signal
    import shutil
    from fcm.core.network import remove_iptables_forward_rules, delete_tap
    from fcm.exceptions import NetworkError

    manager = VMManager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMState.RUNNING]

    if dry_run or not targets:
        return targets

    for v in targets:
        vm_dir = vm_cache_dir(v.name)
        tap_name = f"fc-{v.name}-0"

        if v.pid:
            try:
                os.kill(v.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        remove_iptables_forward_rules(tap_name)
        try:
            delete_tap(tap_name)
        except NetworkError:
            pass

        manager.deregister(v.name)

        if vm_dir.exists():
            shutil.rmtree(vm_dir)

    return targets
