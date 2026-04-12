"""Cloud-init provisioning for VM creation."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import CONST_DIR_PERMS_CACHE
from mvmctl.exceptions import CloudInitError, MVMError

if TYPE_CHECKING:
    from mvmctl.db.models import Network
    from mvmctl.models.cloud_init import CloudInitMode


@dataclass
class CloudInitProvisionResult:
    """Result of cloud-init provisioning."""

    iso_path: Path | None = None
    nocloud_url: str | None = None
    nocloud_port: int = 0
    nocloud_pid: int | None = None


@dataclass
class CloudInitProvisionConfig:
    """Configuration for cloud-init provisioning."""

    mode: CloudInitMode
    vm_id: str
    vm_dir: Path
    guest_ip: str
    user: str
    net_config: Network
    ssh_pub_key: list[str] | str | None = None
    user_data: Path | None = None
    nocloud_net_port: int | None = None
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False


class CloudInitProvisioner:
    """Handle all cloud-init modes cleanly."""

    def provision(self, config: CloudInitProvisionConfig) -> CloudInitProvisionResult:
        """Provision cloud-init based on configuration.

        Returns:
            CloudInitProvisionResult with what was created (iso path, nocloud url, etc.).
        """
        if config.mode.value == "off":
            return self._provision_off()
        elif config.mode.value == "net":
            return self._provision_net(config)
        elif config.mode.value == "iso":
            return self._provision_iso(config)
        elif config.mode.value == "inject":
            return self._provision_inject(config)
        else:
            return CloudInitProvisionResult()

    def _provision_off(self) -> CloudInitProvisionResult:
        """Provision with cloud-init disabled."""
        return CloudInitProvisionResult()

    def _provision_net(self, config: CloudInitProvisionConfig) -> CloudInitProvisionResult:
        """Provision using nocloud-net mode with HTTP server."""
        import ipaddress

        from mvmctl.core.cloud_init import write_cloud_init
        from mvmctl.models.cloud_init import CloudInitWriteConfig
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        cloud_init_dir = config.vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(config.net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=config.vm_dir.name,
            guest_ip=config.guest_ip,
            user=config.user,
            ssh_pub_key=config.ssh_pub_key,
            custom_user_data=config.user_data,
            ipv4_gateway=config.net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        net_manager = NoCloudNetServerManager()
        url, port = net_manager.start_server(
            config.vm_dir.name,
            cloud_init_dir,
            config.net_config.ipv4_gateway,
            config.vm_id,
            preferred_port=config.nocloud_net_port if config.nocloud_net_port is not None else 0,
        )
        nocloud_server_pid = net_manager.get_server_pid(config.vm_dir.name, config.vm_id)

        return CloudInitProvisionResult(
            nocloud_url=url,
            nocloud_port=port,
            nocloud_pid=nocloud_server_pid,
        )

    def _provision_iso(self, config: CloudInitProvisionConfig) -> CloudInitProvisionResult:
        """Provision using ISO mode with cloud-init ISO image."""
        import ipaddress

        from mvmctl.constants import DEFAULT_CLOUD_INIT_ISO_NAME
        from mvmctl.core.cloud_init import create_cloud_init_iso, write_cloud_init
        from mvmctl.models.cloud_init import CloudInitWriteConfig

        if config.cloud_init_iso_path is not None:
            if not config.cloud_init_iso_path.exists():
                raise MVMError(f"Custom cloud-init ISO not found: {config.cloud_init_iso_path}")
            return CloudInitProvisionResult(iso_path=config.cloud_init_iso_path)

        cloud_init_dir = config.vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(config.net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=config.vm_dir.name,
            guest_ip=config.guest_ip,
            user=config.user,
            ssh_pub_key=config.ssh_pub_key,
            custom_user_data=config.user_data,
            ipv4_gateway=config.net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        iso_path = config.vm_dir / DEFAULT_CLOUD_INIT_ISO_NAME
        try:
            create_cloud_init_iso(cloud_init_dir, iso_path)
        except Exception as exc:
            raise CloudInitError(f"Failed to create cloud-init ISO: {exc}") from exc

        return CloudInitProvisionResult(iso_path=iso_path)

    def _provision_inject(self, config: CloudInitProvisionConfig) -> CloudInitProvisionResult:
        """Provision using inject mode with direct rootfs injection."""
        import ipaddress

        from mvmctl.core.cloud_init import write_cloud_init
        from mvmctl.core.rootfs_injector import inject_cloud_init
        from mvmctl.models.cloud_init import CloudInitWriteConfig

        cloud_init_dir = config.vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(config.net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=config.vm_dir.name,
            guest_ip=config.guest_ip,
            user=config.user,
            ssh_pub_key=config.ssh_pub_key,
            custom_user_data=config.user_data,
            ipv4_gateway=config.net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        rootfs_path = config.vm_dir / "rootfs.ext4"
        if not rootfs_path.exists():
            for ext in [".ext4", ".btrfs"]:
                rootfs_path = config.vm_dir / f"rootfs{ext}"
                if rootfs_path.exists():
                    break

        try:
            inject_cloud_init(str(rootfs_path), str(cloud_init_dir))
        except Exception as exc:
            raise CloudInitError(f"Direct injection failed: {exc}") from exc

        return CloudInitProvisionResult()


__all__ = [
    "CloudInitProvisionConfig",
    "CloudInitProvisioner",
    "CloudInitProvisionResult",
]
