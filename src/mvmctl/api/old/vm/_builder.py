"""VM Builder class"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.exceptions import VMBuilderError
from mvmctl.utils.fs import secure_mkdir
from src.mvmctl.api._internal._cloudinit._provisioner import (
    CloudInitProvisionConfig,
    CloudInitProvisioner,
    CloudInitProvisionResult,
)
from src.mvmctl.api._internal._image_manager import ImageManager
from src.mvmctl.api._internal._iptables_tracker import IPTablesTracker
from src.mvmctl.api._internal._network_ip_lease import NetworkIPLeaseManager
from src.mvmctl.api._internal._network_manager import NetworkManager
from src.mvmctl.api.vm._firecracker import FirecrackerManager
from src.mvmctl.api.vm._guestfs import GuestfsProvisioner
from src.mvmctl.api.vm._resolver import VMInputResolved
from src.mvmctl.core.mvm_db import MVMDatabase
from src.mvmctl.db.models import VMInstance
from src.mvmctl.models.cloud_init import CloudInitMode
from src.mvmctl.models.vm import VMStatus
from src.mvmctl.utils.fs import get_vm_dir_by_hash

if TYPE_CHECKING:
    from mvmctl.api.vm._console_relay import VMConsoleRelay

logger = logging.getLogger(__name__)


@dataclass
class VMBuilder:
    """Builder for VM creation - tracks state and spawns processes.

    Generates VM ID automatically on instantiation based on name.
    NOTE: PURE STATE TRACKER for creation. Does NOT call core modules directly
    except for spawn() which is a builder action.
    Core call sequencing stays in _orchestration.py (the orchestrator).
    """

    name: str
    vm_id: str
    vm_dir: Path
    rootfs_path: Path
    resolved: VMInputResolved | None = None

    fc_manager: FirecrackerManager | None = None
    relay: VMConsoleRelay | None = None
    # Stores final state of cloud-init, use this as reference
    cloud_init_result: CloudInitProvisionResult | None = None

    resources_created: dict[str, bool] = field(default_factory=dict)

    def __init__(self, name: str, db: MVMDatabase | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        created_at = datetime.now()
        self.vm_id = self._generate_vm_id(name, created_at)
        self.vm_dir = Path(get_vm_dir_by_hash(self.vm_id))
        self._db = db if db is not None else MVMDatabase()

    @staticmethod
    def _generate_vm_id(name: str, created_at: datetime) -> str:
        """Generate a unique VM ID from name and creation time."""
        import hashlib

        data = f"{name}:{created_at.isoformat()}"
        return hashlib.sha256(data.encode()).hexdigest()[:16]

    def set_resolved(self, resolved: VMInputResolved) -> None:
        self.resolved = resolved

    def set_firecracker_manager(self, manager: FirecrackerManager) -> None:
        self.fc_manager = manager

    def clone_image(self) -> None:

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        vm_rootfs_path = Path(f"{self.vm_dir}/rootfs.{self.resolved.image.fs_type}")

        image_manager = ImageManager(self.resolved.image, self._db)
        image_manager.ensure_cached()
        image_manager.copy_cached_to(vm_rootfs_path)

        self.rootfs_path = vm_rootfs_path

    def mark_created(self, resource: str) -> None:
        """Mark a resource as created (for cleanup tracking)."""
        self.resources_created[resource] = True

    def was_created(self, resource: str) -> bool:
        """Check if a resource was created."""
        return self.resources_created.get(resource, False)

    def cleanup(self) -> None:
        """Perform cleanup of all created resources. Called on creation failure."""
        import shutil

        if self.vm_dir is None:
            raise VMBuilderError("VM directory not set in context")

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        net_manager = NetworkManager()
        iptables_tracker = IPTablesTracker()
        lease_manager = NetworkIPLeaseManager(self.resolved.network, self._db)

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
                for rule in self.cloud_init_result.nocloud_net_rules:
                    iptables_tracker.remove_rule(rule)
            except Exception as exc:
                logger.warning("Failed to stop nocloud server during cleanup: %s", exc)

        # Networking
        if self.was_created("network_tap") and self.resolved:
            try:
                net_manager.remove_tap(self.resolved.tap_name, self.resolved.network.bridge)
            except Exception as exc:
                logger.warning("Failed to cleanup TAP device during cleanup: %s", exc)

            try:
                lease_manager.release(self.vm_id)
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

    def spawn(self) -> None:

        if self.vm_dir is None:
            raise VMBuilderError("VM directory not set in context")

        if self.resolved is None:
            raise VMBuilderError("Failed to resolve necessary dependencies")

        secure_mkdir(self.vm_dir, self.resolved.name)
        self.mark_created("vm_dir")

        # Networking
        net_manager = NetworkManager(self._db)
        net_manager.ensure_bridge(self.resolved.network.bridge, self.resolved.network.subnet)

        # NAT rules shouldn't be tracked since we don't clean it up, and most of the time
        # NAT rules are created after network is created, this is here just to ensure the
        # network NAT rules are present.
        if self.resolved.network.nat_enabled and self.resolved.network.nat_gateways:
            net_manager.ensure_nat(
                self.resolved.network.bridge,
                self.resolved.network.nat_gateways_list,
                subnet=self.resolved.network.subnet,
            )

        net_manager.ensure_tap(self.resolved.tap_name, self.resolved.network.bridge)
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
                guest_ip=self.resolved.guest_ip,
                tap_name=self.resolved.tap_name,
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
        config = FirecrackerManager(self, self.resolved)
        self.set_firecracker_manager(config)
        config.write_to_file()
        self.mark_created("firecracker")

        if self.fc_manager is None:
            raise VMBuilderError("Firecracker manager is not set in context")

        # Console
        if self.resolved.enable_console:
            from mvmctl.api.vm._console_relay import VMConsoleRelay

            self.relay = VMConsoleRelay(
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
            ipv4=self.resolved.guest_ip,
            mac=self.resolved.guest_mac,
            network_id=self.resolved.network.id,
            tap_device=self.resolved.tap_name,
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


__all__ = [
    "VMBuilder",
]
