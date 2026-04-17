"""VM operations orchestration - merged from builder, orchestration, and removal.

This module provides the orchestration layer for VM lifecycle operations.
It combines:
- VMBuilder: Builder pattern for VM creation
- VMOrchestrator: High-level orchestration for VM creation
- VMRemovalContext/VMBulkCleanupContext: State trackers for removal operations
"""

from __future__ import annotations

import hashlib
import logging
import os
import shutil
import signal
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api.input.vm_create_request import ResolvedVMCreateRequest, VMCreateRequest
from mvmctl.constants import (
    DEFAULT_BRIDGE_NAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_NETWORK_NAME,
    MAX_VMS,
)
from mvmctl.core._internal._db import Database
from mvmctl.core.cloudinit._provisioner import (
    CloudInitProvisionConfig,
    CloudInitProvisioner,
    CloudInitProvisionResult,
)
from mvmctl.core.host._service import HostInteractiveService
from mvmctl.core.image._controller import ImageController
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._firecracker import FirecrackerController
from mvmctl.core.vm._guestfs import GuestfsProvisioner
from mvmctl.core.vm._inventory import VMInventory
from mvmctl.core.vm._repository import VMRepository
from mvmctl.db.models import VMInstance
from mvmctl.exceptions import (
    MVMError,
    NetworkError,
    VMBuilderError,
    VMNotFoundError,
)
from mvmctl.models.cloud_init import CloudInitMode
from mvmctl.models.vm import VMStatus
from mvmctl.utils.audit import log_audit
from mvmctl.utils.fs import get_cache_dir, get_vm_dir_by_hash
from mvmctl.utils.network import generate_mac, generate_tap_name
from mvmctl.utils.signals import SigtermContext
from src.mvmctl.core.console._controller import ConsoleController

if TYPE_CHECKING:
    from mvmctl.models.network import NetworkConfig
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)


@dataclass
class VMCreateContext:
    """Builder for VM creation - tracks state and spawns processes.

    Generates VM ID automatically on instantiation based on name.
    NOTE: PURE STATE TRACKER for creation. Does NOT call core modules directly
    except for spawn() which is a builder action.
    Core call sequencing stays in vm_operations.py (the orchestrator).
    """

    name: str
    vm_id: str
    vm_dir: Path
    guest_ip: str
    guest_mac: str
    tap_name: str
    rootfs_path: Path
    resolved: ResolvedVMCreateRequest | None = None

    fc_manager: FirecrackerController | None = None
    relay: ConsoleController | None = None
    # Stores final state of cloud-init, use this as reference
    cloud_init_result: CloudInitProvisionResult | None = None

    resources_created: dict[str, bool] = field(default_factory=dict)

    def __init__(self, name: str, db: Database | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        created_at = datetime.now()
        self.vm_id = self._generate_vm_id(name, created_at)
        self.vm_dir = Path(get_vm_dir_by_hash(self.vm_id))
        self._db = db if db is not None else Database()

    @staticmethod
    def _generate_vm_id(name: str, created_at: datetime) -> str:
        """Generate a unique VM ID from name and creation time."""
        data = f"{name}:{created_at.isoformat()}"
        return hashlib.sha256(data.encode()).hexdigest()[:16]

    def set_resolved(self, resolved: ResolvedVMCreateRequest) -> None:
        self.resolved = resolved

    def set_firecracker_manager(self, manager: FirecrackerController) -> None:
        self.fc_manager = manager

    def clone_image(self) -> None:

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        vm_rootfs_path = Path(f"{self.vm_dir}/rootfs.{self.resolved.image.fs_type}")

        image_controller = ImageController(self.resolved.image, self._db)
        image_controller.ensure_cached()
        image_controller.copy_cached_to(vm_rootfs_path)

        self.rootfs_path = vm_rootfs_path

    def mark_created(self, resource: str) -> None:
        """Mark a resource as created (for cleanup tracking)."""
        self.resources_created[resource] = True

    def was_created(self, resource: str) -> bool:
        """Check if a resource was created."""
        return self.resources_created.get(resource, False)

    def cleanup(self) -> None:
        """Perform cleanup of all created resources. Called on creation failure."""

        if self.vm_dir is None:
            raise VMBuilderError("VM directory not set in context")

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        net_service = NetworkService(self._db)
        lease_service = LeaseService(self.resolved.network, self._db)

        # Cloud-init
        # only clean up nocloud-net since iso/inject are going to be cleaned up along with vm_dir removal
        if (
            self.was_created("cloud-init-net")
            and self.cloud_init_result is not None
            and self.cloud_init_result.nocloud_net_manager is not None
        ):
            try:
                self.cloud_init_result.nocloud_net_manager.stop()

                # Remove all rules created by cloud-init, currently only nocloud-net
                # creates rule.
                from mvmctl.core._internal._iptables_tracker import IPTablesTracker

                iptables_tracker = IPTablesTracker(self._db)
                for rule in self.cloud_init_result.nocloud_net_rules:
                    iptables_tracker.remove_rule(rule)
            except Exception as exc:
                logger.warning("Failed to stop nocloud server during cleanup: %s", exc)

        # Networking
        if self.was_created("network_tap") and self.resolved:
            try:
                net_service.remove_tap(self.tap_name, self.resolved.network.bridge)
            except Exception as exc:
                logger.warning("Failed to cleanup TAP device during cleanup: %s", exc)

            try:
                lease_service.release(self.vm_id)
            except Exception as exc:
                logger.warning("Failed to release network IP during cleanup: %s", exc)

        if self.was_created("console_relay") and self.relay is not None:
            try:
                self.relay.cleanup()
            except Exception as exc:
                logger.warning("Failed to stop console relay during cleanup: %s", exc)

        if self.was_created("firecracker") and self.fc_manager is not None:
            try:
                self.fc_manager.cleanup()
            except Exception as exc:
                logger.warning("Failed to cleanup running firecracker during cleanup: %s", exc)

        if self.was_created("vm_dir") and self.vm_dir.exists():
            try:
                shutil.rmtree(self.vm_dir, ignore_errors=True)
            except OSError as exc:
                logger.warning("Failed to remove VM directory during cleanup: %s", exc)

    def execute(self) -> None:

        if self.vm_dir is None:
            raise VMBuilderError("VM directory not set in context")

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        from mvmctl.utils.fs import secure_mkdir

        self.guest_mac = (
            self.resolved.requested_guest_mac
            if self.resolved.requested_guest_mac
            else generate_mac()
        )
        self.tap_name = generate_tap_name(self.resolved.network.name, self.name)

        secure_mkdir(self.vm_dir, self.resolved.name)
        self.mark_created("vm_dir")

        # IP Lease
        leaseManager = LeaseService(self.resolved.network, self._db)
        if self.resolved.requested_guest_ip:
            self.guest_ip = leaseManager.lease_specific(
                self.resolved.requested_guest_ip, self.vm_id
            )
        else:
            self.guest_ip = leaseManager.lease(self.vm_id)

        # Networking
        net_service = NetworkService(self._db)
        net_service.ensure_bridge(self.resolved.network.bridge, self.resolved.network.subnet)

        # NAT rules shouldn't be tracked since we don't clean it up, and most of the time
        # NAT rules are created after network is created, this is here just to ensure the
        # network NAT rules are present.
        if self.resolved.network.nat_enabled and self.resolved.network.nat_gateways:
            net_service.ensure_nat(
                self.resolved.network.bridge,
                self.resolved.network.nat_gateways_list,
                subnet=self.resolved.network.subnet,
            )

        net_service.ensure_tap(self.tap_name, self.resolved.network.bridge)
        self.mark_created("network_tap")

        # Rootfs
        self.clone_image()
        self.mark_created("rootfs")

        # Cloud-init
        if self.resolved.cloud_init_mode == CloudInitMode.OFF:
            provisioner = GuestfsProvisioner(
                rootfs_path=self.rootfs_path,
                hostname=self.resolved.name,
                user=self.resolved.user,
                target_size_bytes=self.resolved.disk_size_bytes,
                ssh_pubkeys=self.resolved.ssh_keys,
            )
            provisioner.provision()
            self.mark_created("cloud-init-off")

        if self.resolved.cloud_init_mode != CloudInitMode.OFF:
            ci_config = CloudInitProvisionConfig(
                mode=self.resolved.cloud_init_mode,
                vm_name=self.resolved.name,
                vm_id=self.vm_id,
                vm_dir=self.vm_dir,
                cloud_init_dir=(self.vm_dir / "cloud-init"),
                guest_ip=self.guest_ip,
                tap_name=self.tap_name,
                user=self.resolved.user,
                network=self.resolved.network,
                network_prefix_len=self.resolved.network_prefix_len,
                skip_network_config=self.resolved.skip_ci_network_config,
                ssh_pubkeys=self.resolved.ssh_keys,
                custom_user_data_path=self.resolved.custom_user_data_path,
                nocloud_net_port=self.resolved.nocloud_net_port,
                cloud_init_iso_path=self.resolved.cloud_init_iso_path,
                keep_cloud_init_iso=self.resolved.keep_cloud_init_iso,
            )

            ci_provisioner = CloudInitProvisioner(ci_config)
            self.cloud_init_result = ci_provisioner.provision()

            if self.cloud_init_result.mode == CloudInitMode.NET:
                self.mark_created("cloud-init-net")
            elif self.cloud_init_result.mode == CloudInitMode.ISO:
                self.mark_created("cloud-init-iso")
            elif self.cloud_init_result.mode == CloudInitMode.INJECT:
                self.mark_created("cloud-init-inject")

        # Firecracker
        config = FirecrackerController(self, self.resolved)
        self.set_firecracker_manager(config)
        config.write_to_file()
        self.mark_created("firecracker")

        if self.fc_manager is None:
            raise VMBuilderError("Firecracker manager is not set in context")

        # Console
        if self.resolved.enable_console:
            from mvmctl.core.console._controller import ConsoleController

            self.relay = ConsoleController(
                vm_id=self.vm_id,
                vm_dir=self.vm_dir,
                vm_name=self.resolved.name,
            )
            self.relay.create_pty()

        relay_enabled = self.relay is not None
        relay_client_fd = self.relay.client_fd if self.relay is not None else None

        self.fc_manager.spawn(relay_enabled=relay_enabled, relay_client_fd=relay_client_fd)

        # Start console relay if enabled
        if self.resolved.enable_console and self.relay is not None:
            self.relay.close_client_fd()
            self.relay.start()
            self.mark_created("console_relay")

    def to_model(self) -> VMInstance | None:

        if self.resolved is None or self.fc_manager is None or self.fc_manager.pid is None:
            return None

        now = datetime.now(tz=timezone.utc)
        vm_instance = VMInstance(
            name=self.resolved.name,
            id=self.resolved.vm_id,
            pid=self.fc_manager.pid,
            ipv4=self.guest_ip,
            mac=self.guest_mac,
            network_id=self.resolved.network.id,
            tap_device=self.tap_name,
            created_at=now.isoformat(),
            updated_at=now.isoformat(),
            status=VMStatus.RUNNING,
            config_path=str(self.fc_manager.config_path),
            kernel_id=self.resolved.kernel.id,
            image_id=self.resolved.image.id,
            binary_id=self.resolved.binary.id,
            disk_size_mib=self.resolved.disk_size_mib,
            vcpu_count=self.resolved.vcpu_count,
            mem_size_mib=self.resolved.mem_size_mib,
            api_socket_path=str(self.fc_manager.api_socket_path),
            rootfs_path=str(self.rootfs_path),
            rootfs_suffix=self.resolved.image.fs_type,
            enable_pci=self.resolved.enable_pci,
            enable_logging=self.resolved.enable_logging,
            enable_metrics=self.resolved.enable_metrics,
            enable_console=self.resolved.enable_console,
            cloud_init_mode=self.resolved.cloud_init_mode.value,
            log_path=str(self.fc_manager.log_path),
            serial_output_path=str(self.fc_manager.serial_output_path),
            exit_code=None,
            lsm_flags=self.resolved.lsm_flags,
            boot_args=self.resolved.boot_args,
        )

        if self.cloud_init_result and self.cloud_init_result.nocloud_net_manager:
            vm_instance.nocloud_net_port = self.cloud_init_result.nocloud_port
            vm_instance.nocloud_net_pid = self.cloud_init_result.nocloud_pid

        if self.relay and self.relay.pid and self.relay.socket_path:
            vm_instance.relay_socket_path = str(self.relay.socket_path)
            vm_instance.relay_pid = self.relay.pid

        return vm_instance


class VMOperations:
    @staticmethod
    def create(inputs: VMCreateInput) -> None:

        db = Database()

        # Pre-checks before wasting resources
        HostInteractiveService.check_privileges("/usr/sbin/ip", f"create VM '{inputs.name}'")

        vm_inventory = VMInventory(db)
        if vm_inventory.count() >= MAX_VMS:
            raise MVMError(
                f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
            )

        # New VM context
        ctx = VMCreateContext(name=inputs.name)

        # Sanitized - use resolved inputs
        resolver = VMCreateRequest(db)
        resolved = resolver.resolve(inputs, vm_id=ctx.vm_id, vm_dir=ctx.vm_dir)
        resolver.ensure_validate()

        ctx.set_resolved(resolved)

        with SigtermContext(lambda: ctx.cleanup()):
            try:
                ctx.execute()

                vm_instance = ctx.to_model()
                if vm_instance is None:
                    raise VMBuilderError("Failed to create VM instance model")

                vm_repo = VMRepository(db)
                vm_repo.upsert(vm_instance)
                log_audit("vm.create", f"name={inputs.name}")
            except Exception as exc:
                ctx.cleanup()
                raise

    def remoev(self) -> None:
    def cleanup_create_vm(self) -> None:
        pass


def _persist_failed_vm(instance: VMInstance, manager: VMController | None) -> None:
    """Persist failed VM to DB. Called when skip_cleanup=True."""
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
    """Perform all cleanup steps for VM removal using firewall."""
    from mvmctl.api.network import release_network_ip
    from mvmctl.api.vm._firewall import FirewallManager, NocloudManager

    from mvmctl.core.network._service import NetworkService
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
                NetworkService().remove_tap(tap_name)
            except NetworkError:
                pass

    def _cleanup_ip() -> None:
        try:
            db_net = Database().get_network_by_name(net_config.name) if net_config else None
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
    manager: VMController,
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
    name: str, vm_manager: VMController | None = None, force: bool = False, fast: bool = False
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
    from mvmctl.core.vm_process import _read_pid_file

    check_privileges_interactive("/usr/sbin/ip", f"remove VM '{name}'")

    manager = vm_manager or VMController()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir_by_hash(vm.id)
    # Get network name from network_id
    db_net = Database().get_network(vm.network_id) if vm.network_id else None
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
    manager: VMController,
    cache_dir: Path,
) -> None:
    """Perform bulk cleanup of multiple VMs using firewall."""
    from mvmctl.api.network import get_network
    from mvmctl.api.vm._firewall import FirewallManager, NocloudManager

    from mvmctl.core.network._service import NetworkService
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
            db_net = Database().get_network(vm.network_id) if vm.network_id else None
            net_name = db_net.name if db_net else DEFAULT_NETWORK_NAME
            net_config = get_network(net_name)
            bridge = net_config.bridge if net_config else DEFAULT_BRIDGE_NAME
            fm.remove_forward_rules(tap_name, bridge=bridge)
            try:
                NetworkService().remove_tap(tap_name)
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
    all_vms: bool = False, dry_run: bool = False, vm_manager: VMController | None = None
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

    manager = vm_manager or VMController()
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


__all__ = [
    "VMCreateContext",
    "VMOperations",
    "VMRemovalContext",
    "VMBulkCleanupContext",
]
