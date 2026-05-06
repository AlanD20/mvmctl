"""
VM operations orchestration - merged from builder, orchestration, and removal.

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
import signal
import time
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.api.inputs import (
    ResolvedVMCreateInput,
    VMCreateRequest,
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
from mvmctl.api.inputs._vm_create_input import VMCreateInput
from mvmctl.api.inputs._vm_import_input import VMImportInput, VMImportRequest
from mvmctl.api.inputs._vm_input import VMInput, VMRequest
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.cloudinit._provisioner import (
    CloudInitProvisionConfig,
    CloudInitProvisioner,
    CloudInitProvisionResult,
)
from mvmctl.core.config._service import SettingsService
from mvmctl.core.console._controller import ConsoleController
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._service import ImageService
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.core.vm import VMProvisioner, VMResolver
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
from mvmctl.models import (
    CloudInitMode,
    FirecrackerConfig,
    VMInstanceItem,
    VMStatus,
)
from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)
from mvmctl.utils._system import SigtermContext, is_process_running
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.network import NetworkUtils

logger = logging.getLogger(__name__)


@dataclass(init=False)
class VMCreateContext:
    """
    Builder for VM creation - tracks state and spawns processes.

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

    # Internal state for respawn operations
    _vm: VMInstanceItem | None = None
    _snapshot_mode: bool = False

    def __init__(
        self,
        name: str,
        db: Database | None = None,
        vm_id: str | None = None,
        vm_dir: Path | None = None,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        from mvmctl.utils.crypto import HashGenerator

        self._on_progress = on_progress
        self.name = name
        if vm_id is not None:
            self.vm_id = vm_id
            self.vm_dir = (
                Path(vm_dir)
                if vm_dir is not None
                else Path(CacheUtils.get_vm_dir(self.vm_id))
            )
        else:
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

    @property
    def _ssh_pubkey_contents(self) -> list[str]:
        """Extract public key content strings from resolved SSHKeyItem list."""
        contents: list[str] = []
        if self.resolved is None:
            return contents
        for k in self.resolved.ssh_keys:
            if k.public_key_path:
                path = Path(k.public_key_path)
                if path.exists():
                    contents.append(path.read_text().strip())
        return contents

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
                from mvmctl.core._shared import IPTablesTracker
                from mvmctl.core._shared._iptables_tracker._repository import (
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

        if self.was_created("vm_dir") and self.vm_dir.exists():
            try:
                shutil.rmtree(self.vm_dir, ignore_errors=True)
            except OSError as exc:
                logger.warning(
                    "Failed to remove VM directory during cleanup: %s", exc
                )

    def execute(self) -> None:

        if self.vm_dir is None:
            raise VMCreateError("VM directory not set in context")

        if self.resolved is None:
            raise VMCreateError("Failed to resolve necessary dependencies")

        _t0 = time.perf_counter()
        _step_start = _t0

        from mvmctl.utils.fs import FsUtils

        self.guest_mac = (
            self.resolved.requested_guest_mac
            if self.resolved.requested_guest_mac
            else NetworkUtils.generate_mac(
                mac_prefix=self.resolved.guest_mac_prefix
            )
        )
        self.tap_name = NetworkUtils.generate_tap_name(
            self.resolved.network.name, self.name
        )

        FsUtils.secure_mkdir(self.vm_dir, self.resolved.name)
        self.mark_created("vm_dir")

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="network",
                    status="running",
                    message="Configuring network...",
                )
            )

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

        logger.info(
            "[timing] network_setup: %.3fs", time.perf_counter() - _step_start
        )
        _step_start = time.perf_counter()

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="rootfs",
                    status="running",
                    message="Copying root filesystem...",
                )
            )

        # Rootfs
        self.clone_image()
        self.mark_created("rootfs")

        logger.info(
            "[timing] image_clone: %.3fs", time.perf_counter() - _step_start
        )
        _step_start = time.perf_counter()

        # Cloud-init provisioning
        mode = self.resolved.cloud_init_mode

        provisioner = VMProvisioner(
            rootfs_path=self.rootfs_path,
            provisioner_type=self.resolved.provisioner,
            fs_type=self.resolved.image.fs_type,
            root_uid=self.resolved.root_uid,
            root_gid=self.resolved.root_gid,
            user_uid=self.resolved.user_uid,
            user_gid=self.resolved.user_gid,
        )
        provisioner.resize(self.resolved.disk_size_bytes)

        logger.info(
            "[timing] provisioner_setup: %.3fs",
            time.perf_counter() - _step_start,
        )
        _step_start = time.perf_counter()

        if mode == CloudInitMode.OFF:
            provisioner.set_hostname(self.resolved.name)
            provisioner.inject_dns(dns_server=self.resolved.dns_server)
            provisioner.setup_ssh(self.resolved.user, self._ssh_pubkey_contents)
            provisioner.disable_cloud_init()
            self.mark_created("cloud-init-off")

        elif mode == CloudInitMode.INJECT:
            ci_config = CloudInitProvisionConfig(
                mode=mode,
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
                ssh_pubkeys=self._ssh_pubkey_contents,
                custom_user_data_path=self.resolved.custom_user_data_path,
                nocloud_net_port=self.resolved.nocloud_net_port,
                cloud_init_iso_path=self.resolved.cloud_init_iso_path,
                keep_cloud_init_iso=self.resolved.keep_cloud_init_iso,
                cloud_init_iso_name=self.resolved.cloud_init_iso_name,
                nocloud_port_range_start=self.resolved.nocloud_port_range_start,
                nocloud_port_range_end=self.resolved.nocloud_port_range_end,
                nocloud_max_port_retries=self.resolved.nocloud_max_port_retries,
            )
            ci_provisioner = CloudInitProvisioner(ci_config)
            self.cloud_init_result = ci_provisioner.provision()

            provisioner.inject_cloud_init(ci_config.cloud_init_dir)
            self.mark_created("cloud-init-inject")

        else:  # ISO or NET
            ci_config = CloudInitProvisionConfig(
                mode=mode,
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
                ssh_pubkeys=self._ssh_pubkey_contents,
                custom_user_data_path=self.resolved.custom_user_data_path,
                nocloud_net_port=self.resolved.nocloud_net_port,
                cloud_init_iso_path=self.resolved.cloud_init_iso_path,
                keep_cloud_init_iso=self.resolved.keep_cloud_init_iso,
                cloud_init_iso_name=self.resolved.cloud_init_iso_name,
                nocloud_port_range_start=self.resolved.nocloud_port_range_start,
                nocloud_port_range_end=self.resolved.nocloud_port_range_end,
                nocloud_max_port_retries=self.resolved.nocloud_max_port_retries,
            )
            ci_provisioner = CloudInitProvisioner(ci_config)
            self.cloud_init_result = ci_provisioner.provision()

            if mode == CloudInitMode.ISO:
                self.mark_created("cloud-init-iso")
            else:
                self.mark_created("cloud-init-net")

        # Fix fstab for Firecracker (superfloppy /dev/vda layout)
        provisioner.fix_fstab()

        # Execute all queued operations
        provisioner.run()

        logger.info(
            "[timing] provisioner_run: %.3fs", time.perf_counter() - _step_start
        )
        _step_start = time.perf_counter()

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="firecracker",
                    status="running",
                    message="Starting Firecracker microVM...",
                )
            )

        # Firecracker
        fc_config = self.build_firecracker_config()
        if fc_config is None:
            raise VMCreateError("Firecracker config is not set in context")

        firecracker_spawner = FirecrackerSpawner(fc_config)
        self.set_firecracker_manager(firecracker_spawner)
        firecracker_spawner.write_to_file()
        self.mark_created("firecracker")

        logger.info(
            "[timing] firecracker_config: %.3fs",
            time.perf_counter() - _step_start,
        )
        _step_start = time.perf_counter()

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
        _console_start = time.perf_counter()
        if self.resolved.enable_console:
            from mvmctl.core.console._controller import ConsoleController

            self.relay = ConsoleController(
                vm_id=self.vm_id,
                vm_dir=self.vm_dir,
                vm_name=self.resolved.name,
                socket_filename=self.resolved.console_socket_filename,
                pid_filename=self.resolved.console_pid_filename,
            )
            self.relay.create_pty()
        _console_elapsed = time.perf_counter() - _console_start

        # Set relay config on the FirecrackerConfig (shared via spawner._config)
        fc_config.relay_enabled = self.relay is not None
        fc_config.relay_client_fd = (
            self.relay.client_fd if self.relay is not None else None
        )

        self.fc_manager.spawn()
        logger.info(
            "[timing] firecracker_spawn: %.3fs",
            time.perf_counter() - _step_start,
        )
        _step_start = time.perf_counter()

        # Start console relay if enabled
        if self.resolved.enable_console and self.relay is not None:
            self.relay.close_client_fd()
            self.relay.start()
            self.mark_created("console_relay")

        logger.info("[timing] console_setup: %.3fs", _console_elapsed)

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="complete",
                    status="complete",
                    message="VM created successfully",
                )
            )

        logger.info("[timing] total: %.3fs", time.perf_counter() - _t0)

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
            log_level=self.resolved.log_level,
            log_filename=self.resolved.log_filename,
            serial_output_filename=self.resolved.serial_output_filename,
            metrics_filename=self.resolved.metrics_filename,
            api_socket_filename=self.resolved.api_socket_filename,
            pid_filename=self.resolved.pid_filename,
            config_filename=self.resolved.config_filename,
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

        now = datetime.now(tz=UTC)
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
            ssh_keys=[k.id for k in self.resolved.ssh_keys],
            ssh_user=self.resolved.user,
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

    @classmethod
    def for_respawn(
        cls, vm: VMInstanceItem, *, snapshot_mode: bool = False
    ) -> VMCreateContext:
        """Create a VMCreateContext for respawning a stopped VM from its stored state."""
        from mvmctl.core.cloudinit._provisioner import CloudInitProvisionResult

        ctx = cls(name=vm.name, vm_id=vm.id, vm_dir=vm.vm_dir)
        ctx._vm = vm
        ctx._snapshot_mode = snapshot_mode
        ctx.guest_ip = vm.ipv4 or ""
        ctx.guest_mac = vm.mac or ""
        ctx.tap_name = vm.tap_device or ""
        ctx.rootfs_path = (
            vm.vm_dir / vm.rootfs_path if vm.rootfs_path else Path()
        )

        # Build resolved from VM state — makes build_firecracker_config() work unchanged
        ctx.resolved = VMCreateRequest.from_vm(vm)

        # Fabricate cloud_init_result for build_firecracker_config()
        cloud_init_mode = (
            CloudInitMode(vm.cloud_init_mode)
            if vm.cloud_init_mode
            else CloudInitMode.OFF
        )
        iso_path = vm.vm_dir / "cloud-init" / "seed.iso"
        nocloud_url = (
            f"http://{vm.network.ipv4_gateway}:{vm.nocloud_net_port}/"
            if vm.nocloud_net_port and vm.network and vm.network.ipv4_gateway
            else None
        )
        ctx.cloud_init_result = CloudInitProvisionResult(
            mode=cloud_init_mode,
            iso_path=iso_path if iso_path.exists() else None,
            nocloud_url=nocloud_url,
        )

        return ctx

    def respawn_execute(self) -> None:
        """Execute the respawn flow for a stopped VM.

        Handles: nocloud-net restart, force-kill old process, TAP re-ensure,
        then delegates to spawn_only() for config building + spawn.
        DB updates (pid, status) are handled by the caller.
        """
        from mvmctl.services.nocloud_server.manager import (
            NoCloudNetServerManager,
        )

        if self._vm is None:
            raise VMCreateError("VM not set on VMCreateContext for respawn")

        if self._vm.network is None:
            raise VMNotFoundError(
                f"Network not found for VM '{self._vm.name}' (ID: {self._vm.network_id})"
            )

        cloud_init_mode = (
            self.cloud_init_result.mode if self.cloud_init_result else None
        )

        # ── Restart nocloud-net server if needed ──
        if cloud_init_mode == CloudInitMode.NET:
            nocloud_manager = NoCloudNetServerManager(
                id=self._vm.id,
                path=self._vm.vm_dir,
                name=self._vm.name,
                ipv4_gateway=self._vm.network.ipv4_gateway,
                port=self._vm.nocloud_net_port or 0,
            )
            if self._vm.nocloud_net_pid:
                try:
                    os.kill(self._vm.nocloud_net_pid, 0)
                except (ProcessLookupError, PermissionError):
                    nocloud_manager.start()
            else:
                nocloud_manager.start()

        # ── Force-kill any remaining Firecracker process ──
        if self._vm.pid:
            from mvmctl.utils._system import ProcessSignalHandler

            handler = ProcessSignalHandler(self._vm.pid, is_child=False)
            if not handler.kill_and_wait():
                logger.warning(
                    "Failed to kill old Firecracker process %d for VM '%s'",
                    self._vm.pid,
                    self._vm.name,
                )

        # ── Re-ensure TAP device exists before spawning ──
        net_service = NetworkService(NetworkRepository(self._db))
        bridge_addr = NetworkUtils.compute_bridge_address(
            self._vm.network.ipv4_gateway, self._vm.network.subnet
        )
        net_service.ensure_bridge(self._vm.network.bridge, bridge_addr)
        net_service.ensure_tap(
            self._vm.tap_device,
            self._vm.network.bridge,
            network_id=self._vm.network.id,
        )

        # ── Build config and spawn ──
        fc_config = self.build_firecracker_config()
        if fc_config is None:
            raise VMCreateError("Firecracker config is not set in context")
        fc_config.snapshot_mode = self._snapshot_mode

        spawner = FirecrackerSpawner(fc_config)
        spawner.write_to_file()
        spawner.spawn(wait_for_socket=True)

        if spawner.pid is None:
            raise MVMError("Failed to spawn Firecracker process")

        self.fc_manager = spawner


class VMOperation:
    @staticmethod
    def _execute_create(
        resolved: ResolvedVMCreateInput,
        *,
        audit_action: str,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> VMInstanceItem:
        """Execute VM creation from already-resolved inputs."""
        HostPrivilegeHelper.check_privileges(
            "/usr/sbin/ip", f"create VM '{resolved.name}'"
        )
        db = Database()
        vm_repo = VMRepository(db)
        max_vms_val = int(SettingsService.resolve(db, "settings.vm", "max_vms"))
        if vm_repo.count() >= max_vms_val:
            raise MVMError(
                f"VM limit reached ({max_vms_val}). "
                f"Remove existing VMs before creating new ones."
            )

        ctx = VMCreateContext(
            name=resolved.name,
            vm_id=resolved.vm_id,
            vm_dir=resolved.vm_dir,
            db=db,
            on_progress=on_progress,
        )
        ctx.set_resolved(resolved)

        with SigtermContext(lambda: ctx.cleanup()):
            try:
                ctx.execute()
                vm_instance = ctx.to_model()
                if vm_instance is None:
                    raise VMCreateError("Failed to create VM instance model")
                vm_repo.upsert(vm_instance)
                AuditLog.log(audit_action, context=f"name={resolved.name}")
            except Exception:
                if resolved.skip_cleanup:
                    logger.warning(
                        "VM creation failed but --skip-cleanup is active. Resources left at %s. "
                        "Manually clean with: mvm vm rm %s",
                        ctx.vm_dir,
                        resolved.name,
                    )
                else:
                    ctx.cleanup()
                raise

        return vm_instance

    @staticmethod
    def create(
        inputs: VMCreateInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[VMInstanceItem] | NeedsInteraction:
        try:
            db = Database()
            ctx = VMCreateContext(name=inputs.name)
            request = VMCreateRequest(
                vm_id=ctx.vm_id, vm_dir=ctx.vm_dir, inputs=inputs, db=db
            )
            resolved = request.resolve()
            vm_instance = VMOperation._execute_create(
                resolved,
                audit_action="vm.create",
                on_progress=on_progress,
            )
            return OperationResult(
                status="success",
                code="vm.created",
                item=vm_instance,
                message=f"VM '{inputs.name}' created",
            )
        except MVMError as e:
            if "limit" in str(e).lower():
                return OperationResult(
                    status="error",
                    code="vm.limit_reached",
                    message=str(e),
                )
            return OperationResult(
                status="error",
                code="vm.create_failure",
                message=str(e),
                exception=e,
            )
        except Exception as e:
            return OperationResult(
                status="failure",
                code="vm.create_failure",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def remove(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Remove one or more VMs."""
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "Remove VM")

        db = Database()
        resolver = VMRequest(inputs=inputs, db=db)
        resolved = resolver.resolve()

        repo = VMRepository(db)
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                vm_dir = CacheUtils.get_vm_dir(vm.id)

                controller = VMController(vm, repo)
                controller.stop(force=resolved.force)

                # Defense-in-depth: force-kill if stop() silently left the
                # Firecracker process alive (e.g., stale socket prevented
                # ACPI shutdown, or PermissionError on SIGTERM).
                if vm.pid and is_process_running(vm.pid):
                    try:
                        os.kill(vm.pid, signal.SIGKILL)
                    except (ProcessLookupError, PermissionError, OSError):
                        pass

                VMOperation._perform_removal_cleanup(vm, vm.network_id)

                # Deregister from DB and remove directory
                repo.delete(vm.id)
                if vm_dir.exists():
                    shutil.rmtree(vm_dir)

                AuditLog.log("vm.remove", changes={"name": vm.name})

                results.append(
                    OperationResult(
                        status="success",
                        code="vm.removed",
                        item=vm,
                        message=f"VM '{vm.name}' removed",
                    )
                )
            except Exception as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.remove_failed",
                        item=vm,
                        message=f"Failed to remove VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def list_all(
        status: VMStatus | list[VMStatus] | None = None,
    ) -> list[VMInstanceItem]:
        """
        List all VMs, optionally filtered by status.

        Enriches each VM with the ``network`` relation via ``VMResolver``
        (batch-resolved, no N+1).

        Args:
            status: Optional status filter. Single status or list of statuses.
                    If None (default), all VMs are returned.

        Returns:
            List of VMInstanceItem records with resolved relations.

        """
        from mvmctl.core.vm._resolver import VMResolver

        db = Database()
        repo = VMRepository(db)
        if status is not None:
            vms = repo.list_by_status(status)
        else:
            vms = repo.list_all()

        if vms:
            VMResolver(repo, include=["network"])._enrich(vms)

        return vms

    @staticmethod
    def to_json(vms: list[VMInstanceItem]) -> list[dict[str, Any]]:
        """
        Convert enriched VM model list to JSON-serializable dicts.

        Relies on ``list_all()`` having already populated ``vm.network``
        with the resolved ``NetworkItem`` — no additional DB queries.

        Args:
            vms: List of VMInstanceItem records (must have network enriched).

        Returns:
            List of VM dicts suitable for JSON serialization.

        """
        return [
            {
                "id": vm.id,
                "name": vm.name,
                "status": vm.status,
                "pid": vm.pid,
                "exit_code": vm.exit_code,
                "ipv4": vm.ipv4,
                "mac": vm.mac,
                "network_id": vm.network_id,
                "network_name": vm.network.name if vm.network else None,
                "network_subnet": vm.network.subnet if vm.network else None,
                "network_bridge": vm.network.bridge if vm.network else None,
                "network_gateway": vm.network.ipv4_gateway
                if vm.network
                else None,
                "tap_device": vm.tap_device,
                "image_id": vm.image_id,
                "kernel_id": vm.kernel_id,
                "binary_id": vm.binary_id,
                "vcpu_count": vm.vcpu_count,
                "mem_size_mib": vm.mem_size_mib,
                "disk_size_mib": vm.disk_size_mib,
                "api_socket_path": vm.api_socket_path,
                "config_path": vm.config_path,
                "cloud_init_mode": vm.cloud_init_mode,
                "rootfs_path": vm.rootfs_path,
                "rootfs_suffix": vm.rootfs_suffix,
                "enable_pci": vm.enable_pci,
                "enable_logging": vm.enable_logging,
                "enable_metrics": vm.enable_metrics,
                "enable_console": vm.enable_console,
                "created_at": vm.created_at,
                "updated_at": vm.updated_at,
                "relay_socket_path": vm.relay_socket_path,
                "process_start_time": vm.process_start_time,
                "nocloud_net_port": vm.nocloud_net_port,
                "nocloud_net_pid": vm.nocloud_net_pid,
                "relay_pid": vm.relay_pid,
                "log_path": vm.log_path,
                "serial_output_path": vm.serial_output_path,
                "lsm_flags": vm.lsm_flags,
                "boot_args": vm.boot_args,
                "ssh_keys": vm.ssh_keys,
                "ssh_user": vm.ssh_user,
            }
            for vm in vms
        ]

    @staticmethod
    def get(inputs: VMInput) -> VMInstanceItem:
        """Get a single VM by identifier."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        return resolved.vms[0]

    @staticmethod
    def inspect(inputs: VMInput, tree: bool = False) -> dict[str, Any]:
        """Inspect a VM with enriched data and optional tree output."""
        db = Database()
        resolved = VMRequest(inputs=inputs, db=db).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]

        # Resolve asset names
        image = ImageRepository(db).get(vm.image_id) if vm.image_id else None
        kernel = (
            KernelRepository(db).get(vm.kernel_id) if vm.kernel_id else None
        )
        network = (
            NetworkRepository(db).get(vm.network_id) if vm.network_id else None
        )
        binary = (
            BinaryRepository(db).get(vm.binary_id) if vm.binary_id else None
        )

        # Console relay status
        relay_running = False
        relay_pid = vm.relay_pid
        relay_socket_path = vm.relay_socket_path
        if vm.id and vm.relay_pid:
            from mvmctl.services.console_relay.manager import (
                ConsoleRelayManager,
            )

            relay_mgr = ConsoleRelayManager(
                id=vm.id,
                path=CacheUtils.get_vm_dir(vm.id),
                name=vm.name,
            )
            relay_running = relay_mgr.is_running()

        # Filesystem paths
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        rootfs_path = vm_dir / f"rootfs.{vm.rootfs_suffix}"
        config_path = vm_dir / vm.config_path if vm.config_path else None
        log_path = vm_dir / vm.log_path if vm.log_path else None
        serial_output_path = (
            vm_dir / vm.serial_output_path if vm.serial_output_path else None
        )

        if tree:
            return {
                "vm": {
                    "name": vm.name,
                    "id": vm.id,
                    "status": vm.status,
                    "pid": vm.pid,
                    "exit_code": vm.exit_code,
                    "ssh_keys": vm.ssh_keys,
                    "ssh_user": vm.ssh_user,
                },
                "resources": {
                    "vcpus": vm.vcpu_count,
                    "mem": vm.mem_size_mib,
                    "disk": vm.disk_size_mib,
                },
                "networking": {
                    "ipv4": vm.ipv4,
                    "mac": vm.mac,
                    "network_name": network.name if network else None,
                    "tap_device": vm.tap_device,
                },
                "assets": {
                    "image_name": image.os_name if image else None,
                    "kernel_version": kernel.version if kernel else None,
                    "binary_name": binary.name if binary else None,
                },
                "filesystem": {
                    "vm_dir": str(vm_dir),
                    "rootfs_path": str(rootfs_path),
                    "config_path": str(config_path) if config_path else None,
                    "log_path": str(log_path) if log_path else None,
                    "serial_output_path": str(serial_output_path)
                    if serial_output_path
                    else None,
                },
                "console": {
                    "relay_running": relay_running,
                    "relay_pid": relay_pid,
                    "relay_socket_path": relay_socket_path,
                },
            }

        # Flat mode — enriched dict
        return {
            "id": vm.id,
            "name": vm.name,
            "status": vm.status,
            "ipv4": vm.ipv4,
            "mac": vm.mac,
            "vcpus": vm.vcpu_count,
            "mem_mib": vm.mem_size_mib,
            "disk_mib": vm.disk_size_mib,
            "image_id": vm.image_id,
            "image_name": image.os_name if image else None,
            "kernel_id": vm.kernel_id,
            "kernel_version": kernel.version if kernel else None,
            "network_id": vm.network_id,
            "network_name": network.name if network else None,
            "binary_id": vm.binary_id,
            "binary_name": binary.name if binary else None,
            "tap_device": vm.tap_device,
            "pid": vm.pid,
            "exit_code": vm.exit_code,
            "created_at": vm.created_at,
            "updated_at": vm.updated_at,
            "cloud_init_mode": vm.cloud_init_mode,
            "enable_pci": vm.enable_pci,
            "enable_console": vm.enable_console,
            "enable_logging": vm.enable_logging,
            "enable_metrics": vm.enable_metrics,
            "vm_dir": str(vm_dir),
            "rootfs_path": str(rootfs_path),
            "config_path": str(config_path) if config_path else None,
            "log_path": str(log_path) if log_path else None,
            "serial_output_path": str(serial_output_path)
            if serial_output_path
            else None,
            "relay_running": relay_running,
            "relay_pid": relay_pid,
            "relay_socket_path": relay_socket_path,
            "ssh_keys": vm.ssh_keys,
            "ssh_user": vm.ssh_user,
        }

    @staticmethod
    def _respawn_firecracker(
        vm: VMInstanceItem, snapshot_mode: bool = False
    ) -> None:
        """Re-spawn Firecracker process for a stopped VM.

        After vm stop, the Firecracker process is dead and the API socket is
        gone. This method:

        1. Enriches VM with binary, kernel, image, network via VMResolver
        2. Delegates to VMCreateContext for config building + spawn
        3. Updates PID and process_start_time in DB and in-memory VM object
        """
        db = Database()

        # Enrich VM with resolved relations
        repo = VMRepository(db)
        resolver = VMResolver(
            repo, include=["binary", "kernel", "image", "network"]
        )
        resolver._enrich([vm])

        if vm.binary is None:
            raise VMNotFoundError(
                f"Binary not found for VM '{vm.name}' (ID: {vm.binary_id})"
            )
        if vm.kernel is None:
            raise VMNotFoundError(
                f"Kernel not found for VM '{vm.name}' (ID: {vm.kernel_id})"
            )
        if vm.image is None:
            raise VMNotFoundError(
                f"Image not found for VM '{vm.name}' (ID: {vm.image_id})"
            )
        if vm.network is None:
            raise VMNotFoundError(
                f"Network not found for VM '{vm.name}' (ID: {vm.network_id})"
            )

        # Delegate to VMCreateContext for config building + spawn
        ctx = VMCreateContext.for_respawn(vm, snapshot_mode=snapshot_mode)
        ctx.respawn_execute()
        if ctx.fc_manager is None or ctx.fc_manager.pid is None:
            raise MVMError(
                f"Failed to spawn Firecracker process for VM '{vm.name}'"
            )

        # Update PID and process_start_time in DB
        repo.update_process_info(
            vm.id, ctx.fc_manager.pid, ctx.fc_manager.process_start_time
        )

        # Update status
        if snapshot_mode:
            new_status = VMStatus.PAUSED.value
        else:
            new_status = VMStatus.RUNNING.value
        repo.update_status(vm.id, new_status)

        # Update in-memory VM object
        vm.pid = ctx.fc_manager.pid
        vm.process_start_time = ctx.fc_manager.process_start_time
        vm.status = new_status

    @staticmethod
    def start(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Start one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(VMRepository(Database()))
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                # If VM is stopped, respawn Firecracker process
                # (this also updates status to RUNNING — FC auto-boots)
                if vm.status == VMStatus.STOPPED.value:
                    VMOperation._respawn_firecracker(vm)
                else:
                    # For paused VMs, just send InstanceStart via API
                    service.start(vm)
                AuditLog.log("vm.start", context=f"name={vm.name}")
                results.append(
                    OperationResult(
                        status="success",
                        code="vm.started",
                        item=vm,
                        message=f"VM '{vm.name}' started",
                    )
                )
            except MVMError as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.start_failed",
                        item=vm,
                        message=f"Failed to start VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def stop(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Stop one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(VMRepository(Database()))
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                service.stop(vm, force=resolved.force)

                # Defense-in-depth: force-kill if stop() silently left the
                # Firecracker process alive (non-child process cannot be
                # reaped via waitpid, causing _reaped flag to prematurely
                # short-circuit the SIGTERM/SIGKILL cascade).
                if vm.pid and is_process_running(vm.pid):
                    try:
                        os.kill(vm.pid, signal.SIGKILL)
                    except OSError:
                        pass

                AuditLog.log("vm.stop", context=f"name={vm.name}")
                results.append(
                    OperationResult(
                        status="success",
                        code="vm.stopped",
                        item=vm,
                        message=f"VM '{vm.name}' stopped",
                    )
                )
            except (MVMError, NetworkError) as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.stop_failed",
                        item=vm,
                        message=f"Failed to stop VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def reboot(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Reboot one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                # Stop the VM first (kills the firecracker process)
                controller = VMController(vm, VMRepository(Database()))
                controller.stop(force=resolved.force)
                # After stop, respawn a fresh firecracker process.
                # _respawn_firecracker handles creating a new process,
                # waiting for the API socket, and updating the DB.
                VMOperation._respawn_firecracker(vm)
                AuditLog.log("vm.reboot", context=f"name={vm.name}")
                results.append(
                    OperationResult(
                        status="success",
                        code="vm.rebooted",
                        item=vm,
                        message=f"VM '{vm.name}' rebooted",
                    )
                )
            except MVMError as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.reboot_failed",
                        item=vm,
                        message=f"Failed to reboot VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def pause(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Pause one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(VMRepository(Database()))
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                service.pause(vm)
                AuditLog.log("vm.pause", context=f"name={vm.name}")
                results.append(
                    OperationResult(
                        status="success",
                        code="vm.paused",
                        item=vm,
                        message=f"VM '{vm.name}' paused",
                    )
                )
            except MVMError as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.pause_failed",
                        item=vm,
                        message=f"Failed to pause VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def resume(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Resume one or more VMs."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        service = VMService(VMRepository(Database()))
        results: list[OperationResult[VMInstanceItem]] = []

        for vm in resolved.vms:
            try:
                service.resume(vm)
                AuditLog.log("vm.resume", context=f"name={vm.name}")
                results.append(
                    OperationResult(
                        status="success",
                        code="vm.resumed",
                        item=vm,
                        message=f"VM '{vm.name}' resumed",
                    )
                )
            except MVMError as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="vm.resume_failed",
                        item=vm,
                        message=f"Failed to resume VM '{vm.name}': {e}",
                        exception=e,
                    )
                )

        return BatchResult(items=results)

    @staticmethod
    def snapshot(
        inputs: VMInput, mem_out: Path, state_out: Path
    ) -> OperationResult[VMInstanceItem]:
        """Snapshot a single VM's memory and state."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]
        try:
            controller = VMController(vm, VMRepository(Database()))
            controller.snapshot(mem_out, state_out)
            AuditLog.log("vm.snapshot", context=f"name={vm.name}")
            return OperationResult(
                status="success",
                code="vm.snapshot_created",
                item=vm,
                message=f"VM '{vm.name}' snapshot saved",
            )
        except MVMError as e:
            return OperationResult(
                status="error",
                code="vm.snapshot_failed",
                item=vm,
                message=f"Failed to snapshot VM '{vm.name}': {e}",
                exception=e,
            )
        except Exception as e:
            return OperationResult(
                status="failure",
                code="vm.snapshot_failed",
                item=vm,
                message=f"Failed to snapshot VM '{vm.name}': {e}",
                exception=e,
            )

    @staticmethod
    def load_snapshot(
        inputs: VMInput,
        mem_in: Path,
        state_in: Path,
        resume_after: bool | None = None,
    ) -> OperationResult[VMInstanceItem]:
        """Load a snapshot into a single VM."""
        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]
        try:
            # If the VM is stopped, we need to spawn a fresh Firecracker
            # process in pre-boot (snapshot) mode so the API socket is
            # available for PUT /snapshot/load.
            if vm.status == VMStatus.STOPPED.value:
                VMOperation._respawn_firecracker(vm, snapshot_mode=True)
                # Re-read the updated vm object (pid, status now PAUSED)
                repo = VMRepository(Database())
                updated = repo.get(vm.id)
                if updated:
                    vm = updated

            controller = VMController(vm, VMRepository(Database()))
            controller.load_snapshot(
                mem_in,
                state_in,
                resume_after if resume_after is not None else False,
            )
            AuditLog.log("vm.load_snapshot", context=f"name={vm.name}")
            return OperationResult(
                status="success",
                code="vm.snapshot_loaded",
                item=vm,
                message=f"Snapshot loaded for VM '{vm.name}'",
            )
        except MVMError as e:
            return OperationResult(
                status="error",
                code="vm.load_snapshot_failed",
                item=vm,
                message=f"Failed to load snapshot for VM '{vm.name}': {e}",
                exception=e,
            )
        except Exception as e:
            return OperationResult(
                status="failure",
                code="vm.load_snapshot_failed",
                item=vm,
                message=f"Failed to load snapshot for VM '{vm.name}': {e}",
                exception=e,
            )

    @staticmethod
    def export(inputs: VMInput) -> VMExportConfig:
        """
        Export a VM's configuration as a portable VMExportConfig.

        Resolves the VM by any identifier (name, ID, IP, MAC) and queries
        the database for related asset metadata.
        """
        db = Database()
        resolved = VMRequest(inputs=inputs, db=db).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]

        image = ImageRepository(db).get(vm.image_id) if vm.image_id else None
        kernel = (
            KernelRepository(db).get(vm.kernel_id) if vm.kernel_id else None
        )
        binary = (
            BinaryRepository(db).get(vm.binary_id) if vm.binary_id else None
        )
        network = (
            NetworkRepository(db).get(vm.network_id) if vm.network_id else None
        )

        return VMExportConfig(
            name=vm.name,
            compute=VMExportComputeConfig(
                vcpus=vm.vcpu_count,
                mem=vm.mem_size_mib,
            ),
            image=VMExportImageConfig(
                os_slug=image.os_slug if image else None,
                arch=image.arch if image else None,
                disk_size=f"{vm.disk_size_mib}M" if vm.disk_size_mib else None,
            ),
            kernel=VMExportKernelConfig(
                version=kernel.version if kernel else None,
                arch=kernel.arch if kernel else None,
                type=kernel.type if kernel else None,
            ),
            binary=VMExportBinaryConfig(
                name=binary.name if binary else "firecracker",
                version=binary.version if binary else None,
            ),
            network=VMExportNetworkConfig(
                name=network.name if network else None,
                subnet=network.subnet if network else None,
                ipv4_gateway=network.ipv4_gateway if network else None,
                nat_enabled=network.nat_enabled if network else None,
                nat_gateways=network.nat_gateways if network else None,
                ip=vm.ipv4,
                mac=vm.mac,
            ),
            boot=VMExportBootConfig(
                args=vm.boot_args,
                enable_console=vm.enable_console,
            ),
            firecracker=VMExportFirecrackerConfig(
                enable_pci=vm.enable_pci,
                lsm_flags=vm.lsm_flags,
            ),
            cloud_init=VMExportCloudInitConfig(
                mode=vm.cloud_init_mode or "inject",
                user="root",
                nocloud_net_port=vm.nocloud_net_port,
            ),
        )

    @staticmethod
    def import_(
        inputs: VMImportInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[VMInstanceItem] | NeedsInteraction:
        """Create a VM from a portable export config file."""
        try:
            db = Database()
            resolved = VMImportRequest(inputs=inputs, db=db).resolve()
            vm_instance = VMOperation._execute_create(
                resolved,
                audit_action="vm.import",
                on_progress=on_progress,
            )
            return OperationResult(
                status="success",
                code="vm.imported",
                item=vm_instance,
                message=f"VM imported from {inputs.config_path}",
            )
        except MVMError as e:
            return OperationResult(
                status="error",
                code="vm.import_failed",
                message=str(e),
                exception=e,
            )
        except Exception as e:
            return OperationResult(
                status="failure",
                code="vm.import_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def _perform_removal_cleanup(
        vm: VMInstanceItem,
        network_id: str | None,
    ) -> None:
        """Clean up all VM resources: console relay, TAP device, IP lease, SSH known hosts."""
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
            if tap_name and network_id is not None:
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
            except Exception as exc:
                logger.warning(
                    "Failed to release network IP for VM '%s': %s",
                    vm.name,
                    exc,
                )

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

        if vm.ipv4:
            try:
                import subprocess

                subprocess.run(
                    ["ssh-keygen", "-R", vm.ipv4],
                    capture_output=True,
                    check=False,
                )
            except FileNotFoundError:
                pass


__all__ = [
    "VMCreateContext",
    "VMOperation",
]
