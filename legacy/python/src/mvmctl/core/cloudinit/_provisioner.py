"""Cloud-init provisioning for VM creation."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from mvmctl.constants import CONST_DIR_PERMS_CACHE
from mvmctl.exceptions import (
    CloudInitIsoModeError,
    CloudInitNetModeError,
)
from mvmctl.models import (
    CloudInitMode,
    FirewallChain,
    FirewallPort,
    FirewallProtocol,
    FirewallRule,
    FirewallRuleType,
    FirewallTable,
    FirewallTarget,
    FirewallWildcard,
    NetworkItem,
)
from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager


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
    network: NetworkItem
    network_prefix_len: int
    skip_network_config: bool

    ssh_pubkeys: list[str]

    # Resolved from defaults.cloudinit
    cloud_init_iso_name: str
    nocloud_port_range_start: int
    nocloud_port_range_end: int
    nocloud_max_port_retries: int

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
    nocloud_net_rules: list[FirewallRule] = field(default_factory=list)


class CloudInitProvisioner:
    """Handle all cloud-init modes cleanly."""

    _config: CloudInitProvisionConfig

    def __init__(self, config: CloudInitProvisionConfig) -> None:
        """Initialize the CloudInitProvisioner."""
        from mvmctl.core.cloudinit._manager import CloudInitManager

        self._config = config
        self._manager = CloudInitManager(config)

    def provision(self) -> CloudInitProvisionResult:
        """
        Provision cloud-init based on configuration.

        Args:
            config: CloudInitProvisionConfig containing all provisioning parameters.

        Returns:
            CloudInitProvisionResult with what was created (iso path, nocloud url, etc.).

        """
        if self._config.mode == CloudInitMode.OFF:
            return self._provision_off()

        # Prepare the cloud-init configs
        self._config.cloud_init_dir.mkdir(
            mode=CONST_DIR_PERMS_CACHE, exist_ok=True
        )

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

        from mvmctl.services.nocloud_server.manager import (
            NoCloudNetServerManager,
        )

        try:
            net_manager = NoCloudNetServerManager(
                id=self._config.vm_id,
                path=self._config.vm_dir,
                name=self._config.vm_name,
                ipv4_gateway=self._config.network.ipv4_gateway,
                port=self._config.nocloud_net_port
                if self._config.nocloud_net_port is not None
                else 0,  # Zero triggers auto-allocation from port range
                port_range_start=self._config.nocloud_port_range_start,
                port_range_end=self._config.nocloud_port_range_end,
                max_port_retries=self._config.nocloud_max_port_retries,
            )
            url, port, pid = net_manager.start()

            from mvmctl.core._shared import Database, FirewallTracker

            tracker = FirewallTracker(Database())
            tracker.ensure_chain(
                FirewallChain.MVM_NOCLOUDNET_INPUT, auto_jump_from="INPUT"
            )

            nocloud_net_in_rule = FirewallRule(
                table_name=FirewallTable.FILTER,
                chain_name=FirewallChain.MVM_NOCLOUDNET_INPUT,
                rule_type=FirewallRuleType.NOCLOUDNET_INPUT,
                target=FirewallTarget.ACCEPT,
                network_id=self._config.network.id,
                protocol=FirewallProtocol.TCP,
                source=self._config.guest_ip,
                destination=self._config.network.ipv4_gateway,
                in_interface=self._config.tap_name,
                out_interface=FirewallWildcard.ANY_INTERFACE,
                sport=FirewallPort.ANY,
                dport=port,
                network_name=self._config.network.name,
                comment_tag=f"# nocloudnet:{self._config.vm_name}:{port}",
                is_active=True,
            )
            tracker.ensure_rule(nocloud_net_in_rule)

            return CloudInitProvisionResult(
                mode=CloudInitMode.NET,
                nocloud_url=url,
                nocloud_port=port,
                nocloud_pid=pid,
                nocloud_net_manager=net_manager,
                nocloud_net_rules=[nocloud_net_in_rule],
            )
        except Exception as exc:
            raise CloudInitNetModeError(
                f"Nocloud-net provisioning failed: {exc}"
            ) from exc

    def _provision_iso(self) -> CloudInitProvisionResult:
        """Provision using ISO mode with cloud-init ISO image."""

        if self._config.cloud_init_iso_path is not None:
            if not self._config.cloud_init_iso_path.exists():
                raise CloudInitIsoModeError(
                    f"Custom cloud-init ISO not found: {self._config.cloud_init_iso_path}"
                )
            return CloudInitProvisionResult(
                mode=CloudInitMode.ISO,
                iso_path=self._config.cloud_init_iso_path,
            )

        iso_path = self._config.vm_dir / self._config.cloud_init_iso_name
        try:
            self._manager.create_seed_iso(self._config.cloud_init_dir, iso_path)
        except Exception as exc:
            raise CloudInitIsoModeError(
                f"Failed to create cloud-init ISO: {exc}"
            ) from exc

        return CloudInitProvisionResult(
            mode=CloudInitMode.ISO, iso_path=iso_path
        )

    def _provision_inject(self) -> CloudInitProvisionResult:
        """Provision using inject mode — config files already written by provision()."""
        return CloudInitProvisionResult(mode=CloudInitMode.INJECT)
