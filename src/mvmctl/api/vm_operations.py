"""VM operations orchestration - merged from builder, orchestration, and removal.

This module provides the orchestration layer for VM lifecycle operations.
It combines:
- VMBuilder: Builder pattern for VM creation
- VMOrchestrator: High-level orchestration for VM creation
- VMRemovalContext/VMBulkCleanupContext: State trackers for removal operations
"""

from __future__ import annotations

import logging
import os
import shutil
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.api.inputs import (
    ResolvedVMCreateInput,
    VMCreateRequest,
    VMExportConfig,
)
from mvmctl.api.inputs._vm_create_input import VMCreateInput
from mvmctl.api.inputs._vm_input import VMInput, VMRequest
from mvmctl.constants import (
    DEFAULT_BRIDGE_NAME,
    MAX_VMS,
)
from mvmctl.core._internal._db import Database
from mvmctl.core._internal._guestfs import GuestfsProvisioner
from mvmctl.core.cloudinit._provisioner import (
    CloudInitProvisionConfig,
    CloudInitProvisioner,
    CloudInitProvisionResult,
)
from mvmctl.core.console._controller import ConsoleController
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._service import ImageService
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._firecracker import FirecrackerSpawner
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._service import VMService
from mvmctl.exceptions import (
    MVMError,
    NetworkError,
    VMCreateError,
    VMNotFoundError,
)
from mvmctl.models.cloudinit import CloudInitMode
from mvmctl.models.firecracker import FirecrackerConfig
from mvmctl.models.vm import VMInstanceItem, VMStatus
from mvmctl.utils.audit import log_audit
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.network import NetworkUtils
from mvmctl.utils.signals import SigtermContext

logger = logging.getLogger(__name__)


@dataclass(init=False)
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
    resolved: ResolvedVMCreateInput | None = None

    fc_manager: FirecrackerSpawner | None = None
    relay: ConsoleController | None = None
    # Stores final state of cloud-init, use this as reference
    cloud_init_result: CloudInitProvisionResult | None = None

    resources_created: dict[str, bool] = field(default_factory=dict)

    def __init__(self, name: str, db: Database | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        from mvmctl.utils.full_hash import HashGenerator

        self.name = name
        created_at = datetime.now()
        self.vm_id = HashGenerator.vm(name, created_at.isoformat())
        self.vm_dir = Path(CacheUtils.get_vm_dir(self.vm_id))
        self._db = db if db is not None else Database()
        self.guest_ip = ""
        self.guest_mac = ""
        self.tap_name = ""
        self.rootfs_path = Path()
        self.resolved = None
        self.fc_manager = None
        self.relay = None
        self.cloud_init_result = None
        self.resources_created = {}

    def set_resolved(self, resolved: ResolvedVMCreateInput) -> None:
        self.resolved = resolved

    def set_firecracker_manager(self, manager: FirecrackerSpawner) -> None:
        self.fc_manager = manager

    def clone_image(self) -> None:

        if self.resolved is None:
            raise VMCreateError("Failed to resolve necessary dependencies")

        vm_rootfs_path = Path(
            f"{self.vm_dir}/rootfs.{self.resolved.image.fs_type}"
        )

        repo = ImageRepository(self._db)
        image_service = ImageService(repo)
        image_service.ensure_cached([self.resolved.image])
        image_service.materialize_to(
            image_id=self.resolved.image.id,
            fs_type=self.resolved.image.fs_type,
            output_path=vm_rootfs_path,
        )

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
            raise VMCreateError("VM directory not set in context")

        if self.resolved is None:
            raise VMCreateError("Failed to resolve necessary dependencies")

        net_repo = NetworkRepository(self._db)
        net_service = NetworkService(net_repo)
        lease_service = LeaseService(
            self.resolved.network, LeaseRepository(self._db)
        )

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
                from mvmctl.core._internal._iptables_tracker import (
                    IPTablesTracker,
                )
                from mvmctl.core._internal._iptables_tracker._repository import (
                    IPTablesRuleRepository,
                )

                iptables_tracker = IPTablesTracker(
                    IPTablesRuleRepository(self._db)
                )
                for rule in self.cloud_init_result.nocloud_net_rules:
                    iptables_tracker.remove_rule(rule)
            except Exception as exc:
                logger.warning(
                    "Failed to stop nocloud server during cleanup: %s", exc
                )

        # Networking
        if self.was_created("network_tap") and self.resolved:
            try:
                net_service.remove_tap(
                    self.tap_name,
                    self.resolved.network.bridge,
                    network_id=self.resolved.network.id,
                )
            except Exception as exc:
                logger.warning(
                    "Failed to cleanup TAP device during cleanup: %s", exc
                )

            try:
                lease_service.release(self.vm_id)
            except Exception as exc:
                logger.warning(
                    "Failed to release network IP during cleanup: %s", exc
                )

        if self.was_created("console_relay") and self.relay is not None:
            try:
                self.relay.cleanup()
            except Exception as exc:
                logger.warning(
                    "Failed to stop console relay during cleanup: %s", exc
                )

        if self.was_created("firecracker") and self.fc_manager is not None:
            try:
                self.fc_manager.cleanup()
            except Exception as exc:
                logger.warning(
                    "Failed to cleanup running firecracker during cleanup: %s",
                    exc,
                )

        # NOTE: vm_dir removal disabled for debugging
        # if self.was_created("vm_dir") and self.vm_dir.exists():
        #     try:
        #         shutil.rmtree(self.vm_dir, ignore_errors=True)
        #     except OSError as exc:
        #         logger.warning(
        #             "Failed to remove VM directory during cleanup: %s", exc
        #         )

    def execute(self) -> None:

        if self.vm_dir is None:
            raise VMCreateError("VM directory not set in context")

        if self.resolved is None:
            raise VMCreateError("Failed to resolve necessary dependencies")

        from mvmctl.utils.fs import FsUtils

        self.guest_mac = (
            self.resolved.requested_guest_mac
            if self.resolved.requested_guest_mac
            else NetworkUtils.generate_mac()
        )
        self.tap_name = NetworkUtils.generate_tap_name(
            self.resolved.network.name, self.name
        )

        FsUtils.secure_mkdir(self.vm_dir, self.resolved.name)
        self.mark_created("vm_dir")

        # IP Lease
        lease_repo = LeaseRepository(self._db)
        lease_manager = LeaseService(self.resolved.network, lease_repo)
        if self.resolved.requested_guest_ip:
            self.guest_ip = lease_manager.lease_specific(
                self.resolved.requested_guest_ip, self.vm_id
            )
        else:
            self.guest_ip = lease_manager.lease(self.vm_id)

        # Networking
        net_repo = NetworkRepository(self._db)
        net_service = NetworkService(net_repo)
        bridge_addr = NetworkUtils.compute_bridge_address(
            self.resolved.network.ipv4_gateway,
            self.resolved.network.subnet,
        )
        net_service.ensure_bridge(self.resolved.network.bridge, bridge_addr)

        # NAT rules shouldn't be tracked since we don't clean it up, and most of the time
        # NAT rules are created after network is created, this is here just to ensure the
        # network NAT rules are present.
        if (
            self.resolved.network.nat_enabled
            and self.resolved.network.nat_gateways
        ):
            net_service.ensure_nat(
                self.resolved.network.bridge,
                self.resolved.network.nat_gateways_list,
                subnet=self.resolved.network.subnet,
                network_id=self.resolved.network.id,
            )

        net_service.ensure_tap(
            self.tap_name,
            self.resolved.network.bridge,
            network_id=self.resolved.network.id,
        )
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
        fc_config = self.build_firecracker_config()
        if fc_config is None:
            raise VMCreateError("Firecracker config is not set in context")

        firecracker_spawner = FirecrackerSpawner(fc_config)
        self.set_firecracker_manager(firecracker_spawner)
        firecracker_spawner.write_to_file()
        self.mark_created("firecracker")

        if self.fc_manager is None:
            raise VMCreateError("Firecracker manager is not set in context")

        # Validate socket path won't exceed Unix domain socket limit
        socket_path = str(self.fc_manager.api_socket_path)
        if len(socket_path) >= 108:
            raise VMCreateError(
                f"VM ID '{self.vm_id}' produces a socket path that is too long "
                f"({len(socket_path)} chars, max 107). "
                f"This is a system limit for Unix domain sockets. "
                f"Path: {socket_path}"
            )

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
        relay_client_fd = (
            self.relay.client_fd if self.relay is not None else None
        )

        self.fc_manager.spawn(
            relay_enabled=relay_enabled, relay_client_fd=relay_client_fd
        )

        # Start console relay if enabled
        if self.resolved.enable_console and self.relay is not None:
            self.relay.close_client_fd()
            self.relay.start()
            self.mark_created("console_relay")

    def build_firecracker_config(self) -> FirecrackerConfig | None:
        """Build Firecracker spawn configuration from resolved state."""

        if self.resolved is None:
            return None

        return FirecrackerConfig(
            vm_dir=self.vm_dir,
            rootfs_path=self.rootfs_path,
            binary_path=str(self.resolved.binary.resolved_path),
            kernel_path=str(self.resolved.kernel.resolved_path),
            vcpu_count=self.resolved.vcpu_count,
            mem_size_mib=self.resolved.mem_size_mib,
            guest_ip=self.guest_ip,
            guest_mac=self.guest_mac,
            tap_name=self.tap_name,
            network_gateway=self.resolved.network.ipv4_gateway,
            network_netmask=self.resolved.network_netmask,
            image_fs_uuid=self.resolved.image.fs_uuid,
            image_fs_type=self.resolved.image.fs_type,
            boot_args=self.resolved.boot_args,
            lsm_flags=self.resolved.lsm_flags,
            enable_pci=self.resolved.enable_pci,
            enable_console=self.resolved.enable_console,
            enable_logging=self.resolved.enable_logging,
            enable_metrics=self.resolved.enable_metrics,
            cloud_init_mode=(
                self.cloud_init_result.mode if self.cloud_init_result else None
            ),
            cloud_init_iso_path=(
                self.cloud_init_result.iso_path
                if self.cloud_init_result
                else None
            ),
            cloud_init_nocloud_url=(
                self.cloud_init_result.nocloud_url
                if self.cloud_init_result
                else None
            ),
        )

    def to_model(self) -> VMInstanceItem | None:

        if (
            self.resolved is None
            or self.fc_manager is None
            or self.fc_manager.pid is None
        ):
            return None

        now = datetime.now(tz=timezone.utc)
        vm_instance = VMInstanceItem(
            name=self.resolved.name,
            id=self.resolved.vm_id,
            pid=self.fc_manager.pid,
            process_start_time=self.fc_manager.process_start_time,
            ipv4=self.guest_ip,
            mac=self.guest_mac,
            network_id=self.resolved.network.id,
            tap_device=self.tap_name,
            created_at=now.isoformat(),
            updated_at=now.isoformat(),
            status=VMStatus.RUNNING,
            config_path=self.fc_manager.config_path.name,
            kernel_id=self.resolved.kernel.id,
            image_id=self.resolved.image.id,
            binary_id=self.resolved.binary.id,
            disk_size_mib=self.resolved.disk_size_mib,
            vcpu_count=self.resolved.vcpu_count,
            mem_size_mib=self.resolved.mem_size_mib,
            api_socket_path=self.fc_manager.api_socket_path.name,
            rootfs_path=self.rootfs_path.name,
            rootfs_suffix=self.resolved.image.fs_type,
            enable_pci=self.resolved.enable_pci,
            enable_logging=self.resolved.enable_logging,
            enable_metrics=self.resolved.enable_metrics,
            enable_console=self.resolved.enable_console,
            cloud_init_mode=self.resolved.cloud_init_mode.value,
            log_path=self.fc_manager.log_path.name,
            serial_output_path=self.fc_manager.serial_output_path.name,
            exit_code=None,
            lsm_flags=self.resolved.lsm_flags,
            boot_args=self.resolved.boot_args,
        )

        if (
            self.cloud_init_result
            and self.cloud_init_result.nocloud_net_manager
        ):
            vm_instance.nocloud_net_port = self.cloud_init_result.nocloud_port
            vm_instance.nocloud_net_pid = self.cloud_init_result.nocloud_pid

        if self.relay and self.relay.pid and self.relay.socket_path:
            vm_instance.relay_socket_path = self.relay.socket_path.name
            vm_instance.relay_pid = self.relay.pid

        return vm_instance


class VMOperation:
    @staticmethod
    def create(inputs: VMCreateInput) -> None:

        # Pre-checks before wasting resources
        HostPrivilegeHelper.check_privileges(
            "/usr/sbin/ip", f"create VM '{inputs.name}'"
        )

        db = Database()

        vm_repo = VMRepository(db)
        if vm_repo.count() >= MAX_VMS:
            raise MVMError(
                f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
            )

        # New VM context
        ctx = VMCreateContext(name=inputs.name)

        # Sanitized - use resolved inputs
        request = VMCreateRequest(
            vm_id=ctx.vm_id, vm_dir=ctx.vm_dir, inputs=inputs, db=db
        )
        resolved = request.resolve()
        ctx.set_resolved(resolved)

        with SigtermContext(lambda: ctx.cleanup()):
            try:
                ctx.execute()

                vm_instance = ctx.to_model()
                if vm_instance is None:
                    raise VMCreateError("Failed to create VM instance model")

                vm_repo.upsert(vm_instance)
                log_audit("vm.create", f"name={inputs.name}")
            except Exception:
                ctx.cleanup()
                raise

    @staticmethod
    def remove(inputs: VMInput) -> None:
        """Remove one or more VMs."""
        db = Database()
        resolver = VMRequest(inputs=inputs, db=db)
        resolved = resolver.resolve()

        for vm in resolved.vms:
            remove_vm(vm.name, force=resolved.force)

    @staticmethod
    def list_all() -> list[VMInstanceItem]:
        """List all VMs."""
        return VMRepository(Database()).list_all()

    @staticmethod
    def get(inputs: VMInput) -> VMInstanceItem:
        """Get a single VM by identifier."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        return resolved.vms[0]

    @staticmethod
    def inspect(inputs: VMInput) -> VMInstanceItem:
        """Inspect a VM (returns the VM instance; enrichment can be added later)."""
        return VMOperation.get(inputs)

    @staticmethod
    def start(inputs: VMInput) -> None:
        """Start one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(Database())
        service.start_many(resolved.vms)
        log_audit("vm.start", f"count={len(resolved.vms)}")

    @staticmethod
    def stop(inputs: VMInput) -> None:
        """Stop one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(Database())
        service.stop_many(resolved.vms, force=resolved.force)
        log_audit("vm.stop", f"count={len(resolved.vms)}")

    @staticmethod
    def reboot(inputs: VMInput) -> None:
        """Reboot one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(Database())
        service.reboot_many(resolved.vms, force=resolved.force)
        log_audit("vm.reboot", f"count={len(resolved.vms)}")

    @staticmethod
    def pause(inputs: VMInput) -> None:
        """Pause one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(Database())
        service.pause_many(resolved.vms)
        log_audit("vm.pause", f"count={len(resolved.vms)}")

    @staticmethod
    def resume(inputs: VMInput) -> None:
        """Resume one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(Database())
        service.resume_many(resolved.vms)
        log_audit("vm.resume", f"count={len(resolved.vms)}")

    @staticmethod
    def snapshot(inputs: VMInput, mem_out: Path, state_out: Path) -> None:
        """Snapshot a single VM's memory and state."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]
        controller = VMController(vm, VMRepository(Database()))
        controller.snapshot(mem_out, state_out)
        log_audit("vm.snapshot", f"name={vm.name}")

    @staticmethod
    def load_snapshot(
        inputs: VMInput,
        mem_in: Path,
        state_in: Path,
        resume_after: bool | None = None,
    ) -> None:
        """Load a snapshot into a single VM."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]
        controller = VMController(vm, VMRepository(Database()))
        controller.load_snapshot(mem_in, state_in, resume_after)
        log_audit("vm.load_snapshot", f"name={vm.name}")

    @staticmethod
    def cleanup(
        all_vms: bool = False, dry_run: bool = False
    ) -> list[VMInstanceItem]:
        """Cleanup stale or all VMs."""
        return cleanup_vms(all_vms, dry_run)

    @staticmethod
    def export(name: str) -> VMExportConfig:
        """Export a VM's configuration."""
        return export_vm_config(name)


__all__ = [
    "VMCreateContext",
    "VMOperation",
]


## TO BE MIGRATED
def _persist_failed_vm(
    instance: VMInstanceItem, repo: VMRepository | None
) -> None:
    """Persist failed VM to DB. Called when skip_cleanup=True."""
    if repo is None:
        logger.warning("Failed to persist failed VM: repo is None")
        return

    instance.status = VMStatus.ERROR
    try:
        repo.upsert(instance)
        logger.info(
            "Persisted failed VM '%s' to database for later cleanup",
            instance.name,
        )
    except Exception as exc:
        logger.warning(
            "Failed to persist failed VM '%s': %s", instance.name, exc
        )


def _vm_shutdown(
    pid: int | None, force: bool, api_socket_path: Path | None
) -> None:
    """Shutdown a VM process."""
    from mvmctl.utils.process_signals import ProcessSignalHandler

    if pid is None:
        return

    handler = ProcessSignalHandler(pid)

    if force:
        handler.kill()
        return

    if api_socket_path:
        try:
            from mvmctl.core.vm._firecracker import FirecrackerClient

            client = FirecrackerClient(Path(api_socket_path))
            client.send_ctrl_alt_del()
            client.close()
            if handler.graceful_shutdown(pre_signal_hook=lambda: False):
                return
        except Exception:
            pass

    handler.graceful_shutdown()


def _vm_wait_and_record_exit(pid: int | None, vm_dir: Path) -> None:
    """Wait for VM process to exit and record exit code."""
    from mvmctl.constants import (
        CONST_SIGNAL_EXIT_CODE_BASE,
        DEFAULT_FC_EXITCODE_FILENAME,
    )

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

        subprocess.run(
            ["ssh-keygen", "-R", ipv4], capture_output=True, check=False
        )
    except FileNotFoundError:
        pass


def _perform_removal_cleanup(
    vm: VMInstanceItem,
    bridge: str,
    network_id: str,
    fast: bool = False,
) -> None:
    """Perform all cleanup steps for VM removal."""
    from mvmctl.core.network._repository import LeaseRepository
    from mvmctl.core.network._service import NetworkService
    from mvmctl.services.console_relay.manager import ConsoleRelayManager

    def _cleanup_console() -> None:
        if vm.relay_pid is not None and vm.id:
            try:
                relay = ConsoleRelayManager(
                    id=vm.id, path=CacheUtils.get_vm_dir(vm.id)
                )
                relay.stop()
            except (OSError, RuntimeError) as exc:
                logger.warning("Failed to cleanup console relay: %s", exc)

    def _cleanup_network() -> None:
        tap_name = vm.tap_device
        if tap_name:
            try:
                net_repo = NetworkRepository(Database())
                net_service = NetworkService(net_repo)
                net_service.remove_tap(tap_name, network_id=network_id)
            except NetworkError:
                pass

    def _cleanup_ip() -> None:
        try:
            if vm.id:
                lease_repo = LeaseRepository(Database())
                lease_repo.release_by_vm(vm.id)
        except NetworkError as exc:
            logger.warning("Failed to release network IP: %s", exc)

    cleanup_tasks = [
        _cleanup_console,
        _cleanup_network,
        _cleanup_ip,
    ]

    with ThreadPoolExecutor(max_workers=4) as executor:
        futures = [executor.submit(task) for task in cleanup_tasks]
        for future in as_completed(futures):
            try:
                future.result()
            except Exception as exc:
                logger.debug("Cleanup task failed: %s", exc)

    if not fast and vm.ipv4:
        _cleanup_ssh_known_hosts(vm.ipv4)


def _perform_removal_deregister(
    vm: VMInstanceItem,
    vm_dir: Path,
    fast: bool = False,
) -> None:
    """Deregister VM from DB and remove directory."""
    db = Database()
    repo = VMRepository(db)
    repo.delete(vm.id)

    if vm_dir.exists():
        shutil.rmtree(vm_dir)


def remove_vm(
    name: str,
    force: bool = False,
    fast: bool = False,
) -> None:
    """Remove a VM and clean up all associated resources."""
    HostPrivilegeHelper.check_privileges("/usr/sbin/ip", f"remove VM '{name}'")

    db = Database()
    repo = VMRepository(db)
    vm = repo.get_by_name(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = CacheUtils.get_vm_dir(vm.id)

    # Get network info
    net_repo = NetworkRepository(db)
    db_net = net_repo.get(vm.network_id) if vm.network_id else None
    bridge = db_net.bridge if db_net else DEFAULT_BRIDGE_NAME

    # Stop VM
    controller = VMController(vm, repo)
    controller.stop(force=force)

    # Cleanup
    _perform_removal_cleanup(vm, bridge, vm.network_id, fast=fast)
    _perform_removal_deregister(vm, vm_dir, fast=fast)

    log_audit("vm.remove", f"name={name}")


def cleanup_vms(
    all_vms: bool = False,
    dry_run: bool = False,
) -> list[VMInstanceItem]:
    """Stop and remove stale or all VMs."""
    HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "cleanup VMs")

    db = Database()
    repo = VMRepository(db)
    vms = repo.list_all()

    targets = (
        vms if all_vms else [v for v in vms if v.status != VMStatus.RUNNING]
    )

    if dry_run or not targets:
        return targets

    for vm in targets:
        try:
            remove_vm(vm.name, force=True, fast=True)
        except Exception as exc:
            logger.warning("Failed to cleanup VM %s: %s", vm.name, exc)

    return targets


def export_vm_config(name: str) -> VMExportConfig:
    """Export a VM's configuration as a portable VMExportConfig."""
    db = Database()
    repo = VMRepository(db)
    vm = repo.get_by_name(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    from mvmctl.api.inputs import (
        VMExportBinaryConfig,
        VMExportBootConfig,
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportConfig,
        VMExportFirecrackerConfig,
        VMExportImageConfig,
        VMExportKernelConfig,
        VMExportNetworkConfig,
    )

    return VMExportConfig(
        name=vm.name,
        compute=VMExportComputeConfig(
            vcpus=vm.vcpu_count,
            mem=vm.mem_size_mib,
        ),
        image=VMExportImageConfig(),
        kernel=VMExportKernelConfig(),
        binary=VMExportBinaryConfig(),
        network=VMExportNetworkConfig(
            ip=vm.ipv4,
            mac=vm.mac,
        ),
        boot=VMExportBootConfig(
            enable_console=vm.enable_console,
        ),
        firecracker=VMExportFirecrackerConfig(
            enable_pci=vm.enable_pci,
            lsm_flags=vm.lsm_flags,
        ),
        cloud_init=VMExportCloudInitConfig(
            mode=vm.cloud_init_mode or "inject",
            user=vm.name,
        ),
    )
