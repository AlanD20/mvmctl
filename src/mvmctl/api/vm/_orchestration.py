"""Registry - orchestration functions for VM lifecycle."""

from __future__ import annotations

import logging
import os
import signal
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api.vm._manager import VMManager
from mvmctl.api.vm._removal import VMBulkCleanupContext, VMRemovalContext
from mvmctl.api.vm._resolver import VMInputResolver
from mvmctl.constants import (
    DEFAULT_BRIDGE_NAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_NETWORK_NAME,
    MAX_VMS,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import (
    MVMError,
    NetworkError,
    VMBuilderError,
    VMNotFoundError,
)
from mvmctl.models.vm import VMStatus
from mvmctl.utils.audit import log_audit
from mvmctl.utils.fs import get_cache_dir, get_vm_dir_by_hash
from mvmctl.utils.signals import SigtermContext
from src.mvmctl.api.vm._builder import VMBuilder
from src.mvmctl.api.vm._inventory import VMInventory
from src.mvmctl.core.host_privilege import check_privileges_interactive
from src.mvmctl.db.models import VMInstance

if TYPE_CHECKING:
    from mvmctl.models.network import NetworkConfig
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)


class VMOrchestrator:
    def __init__(self, input: VMCreateInput) -> None:
        self._input = input
        self._db = MVMDatabase()

    def create_vm(self) -> None:

        check_privileges_interactive("/usr/sbin/ip", f"create VM '{self._input.name}'")

        # Create builder first - it generates VM ID automatically
        ctx = VMBuilder(name=self._input.name)

        # Sanitized - use resolved inputs
        resolver = VMInputResolver(self._db)
        resolved = resolver.resolve(self._input, vm_id=ctx.vm_id, vm_dir=ctx.vm_dir)
        resolver.ensure_validate()

        vm_inventory = VMInventory(self._db)
        if vm_inventory.count() >= MAX_VMS:
            raise MVMError(
                f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
            )

        ctx.set_resolved(resolved)

        with SigtermContext(lambda: ctx.cleanup()):
            try:
                ctx.spawn()

                vm_instance = ctx.to_model()
                if vm_instance is None:
                    raise VMBuilderError("Failed to create VM instance model")

                self._db.upsert_vm(vm_instance)
                log_audit("vm.create", f"name={input.name}")
            except Exception as exc:
                ctx.cleanup()

    def cleanup_create_vm(self) -> None:
        pass


def _persist_failed_vm(instance: VMInstance, manager: VMManager | None) -> None:
    """Persist failed VM to DB. Called when skip_cleanup=True."""
    from mvmctl.models.vm import VMStatus

    if manager is None:
        logger.warning("Failed to persist failed VM: manager is None")
        return

    instance.status = VMStatus.ERROR
    try:
        manager.register(instance)
        logger.info("Persisted failed VM '%s' to database for later cleanup", instance.name)
    except Exception as exc:
        logger.warning("Failed to persist failed VM '%s': %s", instance.name, exc)


def _vm_shutdown(pid: int | None, force: bool, api_socket_path: Path | None) -> None:
    """Shutdown a VM process."""
    from mvmctl.core.vm_process import graceful_shutdown

    if force and pid is not None:
        try:
            os.kill(pid, signal.SIGKILL)
        except (OSError, ProcessLookupError):
            pass
    else:
        graceful_shutdown(pid, api_socket_path)


def _vm_wait_and_record_exit(pid: int | None, vm_dir: Path) -> None:
    """Wait for VM process to exit and record exit code."""
    from mvmctl.constants import CONST_SIGNAL_EXIT_CODE_BASE, DEFAULT_FC_EXITCODE_FILENAME

    if pid is None:
        return

    try:
        _, status = os.waitpid(pid, os.WNOHANG)
        exit_code_file = vm_dir / DEFAULT_FC_EXITCODE_FILENAME
        if os.WIFEXITED(status):
            exit_code = os.WEXITSTATUS(status)
        elif os.WIFSIGNALED(status):
            exit_code = CONST_SIGNAL_EXIT_CODE_BASE + os.WTERMSIG(status)
        else:
            return
        try:
            exit_code_file.write_text(str(exit_code))
        except OSError as exc:
            logger.debug("Failed to write exit code: %s", exc)
    except (ChildProcessError, OSError):
        pass


def _cleanup_ssh_known_hosts(ipv4: str) -> None:
    """Remove VM from SSH known_hosts file."""
    try:
        import subprocess

        subprocess.run(["ssh-keygen", "-R", ipv4], capture_output=True, check=False)
    except FileNotFoundError:
        pass


def _perform_removal_cleanup(
    vm: VMInstance,
    net_config: NetworkConfig | None,
    bridge: str,
    fast: bool = False,
) -> None:
    """Perform all cleanup steps for VM removal using _firewall.py."""
    from concurrent.futures import ThreadPoolExecutor, as_completed

    from mvmctl.api.network import release_network_ip
    from mvmctl.api.vm._firewall import FirewallManager, NocloudManager
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.core.network import delete_tap
    from mvmctl.services.console_relay import ConsoleRelayManager

    fm = FirewallManager()
    nm = NocloudManager()

    def _cleanup_console() -> None:
        if vm.relay_pid is not None:
            try:
                ConsoleRelayManager().stop_relay(vm.name, vm.id)
            except (OSError, RuntimeError) as exc:
                logger.warning("Failed to cleanup console relay: %s", exc)

    def _cleanup_nocloud() -> None:
        if vm.nocloud_net_port is not None and vm.ipv4 is not None:
            nm.stop_server(vm.name, vm.id or "")
            fm.remove_nocloud_rule(vm.ipv4, vm.name, vm.nocloud_net_port)

    def _cleanup_network() -> None:
        tap_name = vm.tap_device
        if tap_name:
            fm.remove_forward_rules(tap_name, bridge=bridge)
            fm.teardown_nat(bridge, force=False, subnet=net_config.subnet if net_config else None)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass

    def _cleanup_ip() -> None:
        try:
            db_net = MVMDatabase().get_network_by_name(net_config.name) if net_config else None
            if db_net and vm.id:
                release_network_ip(db_net.id, vm.id)
        except NetworkError as exc:
            logger.warning("Failed to release network IP: %s", exc)

    # Run cleanup tasks in parallel
    cleanup_tasks = [_cleanup_console, _cleanup_nocloud, _cleanup_network, _cleanup_ip]

    with ThreadPoolExecutor(max_workers=4) as executor:
        futures = [executor.submit(task) for task in cleanup_tasks]
        for future in as_completed(futures):
            try:
                future.result()
            except Exception as exc:
                logger.debug("Cleanup task failed: %s", exc)

    # Skip SSH known_hosts cleanup in fast mode
    if not fast and vm.ipv4:
        _cleanup_ssh_known_hosts(vm.ipv4)


def _perform_removal_deregister(
    vm: VMInstance,
    vm_dir: Path,
    manager: VMManager,
    fast: bool = False,
) -> None:
    """Deregister VM from DB and remove directory."""
    from mvmctl.api.vm._firewall import NocloudManager

    manager.deregister(vm.id)

    if vm_dir.exists():
        import shutil

        shutil.rmtree(vm_dir)

    # Skip orphan cleanup in fast mode
    if not fast:
        NocloudManager().cleanup_orphans()


def remove_vm(
    name: str, vm_manager: VMManager | None = None, force: bool = False, fast: bool = False
) -> None:
    """Remove a VM and clean up all associated resources.

    This is the orchestrator function that coordinates all components
    for VM removal using the class-based architecture.

    Args:
        name: The name of the VM to remove.
        vm_manager: Optional VM manager instance for dependency injection.
        force: If True, forcefully kill the VM process immediately.
        fast: If True, skip non-essential cleanup operations.

    Raises:
        VMNotFoundError: If the VM is not found.
        MVMError: If removal fails.
    """
    from mvmctl.api.host import check_privileges_interactive
    from mvmctl.api.network import get_network
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.core.vm_process import _read_pid_file

    check_privileges_interactive("/usr/sbin/ip", f"remove VM '{name}'")

    import mvmctl.api.vm

    manager = vm_manager or mvmctl.api.vm.get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir_by_hash(vm.id)
    # Get network name from network_id
    db_net = MVMDatabase().get_network(vm.network_id) if vm.network_id else None
    net_name = db_net.name if db_net else DEFAULT_NETWORK_NAME
    net_config = get_network(net_name)
    bridge = net_config.bridge if net_config else DEFAULT_BRIDGE_NAME

    # Create removal context (pure state tracker)
    ctx = VMRemovalContext(
        vm=vm,
        vm_dir=vm_dir,
        net_config=net_config,
        bridge=bridge,
        manager=manager,
    )

    # Read PID from file or use VM's recorded PID
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    pid = _read_pid_file(pid_file)
    if pid is None:
        pid = vm.pid
    ctx.pid = pid

    # Orchestration: all core calls are HERE, not in context class
    _vm_shutdown(ctx.pid, force=force, api_socket_path=vm.api_socket_path)
    _vm_wait_and_record_exit(ctx.pid, vm_dir)
    _perform_removal_cleanup(vm, net_config, bridge, fast=fast)
    _perform_removal_deregister(vm, vm_dir, manager, fast=fast)

    # Log the removal
    log_audit("vm.remove", f"name={name}")


def _perform_bulk_cleanup(
    targets: list[VMInstance],
    manager: VMManager,
    cache_dir: Path,
) -> None:
    """Perform bulk cleanup of multiple VMs using _firewall.py."""
    from mvmctl.api.network import get_network
    from mvmctl.api.vm._firewall import FirewallManager, NocloudManager
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.core.network import delete_tap
    from mvmctl.utils.fs import get_vm_dir_by_hash

    fm = FirewallManager()
    nm = NocloudManager()

    for vm in targets:
        vm_dir = get_vm_dir_by_hash(vm.id) if vm.id else None

        # Stop nocloud server
        if vm.nocloud_net_port is not None and vm.ipv4 is not None:
            nm.stop_server(vm.name, vm.id or "")
            fm.remove_nocloud_rule(vm.ipv4, vm.name, vm.nocloud_net_port)

        # Kill VM process
        if vm.pid:
            try:
                os.kill(vm.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        # Clean up network resources
        tap_name = vm.tap_device
        if tap_name:
            # Get network name from network_id
            db_net = MVMDatabase().get_network(vm.network_id) if vm.network_id else None
            net_name = db_net.name if db_net else DEFAULT_NETWORK_NAME
            net_config = get_network(net_name)
            bridge = net_config.bridge if net_config else DEFAULT_BRIDGE_NAME
            fm.remove_forward_rules(tap_name, bridge=bridge)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass
            fm.teardown_nat(bridge)

        # Deregister VM
        manager.deregister(vm.id if vm.id else vm.name)

        # Clean up nocloud cache directory
        nocloud_cache_dir = cache_dir / f"nocloud-{vm.id}" if vm.id else None
        if nocloud_cache_dir is not None and nocloud_cache_dir.exists():
            import shutil

            shutil.rmtree(nocloud_cache_dir)

        # Clean up VM directory
        if vm_dir is not None and vm_dir.exists():
            import shutil

            shutil.rmtree(vm_dir)

    # Clean up any orphaned nocloud servers
    nm.cleanup_orphans()


def cleanup_vms(
    all_vms: bool = False, dry_run: bool = False, vm_manager: VMManager | None = None
) -> list[VMInstance]:
    """Stop and remove stale or all VMs, tearing down their TAP devices and iptables rules.

    This is the orchestrator function that coordinates bulk VM cleanup
    using the class-based architecture.

    Args:
        all_vms: If True, clean up all VMs. Otherwise, only clean up non-running VMs.
        dry_run: If True, return the list of VMs that would be cleaned up without actually cleaning.
        vm_manager: Optional VM manager instance for dependency injection.

    Returns:
        List of VM instances that were (or would be) cleaned up.
    """
    from mvmctl.api.host import check_privileges_interactive

    check_privileges_interactive("/usr/sbin/ip", "cleanup VMs")

    import mvmctl.api.vm

    manager = vm_manager or mvmctl.api.vm.get_vm_manager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMStatus.RUNNING]

    if dry_run or not targets:
        return targets

    cache_dir = Path(get_cache_dir())

    # Create bulk cleanup context (pure state tracker)
    ctx = VMBulkCleanupContext(manager=manager, cache_dir=cache_dir)
    ctx.set_targets(targets)

    # Orchestration: all core calls are HERE, not in context class
    _perform_bulk_cleanup(ctx.targets, manager, cache_dir)

    return targets


__all__ = ["create_vm", "remove_vm", "cleanup_vms"]
