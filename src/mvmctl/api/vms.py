"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path

from mvmctl.api.host import check_privileges
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LOG_FOLLOW,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_TYPE,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
    TAP_PREFIX,
)
from mvmctl.core.logs import show_logs
from mvmctl.core.ssh import connect_to_vm
from mvmctl.core.vm_lifecycle import (
    _resolve_image_path as _core_resolve_image_path,
)
from mvmctl.core.vm_lifecycle import (
    _resolve_image_short_id_path as _core_resolve_image_short_id_path,
)
from mvmctl.core.vm_lifecycle import (
    _resolve_kernel_path as _core_resolve_kernel_path,
)
from mvmctl.core.vm_lifecycle import (
    _resolve_kernel_short_id_path as _core_resolve_kernel_short_id_path,
)
from mvmctl.core.vm_lifecycle import (
    create_vm as _create_vm,
)
from mvmctl.core.vm_lifecycle import (
    load_snapshot as _load_snapshot,
)
from mvmctl.core.vm_lifecycle import (
    remove_vm as _remove_vm,
)
from mvmctl.core.vm_lifecycle import (
    snapshot_vm as _snapshot_vm,
)
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.models.vm import VMInstance, VMState

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
    "get_vm_manager",
    "VMManager",
    "resolve_image_path",
    "resolve_kernel_path",
    "resolve_image_short_id_path",
    "resolve_kernel_short_id_path",
]


def resolve_image_path(image: str) -> Path:
    return _core_resolve_image_path(image)


def resolve_kernel_path(kernel: str) -> Path:
    return _core_resolve_kernel_path(kernel)


def resolve_image_short_id_path(image: str) -> Path:
    return _core_resolve_image_short_id_path(image)


def resolve_kernel_short_id_path(kernel: str) -> Path:
    return _core_resolve_kernel_short_id_path(kernel)


def create_vm(
    name: str,
    image: str,
    kernel: str | None = None,
    vcpus: int = DEFAULT_VM_VCPU_COUNT,
    mem: int = DEFAULT_VM_MEM_MIB,
    ip: str | None = None,
    network_name: str = DEFAULT_NETWORK_NAME,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    user: str = DEFAULT_VM_SSH_USER,
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET,
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI,
    firecracker_bin: str = DEFAULT_FIRECRACKER_BIN_NAME,
    vm_manager: VMManager | None = None,
) -> VMInstance:
    check_privileges("/usr/sbin/ip")
    return _create_vm(
        name=name,
        image=image,
        kernel=kernel,
        vcpus=vcpus,
        mem=mem,
        ip=ip,
        network_name=network_name,
        mac=mac,
        ssh_key=ssh_key,
        user_data=user_data,
        user=user,
        enable_api_socket=enable_api_socket,
        enable_pci=enable_pci,
        firecracker_bin=firecracker_bin,
        vm_manager=vm_manager,
    )


def remove_vm(name: str, vm_manager: VMManager | None = None) -> None:
    check_privileges("/usr/sbin/ip")
    return _remove_vm(name=name, vm_manager=vm_manager)


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    check_privileges("/usr/sbin/ip")
    return _snapshot_vm(name=name, mem_out=mem_out, state_out=state_out)


def load_snapshot(name: str, mem_in: Path, state_in: Path, resume_after: bool = True) -> None:
    check_privileges("/usr/sbin/ip")
    return _load_snapshot(name=name, mem_in=mem_in, state_in=state_in, resume_after=resume_after)


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
    from mvmctl.utils.fs import get_vms_dir

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
    import shutil
    import signal

    from mvmctl.core.network import delete_tap, remove_iptables_forward_rules
    from mvmctl.exceptions import NetworkError

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
