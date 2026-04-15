"""Cloud-init provisioning for VM creation."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import CONST_DIR_PERMS_CACHE, DEFAULT_CLOUD_INIT_ISO_NAME
from mvmctl.exceptions import CloudInitError, MVMError
from src.mvmctl.api._internal._cloudinit._manager import CloudInitManager
from src.mvmctl.api._internal._iptables_tracker import IPTablesTracker
from src.mvmctl.db.models import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRule,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
)
from src.mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

if TYPE_CHECKING:
    from mvmctl.db.models import Network
    from mvmctl.models.cloud_init import CloudInitMode


@dataclass
class CloudInitProvisionConfig:
    """Configuration for cloud-init provisioning."""

    mode: CloudInitMode

    vm_name: str
    vm_id: str
    vm_dir: Path
    cloud_init_dir: Path

    guest_ip: str
    user: str
    tap_name: str
    network: Network
    network_prefix_len: int
    skip_network_config: bool

    ssh_pubkeys: list[str]
    custom_user_data_path: Path | None = None

    nocloud_net_port: int | None = None
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False


@dataclass
class CloudInitProvisionResult:
    """Result of cloud-init provisioning."""

    mode: CloudInitMode
    iso_path: Path | None = None
    nocloud_url: str | None = None
    nocloud_port: int = 0
    nocloud_pid: int | None = None
    nocloud_net_manager: NoCloudNetServerManager | None = None
    nocloud_net_rules: list[IPTablesRule] = []


class CloudInitProvisioner:
    """Handle all cloud-init modes cleanly."""

    _config: CloudInitProvisionConfig

    def __init__(self, config: CloudInitProvisionConfig) -> None:
        """Initialize the CloudInitProvisioner."""
        self._config = config
        self._manager = CloudInitManager(config)

    def provision(self) -> CloudInitProvisionResult:
        """Provision cloud-init based on configuration.

        Args:
            config: CloudInitProvisionConfig containing all provisioning parameters.

        Returns:
            CloudInitProvisionResult with what was created (iso path, nocloud url, etc.).
        """
        if self._config.mode == CloudInitMode.OFF:
            return self._provision_off()

        # Prepare the cloud-init configs
        self._config.cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        self._manager.write_config_files()

        result = None

        if self._config.mode == CloudInitMode.NET:
            result = self._provision_net()
        elif self._config.mode == CloudInitMode.ISO:
            result = self._provision_iso()
        else:
            result = self._provision_inject()

        return result

    def _provision_off(self) -> CloudInitProvisionResult:
        """Provision with cloud-init disabled."""
        return CloudInitProvisionResult(mode=CloudInitMode.OFF)

    def _provision_net(self) -> CloudInitProvisionResult:
        """Provision using nocloud-net mode with HTTP server."""

        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        net_manager = NoCloudNetServerManager(
            id=self._config.vm_id,
            path=self._config.vm_dir,
            name=self._config.vm_name,
            ipv4_gateway=self._config.network.ipv4_gateway,
            port=self._config.nocloud_net_port
            if self._config.nocloud_net_port is not None
            else 0,  # Zero is used to allocate next available port in the pool
        )
        url, port, pid = net_manager.start()

        iptables_tracker = IPTablesTracker()
        iptables_tracker.ensure_chain(IPTablesChain.MVM_NOCLOUDNET_INPUT, auto_jump_from="INPUT")

        nocloud_net_in_rule = IPTablesRule(
            table_name=IPTablesTable.FILTER,
            chain_name=IPTablesChain.MVM_NOCLOUDNET_INPUT,
            rule_type=IPTablesRuleType.NOCLOUDNET_INPUT,
            target=IPTablesTarget.ACCEPT,
            network_id=self._config.network.id,
            network_name=self._config.network.name,
            protocol=IPTablesProtocol.TCP,
            source=self._config.guest_ip,
            destination=self._config.network.ipv4_gateway,
            in_interface=self._config.tap_name,
            out_interface=IPTablesWildcard.ANY_INTERFACE,
            sport=IPTablesPort.ANY,
            dport=port,
            comment_tag=f"# nocloudnet:{self._config.vm_name}:{port}",
            is_active=True,
        )
        iptables_tracker.ensure_rule(nocloud_net_in_rule)

        return CloudInitProvisionResult(
            mode=CloudInitMode.NET,
            nocloud_url=url,
            nocloud_port=port,
            nocloud_pid=pid,
            nocloud_net_manager=net_manager,
            nocloud_net_rules=[nocloud_net_in_rule],
        )

    def _provision_iso(self) -> CloudInitProvisionResult:
        """Provision using ISO mode with cloud-init ISO image."""

        if self._config.cloud_init_iso_path is not None:
            if not self._config.cloud_init_iso_path.exists():
                raise MVMError(
                    f"Custom cloud-init ISO not found: {self._config.cloud_init_iso_path}"
                )
            return CloudInitProvisionResult(
                mode=CloudInitMode.ISO, iso_path=self._config.cloud_init_iso_path
            )

        iso_path = self._config.vm_dir / DEFAULT_CLOUD_INIT_ISO_NAME
        try:
            self._manager.create_seed_iso(self._config.cloud_init_dir, iso_path)
        except Exception as exc:
            raise CloudInitError(f"Failed to create cloud-init ISO: {exc}") from exc

        return CloudInitProvisionResult(mode=CloudInitMode.ISO, iso_path=iso_path)

    def _provision_inject(self) -> CloudInitProvisionResult:
        """Provision using inject mode with direct rootfs injection."""

        from mvmctl.core.rootfs_injector import inject_cloud_init

        rootfs_path = self._config.vm_dir / "rootfs.ext4"
        if not rootfs_path.exists():
            for ext in [".ext4", ".btrfs"]:
                rootfs_path = self._config.vm_dir / f"rootfs{ext}"
                if rootfs_path.exists():
                    break

        try:
            inject_cloud_init(str(rootfs_path), str(self._config.cloud_init_dir))
        except Exception as exc:
            raise CloudInitError(f"Direct injection failed: {exc}") from exc

        return CloudInitProvisionResult(mode=CloudInitMode.INJECT)
