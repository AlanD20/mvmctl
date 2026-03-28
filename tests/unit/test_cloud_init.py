from unittest.mock import MagicMock, patch

import pytest
import yaml

from mvmctl.core.cloud_init import (
    _generate_network_config_v2,
    create_cloud_init_iso,
    write_cloud_init,
)
from mvmctl.exceptions import CloudInitError, ConfigError, ProcessError


def test_generate_network_config_v2():
    """_generate_network_config_v2 produces correct v2 format."""
    config = _generate_network_config_v2("10.20.0.10", "10.20.0.1", 24)

    assert config["version"] == 2
    assert "ethernets" in config
    assert "eth0" in config["ethernets"]

    eth0 = config["ethernets"]["eth0"]
    assert eth0["dhcp4"] is False
    assert "10.20.0.10/24" in eth0["addresses"]
    assert {"to": "default", "via": "10.20.0.1"} in eth0["routes"]
    assert "10.20.0.1" in eth0["nameservers"]["addresses"]


def test_write_cloud_init_basic(tmp_path):
    """write_cloud_init creates the three seed files with v2 network config."""
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

    # Verify v2 network config format
    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["version"] == 2
    assert "ethernets" in net
    assert "eth0" in net["ethernets"]
    assert net["ethernets"]["eth0"]["dhcp4"] is False
    assert "10.20.0.10/24" in net["ethernets"]["eth0"]["addresses"]

    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert "default" in ud["users"]
    users = ud["users"]
    myuser_entry = next(
        (u for u in users if isinstance(u, dict) and u.get("name") == "myuser"), None
    )
    assert myuser_entry is not None
    assert "ssh-rsa AAAAB3..." in myuser_entry["ssh-authorized-keys"]

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
    """write_cloud_init skips network-config when skip_network_config=True.

    Regression test for NO_CLOUD_NET mode where kernel ip= configures networking
    early, and cloud-init reconfiguring the interface causes connectivity issues.
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

    # user-data should have network.config disabled for NO_CLOUD_NET mode
    assert ud.get("network", {}).get("config") == "disabled"


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

    # Verify network-config content
    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["version"] == 2
    assert "eth0" in net["ethernets"]
    assert "10.20.0.10/24" in net["ethernets"]["eth0"]["addresses"]


# ---------------------------------------------------------------------------
# Custom user-data network disable injection (NO_CLOUD_NET mode)
# ---------------------------------------------------------------------------


def test_write_cloud_init_custom_user_data_injects_network_disable(tmp_path, monkeypatch):
    """Custom user-data gets network.config disabled injected when skip_network_config=True."""
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
    assert ud.get("network", {}).get("config") == "disabled"
    assert ud["custom_key"] == "custom_value"  # original content preserved


def test_write_cloud_init_custom_user_data_with_network_key_warns(tmp_path, monkeypatch):
    """Custom user-data with existing 'network' key logs a warning."""
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

    # Should NOT overwrite existing network key
    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert "network" in ud  # original preserved
    # Should log warning
    assert any("network" in w.lower() for w in warnings_logged)
