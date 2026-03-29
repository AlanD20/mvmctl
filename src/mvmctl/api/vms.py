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
from mvmctl.core.network import teardown_nat
from mvmctl.core.network_manager import get_network
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
    "get_vm_status_with_exit_code",
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
    "inspect_vm",
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
    image_path: Path | None = None,
    kernel_path: Path | None = None,
    vcpus: int = DEFAULT_VM_VCPU_COUNT,
    mem: int = DEFAULT_VM_MEM_MIB,
    disk_size: str | None = None,
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
        image_path=image_path,
        kernel_path=kernel_path,
        vcpus=vcpus,
        mem=mem,
        disk_size=disk_size,
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
    manager = get_vm_manager()
    vm = manager.get(name)
    # Use VM hash if found, otherwise fall back to name (for backward compatibility)
    vm_hash = vm.id if vm is not None else name
    return show_logs(
        vm_hash=vm_hash,
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
                nocloud_manager.stop_server(v.name, v.id)
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
            net_config = get_network(v.network_name or "")
            bridge = net_config.bridge if net_config else ""
            remove_iptables_forward_rules(tap_name, bridge=bridge)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass
            try:
                teardown_nat(bridge)
            except NetworkError:
                pass

        manager.deregister(v.id if v.id else v.name)

        nocloud_cache_dir = cache_dir / f"nocloud-{v.name}"
        if nocloud_cache_dir.exists():
            shutil.rmtree(nocloud_cache_dir)

        if vm_dir.exists():
            shutil.rmtree(vm_dir)

    # Clean up any orphaned nocloud servers
    try:
        nocloud_manager = NoCloudNetServerManager()
        nocloud_manager.cleanup_orphans()
    except Exception:
        # Don't fail cleanup if orphan cleanup fails
        pass

    return targets


def attach_console(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    if not mgr.is_relay_running(name, vm_hash):
        raise MVMError(f"No console relay running for VM '{name}'")

    socket_path = mgr.get_socket_path(vm_hash if vm_hash else name)
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
    vm_hash = vm.id if vm.id else None
    return mgr.kill_relay(name, vm_hash)


def get_console_state(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_hash = vm.id if vm.id else None
    return _get_console_state(name, vm_hash)


def inspect_vm(name: str) -> dict[str, Any]:
    """Get detailed VM information."""
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()

    # Try short ID first
    vm = manager.get_by_short_id(name)
    if vm:
        return _gather_vm_details(vm)

    # Fall back to name lookup
    matches = manager.get_by_name(name)
    if len(matches) == 1:
        return _gather_vm_details(matches[0])
    elif len(matches) > 1:
        raise MVMError(f"Multiple VMs match name '{name}' — use short ID")

    raise VMNotFoundError(f"VM '{name}' not found")


def _gather_vm_details(vm: VMInstance) -> dict[str, Any]:
    """Gather comprehensive VM details."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    vm_dir = get_vm_dir_by_hash(vm.id)

    # Check file existence - use stored rootfs suffix or default to .ext4
    rootfs_suffix = vm.rootfs_suffix or ".ext4"
    rootfs_path = vm_dir / f"rootfs{rootfs_suffix}"
    config_path = vm_dir / "firecracker.json"

    info: dict[str, Any] = {
        "id": vm.id,
        "short_id": vm.id[:6] if vm.id else "-",
        "name": vm.name,
        "status": vm.status.value,
        "created_at": vm.created_at.isoformat() if vm.created_at else None,
        "pid": vm.pid,
        "ip": vm.ip,
        "mac": vm.mac,
        "network_name": vm.network_name,
        "tap_device": vm.tap_device,
        "paths": {
            "vm_dir": str(vm_dir),
            "rootfs": str(rootfs_path) if rootfs_path.exists() else None,
            "config": str(config_path) if config_path.exists() else None,
        },
        "features": {
            "api_socket": vm.socket_path is not None,
            "console": vm.console_socket_path is not None,
            "nocloud_net": vm.nocloud_net_port is not None,
        },
    }

    # Add cloud-init info if available
    if vm.nocloud_net_port:
        info["nocloud_net"] = {
            "port": vm.nocloud_net_port,
            "server_pid": vm.nocloud_server_pid,
        }

    if vm.console_socket_path:
        info["console"] = {
            "socket_path": str(vm.console_socket_path),
            "relay_pid": vm.console_relay_pid,
        }

    return info


def get_vm_status_with_exit_code(vm: VMInstance) -> tuple[str, int | None]:
    """Get VM status with exit code if process has exited.

    Args:
        vm: VM instance to check

    Returns:
        Tuple of (status_string, exit_code_or_none)
    """
    import os

    from mvmctl.models import VMState

    # Check if process is running
    if vm.pid is not None:
        try:
            os.kill(vm.pid, 0)
            return "running", None
        except (ProcessLookupError, OSError):
            # Process exited - try to get exit code
            pass

    # Try to get exit code from various sources
    exit_code = _get_exit_code_from_sources(vm)

    if exit_code is not None:
        return f"exited({exit_code})", exit_code

    # Check VM state from metadata
    if vm.status == VMState.RUNNING:
        return "exited", None  # Was running but process died
    return vm.status.value, None


def _get_exit_code_from_sources(vm: VMInstance) -> int | None:
    """Try to extract exit code from various sources.

    Sources checked in order:
    1. firecracker.exitcode file in VM directory
    2. firecracker.log for exit code patterns
    """
    import re

    from mvmctl.constants import DEFAULT_FC_EXITCODE_FILENAME, DEFAULT_FC_LOG_FILENAME
    from mvmctl.utils.fs import get_vm_dir_by_hash

    if not vm.id:
        return None

    vm_dir = get_vm_dir_by_hash(vm.id)

    # Check for explicit exit code file
    exitcode_path = vm_dir / DEFAULT_FC_EXITCODE_FILENAME
    if exitcode_path.exists():
        try:
            return int(exitcode_path.read_text().strip())
        except (ValueError, OSError):
            pass

    # Check firecracker.log for exit patterns
    log_path = vm_dir / DEFAULT_FC_LOG_FILENAME
    if log_path.exists():
        try:
            content = log_path.read_text()
            # Look for common exit code patterns
            patterns = [
                r"exit(?:ed| code)[\s:]+(\d+)",
                r"returned\s+(\d+)",
                r"exit_status[=:\s]+(\d+)",
            ]
            for pattern in patterns:
                match = re.search(pattern, content, re.IGNORECASE)
                if match:
                    return int(match.group(1))
        except OSError:
            pass

    return None
