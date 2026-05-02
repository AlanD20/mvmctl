"""Tests for CloudInitManager."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
import yaml

from mvmctl.core.cloudinit._manager import CloudInitManager
from mvmctl.core.cloudinit._provisioner import CloudInitProvisionConfig
from mvmctl.exceptions import CloudInitError, CloudInitProvisionError
from mvmctl.models import CloudInitMode, NetworkItem


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
def config(
    tmp_path: Path, sample_network: NetworkItem
) -> CloudInitProvisionConfig:
    """Create a basic cloud-init provision config."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir(parents=True)

    return CloudInitProvisionConfig(
        mode=CloudInitMode.ISO,
        vm_name="testvm",
        vm_id="abc123",
        vm_dir=tmp_path,
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


class TestCloudInitManager:
    """Tests for CloudInitManager."""

    def test_write_config_files_creates_all(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files creates meta-data, user-data, and network-config."""
        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            manager.write_config_files()

        meta_path = config.cloud_init_dir / "meta-data"
        user_path = config.cloud_init_dir / "user-data"
        net_path = config.cloud_init_dir / "network-config"

        assert meta_path.exists()
        assert user_path.exists()
        assert net_path.exists()

        meta = yaml.safe_load(meta_path.read_text())
        assert meta["instance-id"] == "testvm"
        assert meta["local-hostname"] == "testvm"

        net = yaml.safe_load(net_path.read_text())
        assert net["version"] == 2
        assert "eth0" in net["ethernets"]
        assert net["ethernets"]["eth0"]["addresses"] == ["10.0.0.10/24"]

        user = yaml.safe_load(user_path.read_text())
        assert user["hostname"] == "testvm"

    def test_write_config_files_skips_network(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files skips network-config when skip_network_config=True."""
        config.skip_network_config = True

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            manager.write_config_files()

        net_path = config.cloud_init_dir / "network-config"
        assert not net_path.exists()

    def test_write_config_files_with_ssh_key(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files includes SSH keys in user-data."""
        config.ssh_pubkeys = ["ssh-ed25519 AAAAC3... key1@example.com"]
        config.user = "ubuntu"

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            manager.write_config_files()

        user = yaml.safe_load((config.cloud_init_dir / "user-data").read_text())
        ubuntu_entry = next(
            (
                u
                for u in user["users"]
                if isinstance(u, dict) and u.get("name") == "ubuntu"
            ),
            None,
        )
        assert ubuntu_entry is not None
        assert (
            "ssh-ed25519 AAAAC3... key1@example.com"
            in ubuntu_entry["ssh-authorized-keys"]
        )

    def test_write_config_files_empty_ssh_keys(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files works with empty SSH key list."""
        config.ssh_pubkeys = []

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            manager.write_config_files()

        user = yaml.safe_load((config.cloud_init_dir / "user-data").read_text())
        assert "users" not in user or not user["users"]

    def test_custom_user_data(self, config: CloudInitProvisionConfig) -> None:
        """write_config_files with custom user-data preserves it."""
        custom_ud = config.cloud_init_dir / "custom.yaml"
        custom_ud.write_text("custom_key: custom_value\n")
        config.custom_user_data_path = custom_ud

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            manager.write_config_files()

        user = yaml.safe_load((config.cloud_init_dir / "user-data").read_text())
        assert user["custom_key"] == "custom_value"

    def test_custom_user_data_validates_dangerous_directives(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files rejects custom user-data with write_files."""

        custom_ud = config.cloud_init_dir / "dangerous.yaml"
        custom_ud.write_text(
            "#cloud-config\nwrite_files:\n  - path: /etc/test\n"
        )
        config.custom_user_data_path = custom_ud

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            with pytest.raises(CloudInitProvisionError, match="write_files"):
                manager.write_config_files()

    def test_custom_user_data_rejects_runcmd(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """write_config_files rejects custom user-data with runcmd."""
        custom_ud = config.cloud_init_dir / "runcmd.yaml"
        custom_ud.write_text("#cloud-config\nruncmd:\n  - echo hello\n")
        config.custom_user_data_path = custom_ud

        with patch(
            "mvmctl.core.cloudinit._manager._load_cloud_init_template",
            return_value=self._template(),
        ):
            manager = CloudInitManager(config)
            with pytest.raises(CloudInitProvisionError, match="runcmd"):
                manager.write_config_files()

    def test_create_seed_iso_success(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """create_seed_iso succeeds with all required files."""
        cloud_init_dir = config.cloud_init_dir
        (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
        (cloud_init_dir / "network-config").write_text("version: 2\n")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        output_iso = config.vm_dir / "test.iso"

        with patch("mvmctl.utils._system.run_cmd") as mock_run_cmd:
            mock_run_cmd.return_value = MagicMock(returncode=0)
            manager = CloudInitManager(config)
            manager.create_seed_iso(cloud_init_dir, output_iso)
            mock_run_cmd.assert_called_once()
            call_args = mock_run_cmd.call_args[0][0]
            assert call_args[0] == "cloud-localds"
            assert "-N" in call_args
            assert str(cloud_init_dir / "network-config") in call_args

    def test_create_seed_iso_without_network_config(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """create_seed_iso works without network-config."""
        cloud_init_dir = config.cloud_init_dir
        (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")

        output_iso = config.vm_dir / "test.iso"

        with patch("mvmctl.utils._system.run_cmd") as mock_run_cmd:
            mock_run_cmd.return_value = MagicMock(returncode=0)
            manager = CloudInitManager(config)
            manager.create_seed_iso(cloud_init_dir, output_iso)
            mock_run_cmd.assert_called_once()
            call_args = mock_run_cmd.call_args[0][0]
            assert "-N" not in call_args

    def test_create_seed_iso_missing_meta_data(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """create_seed_iso raises CloudInitError when meta-data is missing."""
        cloud_init_dir = config.cloud_init_dir
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")
        output_iso = config.vm_dir / "test.iso"

        manager = CloudInitManager(config)
        with pytest.raises(CloudInitError, match="meta-data"):
            manager.create_seed_iso(cloud_init_dir, output_iso)

    def test_create_seed_iso_missing_user_data(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """create_seed_iso raises CloudInitError when user-data is missing."""
        cloud_init_dir = config.cloud_init_dir
        (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
        output_iso = config.vm_dir / "test.iso"

        manager = CloudInitManager(config)
        with pytest.raises(CloudInitError, match="user-data"):
            manager.create_seed_iso(cloud_init_dir, output_iso)

    def test_create_seed_iso_fails(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """create_seed_iso raises CloudInitError when cloud-localds fails."""
        from mvmctl.exceptions import ProcessError

        cloud_init_dir = config.cloud_init_dir
        (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
        (cloud_init_dir / "network-config").write_text("version: 2\n")
        (cloud_init_dir / "user-data").write_text("#cloud-config\n")
        output_iso = config.vm_dir / "test.iso"

        with patch(
            "mvmctl.utils._system.run_cmd",
            side_effect=ProcessError("failed"),
        ):
            manager = CloudInitManager(config)
            with pytest.raises(
                CloudInitError, match="Failed to create cloud-init ISO"
            ):
                manager.create_seed_iso(cloud_init_dir, output_iso)

    def test_generate_password_hash(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """generate_password_hash produces a valid hash."""
        manager = CloudInitManager(config)
        hashed = manager.generate_password_hash("test123")
        assert hashed.startswith("$")

    def test_generate_password_hash_bcrypt(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """generate_password_hash supports bcrypt."""
        pytest.importorskip("bcrypt")
        manager = CloudInitManager(config)
        hashed = manager.generate_password_hash("test123", algorithm="bcrypt")
        assert hashed.startswith("$2b$")

    def test_generate_password_hash_invalid_algorithm(
        self, config: CloudInitProvisionConfig
    ) -> None:
        """generate_password_hash raises ValueError for unknown algorithm."""
        manager = CloudInitManager(config)
        with pytest.raises(ValueError, match="Unsupported algorithm"):
            manager.generate_password_hash("test123", algorithm="md5")

    def _template(self) -> str:
        """Return a minimal valid cloud-init template."""
        return (
            "user_data: |\n"
            "  #cloud-config\n"
            "  hostname: {{ vm_name }}\n"
            "  fqdn: {{ vm_name }}.local\n"
            "  users:\n"
            "  {% if ssh_pubkeys %}\n"
            "    - name: {{ user }}\n"
            "      ssh-authorized-keys:\n"
            "      {% for key in ssh_pubkeys %}\n"
            "        - {{ key }}\n"
            "      {% endfor %}\n"
            "  {% endif %}\n"
            "  packages:\n"
            "    - openssh-server\n"
            "    - cloud-init\n"
            "\n"
            "meta_data: |\n"
            "  instance-id: {{ vm_name }}\n"
            "  local-hostname: {{ vm_name }}\n"
            "\n"
            "network_config: |\n"
            "  version: 2\n"
            "  ethernets:\n"
            "    eth0:\n"
            "      dhcp4: false\n"
            "      addresses:\n"
            "        - {{ guest_ip }}/{{ prefix_len }}\n"
            "      routes:\n"
            "        - to: default\n"
            "          via: {{ ipv4_gateway }}\n"
            "\n"
            "nocloud_cfg: |\n"
            "  #cloud-config\n"
            "  network:\n"
            "    config: disabled\n"
        )
