"""Tests for CloudInitProvisioner."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.cloudinit._provisioner import (
    CloudInitProvisionConfig,
    CloudInitProvisioner,
)
from mvmctl.models import CloudInitMode, NetworkItem

# CloudInitMode is only imported under TYPE_CHECKING in the source module.
# Inject at runtime so comparisons like CloudInitMode.OFF work in tests.
import mvmctl.core.cloudinit._provisioner as _pv_mod
_pv_mod.CloudInitMode = CloudInitMode


@pytest.fixture
def sample_network() -> NetworkItem:
    """Create a sample network."""
    return NetworkItem(
        id="net-test",
        name="testnet",
        subnet="10.0.0.0/24",
        bridge="mvmbr0",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=True,
        is_default=True,
        is_present=True,
        created_at="2024-01-01T00:00:00",
        updated_at="2024-01-01T00:00:00",
    )


@pytest.fixture
def provision_config(
    tmp_path: Path, sample_network: NetworkItem
) -> CloudInitProvisionConfig:
    """Create a basic provision config."""
    vm_dir = tmp_path / "vms" / "testvm"
    vm_dir.mkdir(parents=True)
    cloud_init_dir = vm_dir / "cloud-init"
    cloud_init_dir.mkdir(parents=True)

    return CloudInitProvisionConfig(
        mode=CloudInitMode.ISO,
        vm_name="testvm",
        vm_id="abc123def456",
        vm_dir=vm_dir,
        cloud_init_dir=cloud_init_dir,
        guest_ip="10.0.0.10",
        user="myuser",
        tap_name="tap-testvm",
        network=sample_network,
        network_prefix_len=24,
        skip_network_config=False,
        ssh_pubkeys=["ssh-rsa AAAAB3... test@example.com"],
        cloud_init_iso_name="cloud-init.iso",
        nocloud_port_range_start=8000,
        nocloud_port_range_end=9000,
        nocloud_max_port_retries=100,
    )


class TestCloudInitProvisioner:
    """Tests for CloudInitProvisioner."""

    def test_provision_off(
        self, provision_config: CloudInitProvisionConfig
    ) -> None:
        """provision with OFF mode returns result without ISO or URL."""
        provision_config.mode = CloudInitMode.OFF
        provisioner = CloudInitProvisioner(provision_config)
        result = provisioner.provision()
        assert result.mode == CloudInitMode.OFF
        assert result.iso_path is None
        assert result.nocloud_url is None

    def test_provision_inject(
        self, provision_config: CloudInitProvisionConfig
    ) -> None:
        """provision with INJECT mode returns result without ISO or URL."""
        provision_config.mode = CloudInitMode.INJECT
        with patch.object(
            CloudInitProvisioner,
            "_provision_inject",
            return_value=MagicMock(mode=CloudInitMode.INJECT),
        ):
            provisioner = CloudInitProvisioner(provision_config)
            result = provisioner.provision()
            assert result.mode == CloudInitMode.INJECT

    def test_provision_iso(
        self, provision_config: CloudInitProvisionConfig
    ) -> None:
        """provision with ISO mode creates seed ISO."""
        provision_config.mode = CloudInitMode.ISO

        with patch(
            "mvmctl.core.cloudinit._manager.CloudInitManager"
        ) as mock_mgr_cls:
            mock_mgr = MagicMock()
            mock_mgr_cls.return_value = mock_mgr

            provisioner = CloudInitProvisioner(provision_config)
            with patch.object(
                provisioner,
                "_provision_iso",
                return_value=MagicMock(
                    mode=CloudInitMode.ISO, iso_path=Path("/fake/test.iso")
                ),
            ):
                result = provisioner.provision()
                assert result.mode == CloudInitMode.ISO

    def test_provision_net_with_custom_port(
        self, provision_config: CloudInitProvisionConfig
    ) -> None:
        """provision with NET mode and custom port."""
        provision_config.mode = CloudInitMode.NET
        provision_config.nocloud_net_port = 8080

        with (
            patch(
                "mvmctl.core.cloudinit._manager.CloudInitManager"
            ) as mock_mgr_cls,
            patch(
                "mvmctl.services.nocloud_server.manager.NoCloudNetServerManager"
            ) as mock_srv_cls,
            patch(
                "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker"
            ),
            patch(
                "mvmctl.core._shared._iptables_tracker._repository.IPTablesRuleRepository"
            ),
            patch(
                "mvmctl.core.network._service.subprocess.run",
                return_value=MagicMock(returncode=0, stdout="", stderr=""),
            ),
        ):
            mock_mgr = MagicMock()
            mock_mgr_cls.return_value = mock_mgr

            mock_srv = MagicMock()
            mock_srv.start.return_value = ("http://10.0.0.1:8080", 8080, 12345)
            mock_srv_cls.return_value = mock_srv

            provisioner = CloudInitProvisioner(provision_config)
            result = provisioner.provision()

            assert result.mode == CloudInitMode.NET
            assert result.nocloud_port == 8080
            assert result.nocloud_url == "http://10.0.0.1:8080"
            assert result.nocloud_pid == 12345

    def test_custom_iso_path_resolved(
        self, provision_config: CloudInitProvisionConfig
    ) -> None:
        """Custom cloud_init_iso_path is resolved when provided."""
        provision_config.mode = CloudInitMode.ISO
        iso_path = provision_config.vm_dir / "custom.iso"
        iso_path.write_text("fake iso content")
        provision_config.cloud_init_iso_path = iso_path

        with patch(
            "mvmctl.core.cloudinit._manager.CloudInitManager"
        ) as mock_mgr_cls:
            mock_mgr = MagicMock()
            mock_mgr_cls.return_value = mock_mgr

            provisioner = CloudInitProvisioner(provision_config)
            result = provisioner.provision()
            assert result.iso_path == iso_path
