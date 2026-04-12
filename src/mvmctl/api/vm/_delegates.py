"""Delegation functions for VM operations.

This module contains simple delegation functions that wrap core module
calls for VM lifecycle operations like stop, pause, resume, ssh, etc.
"""

from __future__ import annotations

from pathlib import Path

from mvmctl.constants import DEFAULT_FIRECRACKER_BIN_NAME, DEFAULT_SNAPSHOT_RESUME
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
from mvmctl.core.firecracker import FirecrackerClient, get_vm_socket_path
from mvmctl.core.logs import show_logs
from mvmctl.core.ssh import connect_to_vm
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.core.vm_process import (
    graceful_shutdown,
)
from mvmctl.core.vm_process import (
    pause_vm as _pause_process,
)
from mvmctl.core.vm_process import (
    resume_vm as _resume_process,
)
from mvmctl.exceptions import MVMError, VMNotFoundError
from mvmctl.models import ConsoleInfo, ConsoleState, VMInstance, VMStatus
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.utils.audit import log_audit
from mvmctl.utils.fs import get_vm_dir_by_hash

__all__ = [
    "stop_vm",
    "pause_vm",
    "resume_vm",
    "ssh_vm",
    "get_logs",
    "attach_console",
    "kill_console",
    "get_console_state",
    "get_vm",
    "vm_cache_dir",
    "reboot_vm",
    "start_vm",
    "snapshot_vm",
    "load_snapshot",
    # Console functions re-exported from core
    "check_escape_sequence",
    "connect_to_relay",
    "disconnect_from_relay",
    "read_console_output",
    "send_console_input",
]


def stop_vm(name: str, force: bool = False) -> None:
    """Stop a running VM.

    Args:
        name: VM name
        force: If True, force immediate shutdown without graceful shutdown

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If VM is not running or stop fails
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status not in (VMStatus.RUNNING, VMStatus.PAUSED):
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")
    manager.update_status(name, VMStatus.STOPPING)
    try:
        graceful_shutdown(vm.pid, vm.api_socket_path, force=force)
        manager.update_status(name, VMStatus.STOPPED)
    except Exception as exc:
        manager.update_status(name, VMStatus.ERROR)
        raise MVMError(f"Failed to stop VM '{name}': {exc}") from exc


def pause_vm(name: str) -> None:
    """Pause a running VM.

    Args:
        name: VM name

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If VM is not running or has no API socket
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.RUNNING:
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")
    if not vm.api_socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")
    client = FirecrackerClient(vm.api_socket_path)
    try:
        _pause_process(client)
        manager.update_status(name, VMStatus.PAUSED)
    finally:
        client.close()


def resume_vm(name: str) -> None:
    """Resume a paused VM.

    Args:
        name: VM name

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If VM is not paused or has no API socket
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.PAUSED:
        raise MVMError(f"VM '{name}' is not paused (current state: {vm.status.value})")
    if not vm.api_socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")
    client = FirecrackerClient(vm.api_socket_path)
    try:
        _resume_process(client)
        manager.update_status(name, VMStatus.RUNNING)
    finally:
        client.close()


def ssh_vm(
    name: str,
    user: str,
    key: Path | None = None,
    cmd: str | None = None,
) -> int:
    """Open SSH session or execute command on a VM.

    Args:
        name: VM name
        user: SSH user
        key: Path to SSH private key
        cmd: Optional command to execute

    Returns:
        Exit code from SSH session

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If VM has no IP address
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")
    if not vm.ipv4:
        raise MVMError(f"VM '{name}' has no IP address")

    log_audit("vm.ssh", f"name={name},user={user}")

    return connect_to_vm(
        ip=vm.ipv4,
        user=user,
        key_path=key,
        command=cmd,
        exec_mode=cmd is None,
    )


def get_logs(
    name: str,
    log_type: str,
    lines: int,
    follow: bool,
) -> list[str]:
    """View VM logs.

    Args:
        name: VM name
        log_type: Type of log (boot or os)
        lines: Number of lines to show
        follow: Whether to follow log output

    Returns:
        List of log lines
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    vm_hash = vm.id if vm is not None else name
    return show_logs(
        vm_hash=vm_hash,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )


def attach_console(name: str) -> ConsoleInfo:
    """Attach to a VM's console relay.

    Args:
        name: The name of the VM to attach to.

    Returns:
        ConsoleInfo containing the socket path and VM name.

    Raises:
        VMNotFoundError: If the VM is not found.
        MVMError: If no console relay is running for the VM.
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    if not mgr.is_relay_running(name, vm_hash):
        raise MVMError(f"No console relay running for VM '{name}'")

    socket_path = mgr.get_socket_path(vm_hash if vm_hash else name)
    return ConsoleInfo(socket_path=socket_path, vm_name=name)


def kill_console(name: str) -> bool:
    """Kill the console relay for a VM.

    Args:
        name: VM name

    Returns:
        True if relay was killed, False otherwise

    Raises:
        VMNotFoundError: If VM not found
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    return mgr.kill_relay(name, vm_hash)


def get_console_state(name: str) -> ConsoleState:
    """Get the current console state for a VM.

    Args:
        name: The name of the VM to check.

    Returns:
        ConsoleState containing the relay status, PID, and socket path.

    Raises:
        VMNotFoundError: If the VM is not found.
    """
    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_hash = vm.id if vm.id else None
    state = _get_console_state(name, vm_hash)
    return ConsoleState(
        running=state.get("running", False),
        pid=state.get("pid"),
        socket_path=state.get("socket_path"),
    )


def get_vm(name: str, vm_manager: VMManager | None = None) -> VMInstance | None:
    """Return the VMInstance for the given name, or None if not found."""
    manager = vm_manager or get_vm_manager()
    return manager.get(name)


def vm_cache_dir(vm: VMInstance) -> Path:
    """Return the cache directory path for a VM using its hash ID."""
    return get_vm_dir_by_hash(vm.id)


def reboot_vm(name: str, force: bool = False) -> None:
    """Reboot a VM (stop then start).

    Args:
        name: VM name
        force: If True, force immediate shutdown

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If reboot fails
    """
    stop_vm(name, force=force)
    start_vm(name)


def start_vm(name: str) -> None:
    """Start a stopped VM.

    Args:
        name: VM name

    Raises:
        VMNotFoundError: If VM not found
        MVMError: If VM is not stopped or start fails
    """
    import subprocess
    import time

    from mvmctl.constants import (
        DEFAULT_FC_API_SOCKET_FILENAME,
        DEFAULT_FC_CONFIG_FILENAME,
        DEFAULT_FC_CONSOLE_LOG_FILENAME,
        DEFAULT_FC_LOG_FILENAME,
        DEFAULT_FC_PID_FILENAME,
        DEFAULT_VM_ENABLE_API_SOCKET,
    )
    from mvmctl.core.vm_process import _write_pid_file
    from mvmctl.models import VMStatus

    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.STOPPED:
        raise MVMError(f"VM '{name}' is not stopped (current state: {vm.status.value})")
    if not vm.id:
        raise MVMError(f"VM '{name}' has no ID")

    vm_dir = get_vm_dir_by_hash(vm.id)
    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    if not config_file.exists():
        raise MVMError(f"VM config not found: {config_file}")

    firecracker_bin = DEFAULT_FIRECRACKER_BIN_NAME
    if vm.config and vm.config.kernel_path:
        fc_bin_path = Path(firecracker_bin)
        if (fc_bin_path.is_absolute() or "/" in firecracker_bin) and not fc_bin_path.exists():
            raise MVMError(f"Firecracker binary not found: {firecracker_bin}")

    enable_api_socket_runtime = (
        vm.config.enable_api_socket if vm.config else DEFAULT_VM_ENABLE_API_SOCKET
    )
    socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket_runtime else None
    if enable_api_socket_runtime and socket_path:
        fc_cmd = [
            firecracker_bin,
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_file),
        ]
    else:
        fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]

    log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
    console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME
    log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
    console_fp = None
    try:
        console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
        proc = subprocess.Popen(
            fc_cmd,
            stdin=subprocess.DEVNULL,
            stdout=console_fp,
            stderr=log_fp,
            start_new_session=True,
        )
        log_fp.close()
        console_fp.close()
        _write_pid_file(pid_file, proc.pid)
        vm.pid = proc.pid
        vm.api_socket_path = socket_path
        vm.status = VMStatus.RUNNING
        manager.register(vm)
        time.sleep(0.5)
    except Exception as exc:
        try:
            log_fp.close()
        except OSError:
            pass
        if console_fp is not None:
            try:
                console_fp.close()
            except OSError:
                pass
        raise MVMError(f"Failed to start VM '{name}': {exc}") from exc


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    """Snapshot VM memory and disk state.

    Args:
        name: VM name
        mem_out: Memory snapshot output path
        state_out: VM state output path

    Raises:
        MVMError: If socket not found or snapshot fails
    """
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )
    client = FirecrackerClient(socket_path)
    try:
        client.create_snapshot(mem_out, state_out)
    finally:
        client.close()


def load_snapshot(
    name: str, mem_in: Path, state_in: Path, resume_after: bool | None = None
) -> None:
    """Load VM from snapshot.

    Args:
        name: VM name
        mem_in: Memory snapshot input path
        state_in: VM state input path
        resume_after: Whether to resume VM after loading

    Raises:
        MVMError: If socket not found or load fails
    """
    effective_resume = resume_after if resume_after is not None else DEFAULT_SNAPSHOT_RESUME
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )
    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, effective_resume)
    finally:
        client.close()
