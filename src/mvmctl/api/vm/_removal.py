"""VM removal classes - VMRemovalContext, VMBulkCleanupContext."""

from __future__ import annotations

import logging
import os
import shutil
import signal
import subprocess
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import (
    CONST_SIGNAL_EXIT_CODE_BASE,
    DEFAULT_FC_EXITCODE_FILENAME,
)
from mvmctl.core.network import teardown_nat
from mvmctl.exceptions import NetworkError

if TYPE_CHECKING:
    from mvmctl.core.vm_manager import VMManager
    from mvmctl.models.network import NetworkConfig
    from mvmctl.models.vm import VMInstance

logger = logging.getLogger(__name__)


class VMRemovalContext:
    """Manages VM removal state and cleanup.

    NOTE: PURE STATE TRACKER. Does NOT call core modules directly.
    Core call sequencing stays in _registry.py (the orchestrator).
    """

    def __init__(
        self,
        vm: VMInstance,
        vm_dir: Path,
        net_config: NetworkConfig | None,
        bridge: str,
        manager: VMManager,
    ):
        self._vm = vm
        self._vm_dir = vm_dir
        self._net_config = net_config
        self._bridge = bridge
        self._manager = manager
        self._pid: int | None = None

    @property
    def vm(self) -> VMInstance:
        """Get the VM instance being removed."""
        return self._vm

    @property
    def vm_dir(self) -> Path:
        """Get the VM directory path."""
        return self._vm_dir

    @property
    def pid(self) -> int | None:
        """Get the PID of the VM process."""
        return self._pid

    @pid.setter
    def pid(self, value: int | None) -> None:
        """Set the PID of the VM process."""
        self._pid = value

    def shutdown(self, force: bool) -> None:
        """Send SIGKILL or graceful_shutdown to the VM process.

        Args:
            force: If True, send SIGKILL immediately. Otherwise, use graceful shutdown.
        """
        from mvmctl.core.vm_process import graceful_shutdown

        if force and self._pid is not None:
            # Fast path: SIGKILL immediately, no graceful shutdown
            try:
                os.kill(self._pid, signal.SIGKILL)
            except (OSError, ProcessLookupError):
                pass
        else:
            graceful_shutdown(self._pid, self._vm.api_socket_path)

    def wait_and_record_exit(self) -> None:
        """Wait for the VM process to exit and record the exit code."""
        if self._pid is None:
            return

        try:
            _, status = os.waitpid(self._pid, os.WNOHANG)
            if os.WIFEXITED(status):
                self._write_exit_code(os.WEXITSTATUS(status))
            elif os.WIFSIGNALED(status):
                self._write_exit_code(CONST_SIGNAL_EXIT_CODE_BASE + os.WTERMSIG(status))
        except (ChildProcessError, OSError):
            pass

    def _write_exit_code(self, exit_code: int) -> None:
        """Write exit code to the VM directory."""
        exit_code_file = self._vm_dir / DEFAULT_FC_EXITCODE_FILENAME
        try:
            exit_code_file.write_text(str(exit_code))
        except OSError as exc:
            logger.debug("Failed to write exit code: %s", exc)

    def cleanup_all(self, fast: bool = False) -> None:
        """Parallel cleanup of all resources (console, nocloud, network, IP).

        Args:
            fast: If True, skip non-essential cleanup like SSH known_hosts and orphan cleanup.
        """
        # Run cleanup tasks in parallel
        cleanup_tasks = [
            self._cleanup_console,
            self._cleanup_nocloud,
            self._cleanup_network,
            self._cleanup_ip,
        ]

        with ThreadPoolExecutor(max_workers=4) as executor:
            futures = [executor.submit(task) for task in cleanup_tasks]
            for future in as_completed(futures):
                try:
                    future.result()
                except Exception as exc:
                    logger.debug("Cleanup task failed: %s", exc)

        # Skip SSH known_hosts cleanup in fast mode
        if not fast and self._vm.ipv4:
            self._cleanup_ssh_known_hosts()

    def _cleanup_console(self) -> None:
        """Clean up console relay resources."""
        if self._vm.console_relay_pid is not None:
            try:
                from mvmctl.services.console_relay import ConsoleRelayManager

                ConsoleRelayManager().stop_relay(self._vm.name, self._vm.id)
            except (OSError, RuntimeError) as exc:
                logger.warning("Failed to cleanup console relay: %s", exc)

    def _cleanup_nocloud(self) -> None:
        """Clean up nocloud-net server resources."""
        if self._vm.nocloud_net_port is not None and self._vm.ipv4 is not None:
            try:
                from mvmctl.core.firewall import remove_nocloud_input_rule
                from mvmctl.services.nocloud_server import NoCloudNetServerManager

                nocloud_manager = NoCloudNetServerManager()
                if self._vm.id:
                    nocloud_manager.stop_server(self._vm.name, self._vm.id)
                else:
                    nocloud_manager.stop_server(self._vm.name)
                remove_nocloud_input_rule(self._vm.ipv4, self._vm.name, self._vm.nocloud_net_port)
            except (OSError, RuntimeError, NetworkError) as exc:
                logger.warning("Failed to cleanup nocloud-net resources: %s", exc)

    def _cleanup_network(self) -> None:
        """Clean up network resources (iptables rules, TAP device, NAT)."""
        from mvmctl.core.network import (
            delete_tap,
            remove_iptables_forward_rules,
            teardown_nat,
        )

        tap_name = self._vm.tap_device
        if tap_name:
            remove_iptables_forward_rules(tap_name, bridge=self._bridge)
            try:
                teardown_nat(
                    self._bridge,
                    force=False,
                    subnet=self._net_config.subnet if self._net_config else None,
                )
            except NetworkError as exc:
                logger.debug("NAT teardown for bridge %s: %s", self._bridge, exc)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass

    def _cleanup_ip(self) -> None:
        """Release the network IP allocation."""
        from mvmctl.api.network import release_network_ip
        from mvmctl.core.mvm_db import MVMDatabase

        try:
            db_net = (
                MVMDatabase().get_network_by_name(self._net_config.name)
                if self._net_config
                else None
            )
            if db_net and self._vm.id:
                release_network_ip(db_net.id, self._vm.id)
        except NetworkError as exc:
            logger.warning("Failed to release network IP: %s", exc)

    def _cleanup_ssh_known_hosts(self) -> None:
        """Remove VM from SSH known_hosts file."""
        if not self._vm.ipv4:
            return

        try:
            import subprocess

            subprocess.run(["ssh-keygen", "-R", self._vm.ipv4], capture_output=True, check=False)
        except FileNotFoundError:
            pass

    def deregister(self, fast: bool = False) -> None:
        """Remove from DB and delete vm_dir.

        Args:
            fast: If True, skip orphan cleanup.
        """
        self._manager.deregister(self._vm.id)

        if self._vm_dir.exists():
            shutil.rmtree(self._vm_dir)

        # Skip orphan cleanup in fast mode (can be slow and is non-essential)
        if not fast:
            try:
                from mvmctl.services.nocloud_server import NoCloudNetServerManager

                NoCloudNetServerManager().cleanup_orphans()
            except Exception:
                pass


class VMBulkCleanupContext:
    """Manages bulk VM cleanup state.

    NOTE: PURE STATE TRACKER. Does NOT call core modules directly.
    Core call sequencing stays in _registry.py (the orchestrator).
    """

    def __init__(self, manager: VMManager, cache_dir: Path):
        self._manager = manager
        self._cache_dir = cache_dir
        self._targets: list[VMInstance] = []

    @property
    def targets(self) -> list[VMInstance]:
        """Get the list of VMs targeted for cleanup."""
        return self._targets

    def set_targets(self, vms: list[VMInstance]) -> None:
        """Set the list of VMs to clean up."""
        self._targets = vms

    def cleanup_all(self) -> None:
        """Clean up all target VMs."""
        from mvmctl.api.network import get_network
        from mvmctl.core.firewall import remove_nocloud_input_rule
        from mvmctl.core.mvm_db import MVMDatabase
        from mvmctl.core.network import delete_tap, remove_iptables_forward_rules, teardown_nat
        from mvmctl.services.nocloud_server import NoCloudNetServerManager
        from mvmctl.utils.fs import get_vm_dir_by_hash

        for vm in self._targets:
            vm_dir = get_vm_dir_by_hash(vm.id) if vm.id else None

            # Stop nocloud server
            if vm.nocloud_net_port is not None and vm.ipv4 is not None:
                try:
                    nocloud_manager = NoCloudNetServerManager()
                    nocloud_manager.stop_server(vm.name, vm.id)
                except (OSError, RuntimeError):
                    pass

                try:
                    remove_nocloud_input_rule(vm.ipv4, vm.name, vm.nocloud_net_port)
                except NetworkError:
                    pass

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
                net_name = db_net.name if db_net else ""
                net_config = get_network(net_name)
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

            # Deregister VM
            self._manager.deregister(vm.id if vm.id else vm.name)

            # Clean up nocloud cache directory
            nocloud_cache_dir = self._cache_dir / f"nocloud-{vm.id}" if vm.id else None
            if nocloud_cache_dir is not None and nocloud_cache_dir.exists():
                shutil.rmtree(nocloud_cache_dir)

            # Clean up VM directory
            if vm_dir is not None and vm_dir.exists():
                shutil.rmtree(vm_dir)

        # Clean up any orphaned nocloud servers
        try:
            nocloud_manager = NoCloudNetServerManager()
            nocloud_manager.cleanup_orphans()
        except Exception:
            # Don't fail cleanup if orphan cleanup fails
            pass


__all__ = ["VMRemovalContext", "VMBulkCleanupContext", "teardown_nat", "time", "subprocess"]
