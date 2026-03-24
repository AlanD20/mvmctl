"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path

from fcm.core.vm_manager import VMManager, get_vm_manager
from fcm.models.vm import VMInstance, VMState
from fcm.core.vm_lifecycle import (
    create_vm,
    remove_vm,
    snapshot_vm,
    load_snapshot,
)
from fcm.core.ssh import connect_to_vm
from fcm.core.logs import show_logs
from fcm.constants import (
    TAP_PREFIX,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_LOG_TYPE,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_FOLLOW,
)
from fcm.api.host import check_privileges

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


def list_vms(include_stopped: bool = True, vm_manager: VMManager | None = None) -> list[VMInstance]:
    """Return all registered VMs, optionally filtering out stopped ones."""
    manager = vm_manager or get_vm_manager()
    all_vms = manager.list_all()
    if not include_stopped:
        return [vm for vm in all_vms if vm.status == VMState.RUNNING]
    return all_vms


def get_vm(name: str, vm_manager: VMManager | None = None) -> VMInstance | None:
    """Return the VMInstance for the given name, or None if not found."""
    manager = vm_manager or get_vm_manager()
    return manager.get(name)


def deregister_vm(name: str, vm_manager: VMManager | None = None) -> None:
    """Remove a VM from the registry without tearing down its resources."""
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if vm is not None:
        manager.deregister(vm.id)
    else:
        manager.deregister(name)


def vm_cache_dir(name: str) -> Path:
    """Return the cache directory path for a given VM name."""
    from fcm.utils.fs import get_vms_dir

    return get_vms_dir() / name


def ssh_vm(
    name: str,
    user: str = DEFAULT_VM_SSH_USER,
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


def get_logs(
    name: str,
    log_type: str = DEFAULT_VM_LOG_TYPE,
    lines: int = DEFAULT_VM_LOG_LINES,
    follow: bool = DEFAULT_VM_LOG_FOLLOW,
) -> list[str]:
    """View VM logs. Returns log lines."""
    return show_logs(
        vm_name=name,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )


def cleanup_vms(
    all_vms: bool = False, dry_run: bool = False, vm_manager: VMManager | None = None
) -> list[VMInstance]:
    """Stop and remove stale or all VMs, tearing down their TAP devices and iptables rules."""
    check_privileges("/usr/sbin/ip")
    import os
    import signal
    import shutil
    from fcm.core.network import remove_iptables_forward_rules, delete_tap
    from fcm.exceptions import NetworkError

    manager = vm_manager or get_vm_manager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMState.RUNNING]

    if dry_run or not targets:
        return targets

    for v in targets:
        vm_dir = vm_cache_dir(v.name)
        tap_name = f"{TAP_PREFIX}-{v.name}-0"

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

        manager.deregister(v.id if v.id else v.name)

        if vm_dir.exists():
            shutil.rmtree(vm_dir)

    return targets
