"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from mvmctl.api.host import check_privileges
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_CONSOLE,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LOG_FOLLOW,
    DEFAULT_VM_LOG_LINES,
    DEFAULT_VM_LOG_TYPE,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.core.console import (
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    read_console_output,
    send_console_input,
)
from mvmctl.core.console import (
    get_console_state as _get_console_state,
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
from mvmctl.models import CloudInitMode, VMInstance, VMState
from mvmctl.services.console_relay import ConsoleRelayManager

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
    "attach_console",
    "detach_console",
    "kill_console",
    "get_console_state",
    "check_escape_sequence",
    "connect_to_relay",
    "disconnect_from_relay",
    "read_console_output",
    "send_console_input",
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
    enable_console: bool = DEFAULT_VM_ENABLE_CONSOLE,
    firecracker_bin: str = DEFAULT_FIRECRACKER_BIN_NAME,
    cloud_init_mode: CloudInitMode = CloudInitMode.AUTO,
    cloud_init_iso_path: Path | None = None,
    keep_cloud_init_iso: bool = False,
    vm_manager: VMManager | None = None,
    nocloud_net_port: int = 0,
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
        enable_console=enable_console,
        firecracker_bin=firecracker_bin,
        cloud_init_mode=cloud_init_mode,
        cloud_init_iso_path=cloud_init_iso_path,
        keep_cloud_init_iso=keep_cloud_init_iso,
        vm_manager=vm_manager,
        nocloud_net_port=nocloud_net_port,
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
    import logging
    import os
    import shutil
    import signal

    from mvmctl.core.firewall import remove_nocloud_input_rule
    from mvmctl.core.network import delete_tap, remove_iptables_forward_rules
    from mvmctl.exceptions import NetworkError
    from mvmctl.services.nocloud_server import NoCloudNetServerManager
    from mvmctl.utils.fs import get_cache_dir

    log = logging.getLogger(__name__)

    manager = vm_manager or get_vm_manager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMState.RUNNING]

    if dry_run or not targets:
        return targets

    cache_dir = Path(get_cache_dir())

    for v in targets:
        vm_dir = vm_cache_dir(v.name)

        tap_name = v.tap_device
        if not tap_name:
            log.warning("VM %s has no tap_device in state, skipping TAP cleanup", v.name)

        if v.nocloud_net_port is not None and v.ip is not None:
            try:
                nocloud_manager = NoCloudNetServerManager()
                nocloud_manager.stop_server(v.name)
            except (OSError, RuntimeError):
                pass

            try:
                remove_nocloud_input_rule(v.ip, v.name, v.nocloud_net_port)
            except NetworkError:
                pass

        if v.pid:
            try:
                os.kill(v.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        if tap_name:
            remove_iptables_forward_rules(tap_name)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass

        manager.deregister(v.id if v.id else v.name)

        nocloud_cache_dir = cache_dir / f"nocloud-{v.name}"
        if nocloud_cache_dir.exists():
            shutil.rmtree(nocloud_cache_dir)

        if vm_dir.exists():
            shutil.rmtree(vm_dir)

    return targets


def attach_console(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    if not mgr.is_relay_running(name):
        raise MVMError(f"No console relay running for VM '{name}'")

    socket_path = mgr.get_socket_path(name)
    return {"socket_path": str(socket_path), "vm_name": name}


def detach_console(sock: Any) -> None:
    if sock is not None:
        disconnect_from_relay(sock)


def kill_console(name: str) -> bool:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    return mgr.kill_relay(name)


def get_console_state(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    return _get_console_state(name)
