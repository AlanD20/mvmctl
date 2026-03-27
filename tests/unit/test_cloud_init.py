import subprocess
from unittest.mock import MagicMock, patch

import pytest
import yaml

from mvmctl.core.cloud_init import write_cloud_init, _validate_user_data, create_cloud_init_iso
from mvmctl.exceptions import ConfigError, CloudInitError, ProcessError


def test_write_cloud_init_basic(tmp_path):
    """write_cloud_init creates the three seed files."""
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

    net = yaml.safe_load((cloud_init_dir / "network-config").read_text())
    assert net["config"][0]["subnets"][0]["address"] == "10.20.0.10/24"

    ud = yaml.safe_load((cloud_init_dir / "user-data").read_text())
    assert "default" in ud["users"]
    users = ud["users"]
    myuser_entry = next(
        (u for u in users if isinstance(u, dict) and u.get("name") == "myuser"), None
    )
    assert myuser_entry is not None
    assert "ssh-rsa AAAAB3..." in myuser_entry["ssh-authorized-keys"]


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

    # Create required files
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
    (cloud_init_dir / "network-config").write_text("version: 1\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with patch("mvmctl.utils.process.run_cmd") as mock_run_cmd:
        mock_run_cmd.return_value = MagicMock(returncode=0)
        create_cloud_init_iso(cloud_init_dir, output_iso)
        mock_run_cmd.assert_called_once()
        # Verify the command uses cloud-localds
        call_args = mock_run_cmd.call_args[0][0]
        assert call_args[0] == "cloud-localds"
        assert call_args[1] == str(output_iso)


def test_create_cloud_init_iso_missing_file(tmp_path):
    """create_cloud_init_iso raises CloudInitError when required file is missing."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create only some files (missing meta-data)
    (cloud_init_dir / "network-config").write_text("version: 1\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with pytest.raises(CloudInitError, match="meta-data"):
        create_cloud_init_iso(cloud_init_dir, output_iso)


def test_create_cloud_init_iso_creation_fails(tmp_path):
    """create_cloud_init_iso raises CloudInitError when ISO creation fails."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    # Create all required files
    (cloud_init_dir / "meta-data").write_text("instance-id: testvm\n")
    (cloud_init_dir / "network-config").write_text("version: 1\n")
    (cloud_init_dir / "user-data").write_text("#cloud-config\n")

    output_iso = tmp_path / "test.iso"

    with patch("mvmctl.utils.process.run_cmd") as mock_run_cmd:
        mock_run_cmd.side_effect = ProcessError("cloud-localds failed")

        with pytest.raises(CloudInitError, match="Failed to create cloud-init ISO"):
            create_cloud_init_iso(cloud_init_dir, output_iso)
