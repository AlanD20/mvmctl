from unittest.mock import MagicMock, patch

import pytest
import yaml

from mvmctl.core.cloud_init import (
    _load_cloud_init_template,
    _render_cloud_init_template,
    create_cloud_init_iso,
    write_cloud_init,
)
from mvmctl.exceptions import CloudInitError, ConfigError, ProcessError


def test_write_cloud_init_basic(tmp_path):
    """write_cloud_init creates the three seed files with v2 network config from template."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa AAAAB3...",
    )

    assert (cloud_init_dir / "meta-data").exists()
    assert (cloud_init_dir / "network-config").exists()
    assert (cloud_init_dir / "user-data").exists()

    meta = yaml.safe_load((cloud_init_dir / "meta-data").read_text())
    assert meta["instance-id"] == "testvm"

    # Verify v2 network config format with DHCP
    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["version"] == 2
    assert "ethernets" in net
    assert "eth0" in net["ethernets"]
    # DHCP enabled for systemd-networkd compatibility
    assert net["ethernets"]["eth0"]["dhcp4"] is True
    assert net["ethernets"]["eth0"].get("dhcp6", False) is False

    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert (cloud_init_dir / "user-data").read_text().startswith("#cloud-config\n")
    assert "default" in ud["users"]
    users = ud["users"]
    myuser_entry = next(
        (u for u in users if isinstance(u, dict) and u.get("name") == "myuser"), None
    )
    assert myuser_entry is not None
    assert "ssh-rsa AAAAB3..." in myuser_entry["ssh-authorized-keys"]

    # Verify packages from template
    assert "openssh-server" in ud["packages"]
    assert "curl" in ud["packages"]
    assert "cloud-init" in ud["packages"]

    # Verify final_message from template
    assert ud["final_message"] == "mvmctl VM provisioning complete"

    # Default mode (skip_network_config=False) should NOT have network.config disabled
    assert ud.get("network", {}).get("config") != "disabled"


def test_write_cloud_init_custom_user_data(tmp_path):
    """write_cloud_init preserves custom user-data YAML."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    custom_ud = tmp_path / "custom.yaml"
    custom_ud.write_text("custom_key: custom_value\n")

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa CUSTOM",
        custom_user_data=custom_ud,
    )

    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert ud["custom_key"] == "custom_value"
    assert "users" in ud
    assert ud["users"][0]["name"] == "myuser"
    assert "ssh-rsa CUSTOM" in ud["users"][0]["ssh-authorized-keys"]


# ---------------------------------------------------------------------------
# Cloud-init security validation (issue #26)
# ---------------------------------------------------------------------------


def test_validate_user_data_rejects_dangerous_directives(tmp_path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    custom_ud = tmp_path / "dangerous.yaml"
    custom_ud.write_text("#cloud-config\nwrite_files:\n  - path: /etc/test\n")

    with pytest.raises(ConfigError, match="write_files"):
        write_cloud_init(
            cloud_init_dir=cloud_init_dir,
            vm_name="testvm",
            gateway="10.20.0.1",
            guest_ip="10.20.0.10",
            user="myuser",
            custom_user_data=custom_ud,
        )


def test_validate_user_data_rejects_runcmd(tmp_path):
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    custom_ud = tmp_path / "runcmd.yaml"
    custom_ud.write_text("#cloud-config\nruncmd:\n  - echo hello\n")

    with pytest.raises(ConfigError, match="runcmd"):
        write_cloud_init(
            cloud_init_dir=cloud_init_dir,
            vm_name="testvm",
            gateway="10.20.0.1",
            guest_ip="10.20.0.10",
            user="myuser",
            custom_user_data=custom_ud,
        )


# ---------------------------------------------------------------------------
# Cloud-init ISO creation
# ---------------------------------------------------------------------------


def test_create_cloud_init_iso_success(tmp_path):
    """create_cloud_init_iso succeeds when all required files exist and command succeeds."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create required files (with network-config for AUTO/CUSTOM modes)
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
    (cloud_init_dir / "network-config").write_text("version: 2\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with patch("mvmctl.utils.process.run_cmd") as mock_run_cmd:
        mock_run_cmd.return_value = MagicMock(returncode=0)
        create_cloud_init_iso(cloud_init_dir, output_iso)
        mock_run_cmd.assert_called_once()
        # Verify the command uses cloud-localds
        call_args = mock_run_cmd.call_args[0][0]
        assert call_args[0] == "cloud-localds"
        assert "-N" in call_args
        assert str(cloud_init_dir / "network-config") in call_args
        assert str(output_iso) in call_args
        assert str(cloud_init_dir / "user-data") in call_args
        assert str(cloud_init_dir / "meta-data") in call_args


def test_create_cloud_init_iso_without_network_config(tmp_path):
    """create_cloud_init_iso succeeds without network-config for NO_CLOUD_NET mode."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create only meta-data and user-data (network-config skipped for NO_CLOUD_NET)
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with patch("mvmctl.utils.process.run_cmd") as mock_run_cmd:
        mock_run_cmd.return_value = MagicMock(returncode=0)
        create_cloud_init_iso(cloud_init_dir, output_iso)
        mock_run_cmd.assert_called_once()
        # Verify the command uses cloud-localds without -N flag
        call_args = mock_run_cmd.call_args[0][0]
        assert call_args[0] == "cloud-localds"
        assert "-N" not in call_args
        assert str(output_iso) in call_args
        assert str(cloud_init_dir / "user-data") in call_args
        assert str(cloud_init_dir / "meta-data") in call_args


def test_create_cloud_init_iso_missing_meta_data(tmp_path):
    """create_cloud_init_iso raises CloudInitError when meta-data is missing."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create only user-data (missing meta-data)
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with pytest.raises(CloudInitError, match="meta-data"):
        create_cloud_init_iso(cloud_init_dir, output_iso)


def test_create_cloud_init_iso_missing_user_data(tmp_path):
    """create_cloud_init_iso raises CloudInitError when user-data is missing."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create only meta-data (missing user-data)
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")

    output_iso = tmp_path / "test.iso"

    with pytest.raises(CloudInitError, match="user-data"):
        create_cloud_init_iso(cloud_init_dir, output_iso)


def test_create_cloud_init_iso_creation_fails(tmp_path):
    """create_cloud_init_iso raises CloudInitError when ISO creation fails."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create all required files
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
    (cloud_init_dir / "network-config").write_text("version: 2\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with patch("mvmctl.utils.process.run_cmd") as mock_run_cmd:
        mock_run_cmd.side_effect = ProcessError("cloud-localds failed")

        with pytest.raises(CloudInitError, match="Failed to create cloud-init ISO"):
            create_cloud_init_iso(cloud_init_dir, output_iso)


# ---------------------------------------------------------------------------
# Network-config skip regression test (NO_CLOUD_NET mode)
# ---------------------------------------------------------------------------


def test_write_cloud_init_skips_network_config_when_requested(tmp_path):
    """write_cloud_init skips network-config file when skip_network_config=True.

    This is used for NO_CLOUD_NET mode where kernel ip= configures networking.
    The meta-data and user-data files are still written from template.
    """
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa AAAAB3...",
        skip_network_config=True,
    )

    # meta-data and user-data should exist
    assert (cloud_init_dir / "meta-data").exists()
    assert (cloud_init_dir / "user-data").exists()

    # network-config should NOT exist when skip_network_config=True
    assert not (cloud_init_dir / "network-config").exists()

    # Verify meta-data content
    meta = yaml.safe_load((cloud_init_dir / "meta-data").read_text())
    assert meta["instance-id"] == "testvm"

    # Verify user-data content still has SSH key
    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert "users" in ud
    myuser_entry = next(
        (u for u in ud["users"] if isinstance(u, dict) and u.get("name") == "myuser"), None
    )
    assert myuser_entry is not None
    assert "ssh-rsa AAAAB3..." in myuser_entry["ssh-authorized-keys"]


def test_write_cloud_init_includes_network_config_by_default(tmp_path):
    """write_cloud_init includes network-config by default (backward compatibility)."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa AAAAB3...",
        # skip_network_config defaults to False
    )

    # All three files should exist by default
    assert (cloud_init_dir / "meta-data").exists()
    assert (cloud_init_dir / "user-data").exists()
    assert (cloud_init_dir / "network-config").exists()

    # Verify network-config content (should use template-based v2 format with DHCP)
    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["version"] == 2
    assert "eth0" in net["ethernets"]
    # DHCP is enabled - static addresses are no longer used
    assert net["ethernets"]["eth0"]["dhcp4"] is True
    assert "addresses" not in net["ethernets"]["eth0"]


# ---------------------------------------------------------------------------
# Custom user-data network disable injection (NO_CLOUD_NET mode)
# ---------------------------------------------------------------------------


def test_write_cloud_init_custom_user_data_no_network_disable(tmp_path, monkeypatch):
    """Custom user-data does NOT get network.config disabled injected when skip_network_config=True."""
    warnings_logged: list[str] = []

    def _fake_logger_warning(msg: str, *args: object) -> None:
        warnings_logged.append(str(msg) % args if args else msg)

    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    custom_ud = tmp_path / "custom.yaml"
    custom_ud.write_text("custom_key: custom_value\n")

    import mvmctl.core.cloud_init as ci_module

    monkeypatch.setattr(ci_module.logger, "warning", _fake_logger_warning)

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        skip_network_config=True,
        custom_user_data=custom_ud,
    )

    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert ud.get("network") is None  # no network key injected
    assert ud["custom_key"] == "custom_value"  # original content preserved


def test_write_cloud_init_custom_user_data_with_network_key_warns(tmp_path, monkeypatch):
    """Custom user-data with existing 'network' key logs a warning (we do not inject anything)."""
    warnings_logged: list[str] = []

    def _fake_logger_warning(msg: str, *args: object) -> None:
        warnings_logged.append(str(msg) % args if args else msg)

    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    custom_ud = tmp_path / "custom.yaml"
    custom_ud.write_text("network:\n  config: {}\n")

    import mvmctl.core.cloud_init as ci_module

    monkeypatch.setattr(ci_module.logger, "warning", _fake_logger_warning)

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        skip_network_config=True,
        custom_user_data=custom_ud,
    )

    # Should log warning (we do not inject or modify anything)
    assert any("network" in w.lower() for w in warnings_logged)


# ---------------------------------------------------------------------------
# Template rendering tests
# ---------------------------------------------------------------------------


def test_write_cloud_init_uses_template(tmp_path):
    """write_cloud_init uses the Jinja2 template for generation."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Clear the cache to ensure we test fresh template loading
    _load_cloud_init_template.cache_clear()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="templatevm",
        gateway="10.30.0.1",
        guest_ip="10.30.0.50",
        user="testuser",
        ssh_pub_key="ssh-ed25519 AAAAC3... test@example.com",
        prefix_len=24,
    )

    # Verify template is used for meta-data
    meta = yaml.safe_load((cloud_init_dir / "meta-data").read_text())
    assert meta["instance-id"] == "templatevm"
    assert meta["local-hostname"] == "templatevm"

    # Verify template is used for network-config (v2 format with DHCP)
    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["version"] == 2
    assert "ethernets" in net
    # DHCP enabled for systemd-networkd compatibility
    assert net["ethernets"]["eth0"]["dhcp4"] is True
    assert net["ethernets"]["eth0"].get("dhcp6", False) is False
    # Static configuration no longer used with DHCP
    assert "addresses" not in net["ethernets"]["eth0"]
    assert "routes" not in net["ethernets"]["eth0"]

    # Verify template is used for user-data
    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert (cloud_init_dir / "user-data").read_text().startswith("#cloud-config\n")
    assert ud["hostname"] == "templatevm"
    assert ud["fqdn"] == "templatevm.local"
    assert ud["final_message"] == "mvmctl VM provisioning complete"

    # Verify packages from template are present
    assert "openssh-server" in ud["packages"]
    assert "cloud-init" in ud["packages"]


def test_render_cloud_init_template_all_placeholders():
    """_render_cloud_init_template substitutes all placeholders correctly."""
    # Clear cache to test fresh
    _load_cloud_init_template.cache_clear()

    rendered = _render_cloud_init_template(
        vm_name="myvm",
        user="ubuntu",
        guest_ip="192.168.1.100",
        gateway="192.168.1.1",
        prefix_len=24,
        ssh_pub_key="ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest",
    )

    # Verify all expected keys are present
    assert "user_data" in rendered
    assert "meta_data" in rendered
    assert "network_config" in rendered
    assert "nocloud_cfg" in rendered

    # Verify user_data has all placeholders substituted
    user_data = rendered["user_data"]
    assert user_data.startswith("#cloud-config\n")
    assert "{{vm_name}}" not in user_data
    assert "{{user}}" not in user_data
    assert "{{ssh_pub_key}}" not in user_data
    assert "myvm" in user_data
    assert "ubuntu" in user_data
    assert "AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest" in user_data
    assert "datasource:" not in user_data

    # Verify meta_data has placeholders substituted
    meta_data = rendered["meta_data"]
    assert "{{vm_name}}" not in meta_data
    assert "myvm" in meta_data

    # Verify network_config uses DHCP (no static IP placeholders needed)
    network_config = rendered["network_config"]
    # With DHCP, network config doesn't use guest_ip/gateway/prefix_len placeholders
    assert "dhcp4: true" in network_config
    assert "dhcp6: false" in network_config


def test_render_cloud_init_template_without_ssh_key():
    """_render_cloud_init_template works without SSH key."""
    # Clear cache to test fresh
    _load_cloud_init_template.cache_clear()

    rendered = _render_cloud_init_template(
        vm_name="nokeyvm",
        user="root",
        guest_ip="10.0.0.5",
        gateway="10.0.0.1",
        prefix_len=24,
        ssh_pub_key=None,
    )

    # Should still render successfully
    assert "user_data" in rendered
    assert "meta_data" in rendered
    assert "network_config" in rendered

    # user_data should not have SSH key section
    user_data = rendered["user_data"]
    assert "ssh-authorized-keys" not in user_data or "ssh_pub_key" not in user_data

def test_write_cloud_init_dhcp_no_systemd_network_workaround(tmp_path):
    """With DHCP, no systemd-networkd workaround files should be created."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Clear the cache to ensure we test fresh template loading
    _load_cloud_init_template.cache_clear()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa AAAAB3...",
    )

    # With DHCP configuration, we don't need to create systemd .network files
    # because systemd-networkd properly manages DHCP interfaces
    user_data_text = (cloud_init_dir / "user-data").read_text()
    assert "/run/systemd/network/10-mvmctl-eth0.network" not in user_data_text
    assert "/etc/systemd/network/10-mvmctl-eth0.network" not in user_data_text


def test_write_cloud_init_dhcp_no_wait_online_masking(tmp_path):
    """With DHCP, systemd-networkd-wait-online should not need masking."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Clear the cache to ensure we test fresh template loading
    _load_cloud_init_template.cache_clear()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
        gateway="10.20.0.1",
        guest_ip="10.20.0.10",
        user="myuser",
        ssh_pub_key="ssh-rsa AAAAB3...",
    )

    # With DHCP configuration, systemd-networkd-wait-online completes successfully
    # because systemd-networkd properly manages the DHCP interface
    user_data_text = (cloud_init_dir / "user-data").read_text()
    assert "systemctl mask systemd-networkd-wait-online" not in user_data_text
