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
from mvmctl.core.volume._controller import VolumeController
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.core.volume._service import VolumeService
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
    VolumeStatus,
)
from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)
from mvmctl.utils._system import SigtermContext, is_process_running, run_cmd
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.common import CacheUtils, CommonUtils
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.network import NetworkUtils
from mvmctl.utils.timinglog import timed
from mvmctl.utils.version import VersionGate

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

        with timed("network_setup", self.name, self.vm_id):
            net_repo = NetworkRepository(self._db)
            net_service = NetworkService(net_repo)
            bridge_addr = NetworkUtils.compute_bridge_address(
                self.resolved.network.ipv4_gateway,
                self.resolved.network.subnet,
            )
            net_service.ensure_bridge(self.resolved.network.bridge, bridge_addr)

            with net_service.batch():
                # IP Lease
                lease_repo = LeaseRepository(self._db)
                lease_manager = LeaseService(self.resolved.network, lease_repo)
                if self.resolved.requested_guest_ip:
                    self.guest_ip = lease_manager.lease_specific(
                        self.resolved.requested_guest_ip, self.vm_id
                    )
                else:
                    self.guest_ip = lease_manager.lease(self.vm_id)

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
                    subnet=self.resolved.network.subnet,
                )

            self.mark_created("network_tap")
            net_service.flush_arp(self.resolved.network.bridge)

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="rootfs",
                    status="running",
                    message="Copying root filesystem...",
                )
            )

        # Rootfs
        with timed("image_clone", self.name, self.vm_id):
            self.clone_image()
            self.mark_created("rootfs")

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
        with timed("provisioner_setup", self.name, self.vm_id):
            provisioner.resize(self.resolved.disk_size_bytes)

        with timed("provisioner_run", self.name, self.vm_id):
            # Common operations for OFF and INJECT modes — SSH keys, hostname, DNS
            if mode in (CloudInitMode.OFF, CloudInitMode.INJECT):
                provisioner.set_hostname(self.resolved.name)
                provisioner.inject_dns(dns_server=self.resolved.dns_server)
                provisioner.setup_ssh(
                    self.resolved.user, self._ssh_pubkey_contents
                )

            if mode == CloudInitMode.OFF:
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

            # Deblob (OS cache cleanup) unless explicitly skipped
            # Pass pre-detected distro to eliminate redundant OS detection
            if not self.resolved.skip_deblob:
                provisioner.deblob(os_type=self.resolved.image.distro)

            # Fix fstab for Firecracker (superfloppy /dev/vda layout)
            provisioner.fix_fstab()

            # Execute all queued operations
            provisioner.run()

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="firecracker",
                    status="running",
                    message="Starting Firecracker microVM...",
                )
            )

        # Firecracker
        with timed("firecracker_config", self.name, self.vm_id):
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
        with timed("console_setup", self.name, self.vm_id):
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

        # Set relay config on the FirecrackerConfig (shared via spawner._config)
        fc_config.relay_enabled = self.relay is not None
        fc_config.relay_client_fd = (
            self.relay.client_fd if self.relay is not None else None
        )

        with timed("firecracker_spawn", self.name, self.vm_id):
            self.fc_manager.spawn()

        # Start console relay if enabled
        if self.resolved.enable_console and self.relay is not None:
            self.relay.close_client_fd()
            self.relay.start()
            self.mark_created("console_relay")

        if self._on_progress is not None:
            self._on_progress(
                ProgressEvent(
                    phase="complete",
                    status="complete",
                    message="VM created successfully",
                )
            )

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
            pci_enabled=self.resolved.pci_enabled,
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
            extra_drives=self.resolved.extra_drives,
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
            pci_enabled=self.resolved.pci_enabled,
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
        elif self.resolved and self.resolved.nocloud_net_port is not None:
            vm_instance.nocloud_net_port = self.resolved.nocloud_net_port

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
            subnet=self._vm.network.subnet,
        )

        # ── Build config and spawn ──
        fc_config = self.build_firecracker_config()
        if fc_config is None:
            raise VMCreateError("Firecracker config is not set in context")
        fc_config.snapshot_mode = self._snapshot_mode

        spawner = FirecrackerSpawner(fc_config)
        spawner.write_to_file()
        spawner.spawn()

        if spawner.pid is None:
            raise MVMError("Failed to spawn Firecracker process")

        self.fc_manager = spawner


class VMOperation:
    @staticmethod
    def prune(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[list[str]]:
        """Prune VMs based on their status.

        By default, prunes all VMs EXCEPT those in RUNNING or STARTING state.
        Use ``include_all=True`` to prune ALL VMs regardless of state.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: Prune ALL VMs including RUNNING and STARTING.

        Returns:
            OperationResult with item list of VM names that were removed.
        """
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune VMs")
        db = Database()
        vms = VMRepository(db).list_all()

        removed: list[str] = []
        for vm in vms:
            if vm.status in (VMStatus.RUNNING, VMStatus.STARTING):
                if not include_all:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._vm_input import VMInput

                    VMOperation.remove(
                        VMInput(identifiers=[vm.name], force=True)
                    )
                    removed.append(vm.name)
                except Exception as e:
                    logger.warning("Failed to remove VM %s: %s", vm.name, e)
            else:
                removed.append(vm.name)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} VM(s)",
            item=removed,
        )

    @staticmethod
    def _execute_create(
        resolved: ResolvedVMCreateInput,
        *,
        audit_action: str,
        on_progress: Callable[[ProgressEvent], None] | None = None,
        skip_limit_check: bool = False,
    ) -> VMInstanceItem:
        """Execute VM creation from already-resolved inputs."""
        HostPrivilegeHelper.check_privileges(
            "/usr/sbin/ip", f"create VM '{resolved.name}'"
        )
        db = Database()
        vm_repo = VMRepository(db)
        if not skip_limit_check:
            max_vms_val = int(
                SettingsService.resolve(db, "settings.vm", "max_vms")
            )
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

        with (
            timed("overall", resolved.name, resolved.vm_id),
            SigtermContext(lambda: ctx.cleanup()),
        ):
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
    ) -> OperationResult[list[VMInstanceItem]] | NeedsInteraction:
        try:
            db = Database()

            # Resolve shared state ONCE
            ctx = VMCreateContext(name=inputs.name)
            request = VMCreateRequest(
                vm_id=ctx.vm_id, vm_dir=ctx.vm_dir, inputs=inputs, db=db
            )
            resolved = request.resolve()

            count = inputs.count if inputs.count is not None else 1

            if count == 1:
                # Single VM — use existing logic but wrap in list
                vm_instance = VMOperation._execute_create(
                    resolved,
                    audit_action="vm.create",
                    on_progress=on_progress,
                )
                if resolved.volumes:
                    VolumeService(VolumeRepository(db)).set_volumes_state(
                        volumes=resolved.volumes,
                        state=VolumeStatus.ATTACHED,
                        vm_id=vm_instance.id,
                    )
                    vm_instance.volume_ids = [v.id for v in resolved.volumes]
                    VMRepository(db).upsert(vm_instance)
                return OperationResult(
                    status="success",
                    code="vm.created",
                    item=[vm_instance],
                    message=f"VM '{inputs.name}' created",
                )

            # BATCH PATH
            names = CommonUtils.generate_batch_names(inputs.name, count)

            # Pre-allocate: check name collisions (single query)
            existing = VMRepository(db).get_by_names(names)
            if existing:
                return OperationResult(
                    status="error",
                    code="vm.name_collision",
                    message=f"VM name(s) already exist: {', '.join(sorted(existing))}",
                )

            created_vms: list[VMInstanceItem] = []
            errors: list[str] = []

            for idx, name in enumerate(names):
                try:
                    # Generate unique vm_id and vm_dir for this VM
                    created_at = datetime.now()
                    vm_id = HashGenerator.vm(name, created_at.isoformat())
                    vm_dir = Path(CacheUtils.get_vm_dir(vm_id))

                    # Create per-VM resolved input
                    from dataclasses import replace

                    vm_resolved = replace(
                        resolved,
                        name=name,
                        vm_id=vm_id,
                        vm_dir=vm_dir,
                    )

                    def _batch_progress(
                        event: ProgressEvent,
                        n: str = name,
                        i: int = idx,
                        total: int = count,
                    ) -> None:
                        if on_progress is not None:
                            on_progress(
                                ProgressEvent(
                                    phase=event.phase,
                                    status=event.status,
                                    message=f"[{i + 1}/{total}] {n}: {event.message}",
                                )
                            )

                    vm_instance = VMOperation._execute_create(
                        vm_resolved,
                        audit_action="vm.create",
                        on_progress=_batch_progress,
                        skip_limit_check=True,
                    )
                    if vm_resolved.volumes:
                        VolumeService(VolumeRepository(db)).set_volumes_state(
                            volumes=vm_resolved.volumes,
                            state=VolumeStatus.ATTACHED,
                            vm_id=vm_instance.id,
                        )
                        vm_instance.volume_ids = [
                            v.id for v in vm_resolved.volumes
                        ]
                        VMRepository(db).upsert(vm_instance)
                    created_vms.append(vm_instance)

                except Exception as e:
                    errors.append(f"{name}: {e}")
                    if inputs.atomic and created_vms:
                        # Rollback: remove all successfully created VMs
                        for vm in created_vms:
                            try:
                                VMOperation.remove(
                                    VMInput(identifiers=[vm.name], force=True)
                                )
                            except Exception:
                                pass
                        return OperationResult(
                            status="error",
                            code="vm.atomic_failed",
                            message=f"Atomic creation failed at '{name}': {e}. "
                            f"All {len(created_vms)} previously created VMs have been removed.",
                        )

            if errors and not created_vms:
                return OperationResult(
                    status="error",
                    code="vm.create_failure",
                    message="; ".join(errors),
                )

            message = f"Created {len(created_vms)} VM(s): {', '.join(vm.name for vm in created_vms)}"
            if errors:
                message += f"\nFailed: {'; '.join(errors)}"

            return OperationResult(
                status="success" if not errors else "warning",
                code="vm.created_batch",
                item=created_vms,
                message=message,
            )

        except MVMError as e:
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

        if not resolved.vms:
            return BatchResult(
                items=[
                    OperationResult(
                        status="error",
                        code="vm.not_found",
                        message="No VMs found matching the given identifiers",
                    )
                ]
            )

        # Report any identifiers that couldn't be resolved
        unresolved_count = len(inputs.identifiers) - len(resolved.vms)
        if unresolved_count > 0:
            logger.warning(
                "VM rm: %d identifier(s) could not be resolved (out of %d)",
                unresolved_count,
                len(inputs.identifiers),
            )

        repo = VMRepository(db)
        results: list[OperationResult[VMInstanceItem]] = []
        if unresolved_count > 0:
            results.append(
                OperationResult(
                    status="error",
                    code="vm.not_found",
                    message=f"{unresolved_count} VM identifier(s) not found",
                )
            )

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

                # Detach any attached volumes before removing the VM record
                VolumeService(VolumeRepository(db)).set_volumes_state(
                    volumes=vm.volumes, state=VolumeStatus.AVAILABLE
                )

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
            VMResolver(repo, include=["network", "volumes"])._enrich(vms)

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
                "pci_enabled": vm.pci_enabled,
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
                    "image_name": image.name if image else None,
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
                "volumes": [
                    {
                        "id": v.id,
                        "name": v.name,
                        "size": v.size_bytes,
                        "format": v.format,
                        "status": v.status,
                    }
                    for v in (vm.volumes or [])
                ],
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
            "image_name": image.name if image else None,
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
            "nocloud_net_port": vm.nocloud_net_port,
            "nocloud_net_pid": vm.nocloud_net_pid,
            "pci_enabled": vm.pci_enabled,
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
            "volumes": [
                {
                    "id": v.id,
                    "name": v.name,
                    "size": v.size_bytes,
                    "format": v.format,
                    "status": v.status,
                }
                for v in (vm.volumes or [])
            ],
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
            repo, include=["binary", "kernel", "image", "network", "volumes"]
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
        from mvmctl.exceptions import MVMError

        resolved = VMRequest(inputs=inputs, db=Database()).resolve()
        if len(resolved.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved.vms[0]
        # Validate snapshot files exist before loading
        missing: list[str] = []
        if not mem_in.exists():
            missing.append(str(mem_in))
        if not state_in.exists():
            missing.append(str(state_in))
        if missing:
            paths = ", ".join(missing)
            raise MVMError(f"Snapshot file(s) not found: {paths}")

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
                type=image.type if image else None,
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
                pci_enabled=vm.pci_enabled,
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
                run_cmd(
                    ["ssh-keygen", "-R", vm.ipv4],
                    check=False,
                )
            except FileNotFoundError:
                pass

    @staticmethod
    def attach_volume(
        vm_inputs: VMInput, volume_name: str
    ) -> OperationResult[VMInstanceItem]:
        """Attach a volume to a running VM."""
        db = Database()
        vm_resolver = VMRequest(inputs=vm_inputs, db=db)
        resolved_vm = vm_resolver.resolve()
        if len(resolved_vm.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved_vm.vms[0]

        # HOTPLUG: temporarily disabled guard — hotplug requires running VM
        # if vm.status != VMStatus.STOPPED:
        #     raise VMCreateError(
        #         f"Cannot attach volume to VM '{vm.name}': "
        #         f"VM is in '{vm.status}' state, must be 'stopped'. "
        #         "Stop the VM first, then attach the volume, then start the VM again."
        #     )

        vol_repo = VolumeRepository(db)
        vol_resolver = VolumeResolver(vol_repo)
        vol = vol_resolver.resolve(volume_name)

        if vol.status != VolumeStatus.AVAILABLE:
            raise VMCreateError(f"Volume '{volume_name}' is not available")

        # Hotplug the drive on a running VM.
        if vm.status == VMStatus.RUNNING:
            # Version gate: hotplug requires Firecracker v1.16+
            binary_item = BinaryRepository(db).get(vm.binary_id)
            VersionGate.require(
                "firecracker",
                binary_item.version if binary_item else None,
                "1.16",
            )

            from mvmctl.core.vm._controller import VMController

            try:
                controller = VMController(entity=vm, repo=VMRepository(db))
                controller.attach_volume(vol)
            except Exception as exc:
                logger.warning("Hotplug failed for drive '%s': %s", vol.id, exc)

        controller = VolumeController(vol, vol_repo)
        controller.attach(vm.id)

        # Update VM's volume_ids to persist the attachment for next start.
        # Firecracker has no hot-plug — drives are configured on spawn, so
        # we persist volume_ids in the VM record so the next start includes
        # the volume as an extra drive in the Firecracker config.
        vm_volume_ids = list(vm.volume_ids) if vm.volume_ids else []
        if vol.id not in vm_volume_ids:
            vm_volume_ids.append(vol.id)
        vm.volume_ids = vm_volume_ids
        VMRepository(db).upsert(vm)

        return OperationResult(
            status="success",
            code="vm.volume_attached",
            item=vm,
            message=f"Volume '{volume_name}' attached to VM '{vm.name}'",
        )

    @staticmethod
    def detach_volume(
        vm_inputs: VMInput, volume_name: str
    ) -> OperationResult[VMInstanceItem]:
        """Detach a volume from a running VM."""
        db = Database()
        vm_resolver = VMRequest(inputs=vm_inputs, db=db)
        resolved_vm = vm_resolver.resolve()
        if len(resolved_vm.vms) != 1:
            raise VMNotFoundError("Expected exactly one VM identifier")
        vm = resolved_vm.vms[0]

        # HOTPLUG: temporarily disabled guard — hot-unplug requires running VM
        # if vm.status != VMStatus.STOPPED:
        #     raise VMCreateError(
        #         f"Cannot detach volume from VM '{vm.name}': "
        #         f"VM is in '{vm.status}' state, must be 'stopped'. "
        #         "Stop the VM first, then detach the volume, then start the VM again."
        #     )

        vol_repo = VolumeRepository(db)
        vol_resolver = VolumeResolver(vol_repo)
        vol = vol_resolver.resolve(volume_name)

        # Hot-unplug the drive from a running VM.
        if vm.status == VMStatus.RUNNING:
            # Version gate: hot-unplug requires Firecracker v1.16+
            binary_item = BinaryRepository(db).get(vm.binary_id)
            VersionGate.require(
                "firecracker",
                binary_item.version if binary_item else None,
                "1.16",
            )

            from mvmctl.core.key._resolver import KeyResolver
            from mvmctl.core.ssh._service import SSHService

            # Step 1: SSH into guest and remove the PCI device.
            if vm.ssh_keys and vm.ipv4:
                try:
                    key_resolver = KeyResolver()
                    ssh_key = key_resolver.by_id(vm.ssh_keys[0])
                    key_path = (
                        Path(ssh_key.private_key_path)
                        if ssh_key.private_key_path
                        else None
                    )
                    ssh_user = vm.ssh_user or "root"

                    ssh = SSHService(
                        ip=vm.ipv4,
                        user=ssh_user,
                        key_path=key_path,
                        timeout=10,
                    )
                    # Find the last Virtio block device BDF (the hotplugged one)
                    # and remove it. Using tail -1 to skip the root device.
                    cmd = ssh.build_command(
                        "lspci -D | grep 'Virtio.*block' | tail -1 | awk '{print $1}'"
                    )
                    result = run_cmd(cmd, capture=True, check=False, timeout=10)
                    bdf = (
                        result.stdout.strip() if result.returncode == 0 else ""
                    )
                    if bdf:
                        remove_cmd = ssh.build_command(
                            f"echo 1 > /sys/bus/pci/devices/{bdf}/remove"
                        )
                        run_cmd(
                            remove_cmd, capture=False, check=False, timeout=10
                        )
                        logger.info(
                            "Removed PCI device %s from guest for drive '%s'",
                            bdf,
                            vol.id,
                        )
                except Exception as exc:
                    logger.warning(
                        "SSH PCI removal for drive '%s' failed: %s",
                        vol.id,
                        exc,
                    )

            # Step 2: Call Firecracker API to delete the drive.
            from mvmctl.core.vm._controller import VMController

            try:
                controller = VMController(entity=vm, repo=VMRepository(db))
                controller.detach_volume(vol)
            except Exception as exc:
                logger.warning(
                    "Firecracker delete_drive failed for '%s': %s",
                    vol.id,
                    exc,
                )

        controller = VolumeController(vol, vol_repo)
        controller.detach()

        # Update VM's volume_ids to persist the detachment for next start.
        vm_volume_ids = list(vm.volume_ids) if vm.volume_ids else []
        if vol.id in vm_volume_ids:
            vm_volume_ids.remove(vol.id)
        vm.volume_ids = vm_volume_ids
        VMRepository(db).upsert(vm)

        return OperationResult(
            status="success",
            code="vm.volume_detached",
            item=vm,
            message=f"Volume '{volume_name}' detached from VM '{vm.name}'",
        )


__all__ = [
    "VMCreateContext",
    "VMOperation",
]
