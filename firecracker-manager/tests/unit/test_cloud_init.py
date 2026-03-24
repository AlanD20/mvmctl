import subprocess
from unittest.mock import patch

import yaml

from fcm.core.cloud_init import write_cloud_init, inject_cloud_init


def test_write_cloud_init_basic(tmp_path):
    """write_cloud_init creates the three seed files."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    write_cloud_init(
        cloud_init_dir=cloud_init_dir,
        vm_name="testvm",
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


@patch("fcm.core.cloud_init.subprocess.run")
def test_inject_cloud_init_success(mock_run, tmp_path):
    """inject_cloud_init loop-mounts and copies files."""
    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()
    (cloud_init_dir / "meta-data").write_text("test")

    rootfs = tmp_path / "rootfs.ext4"
    rootfs.write_text("fake")

    with patch("fcm.core.cloud_init.shutil.copy2") as mock_copy:
        inject_cloud_init(rootfs, cloud_init_dir)

    assert mock_run.call_count == 2
    mount_call = mock_run.call_args_list[0]
    assert mount_call.args[0][0] == "mount"
    assert mount_call.args[0][3] == str(rootfs)

    umount_call = mock_run.call_args_list[1]
    assert umount_call.args[0][0] == "umount"

    mock_copy.assert_called_once()


@patch("fcm.core.cloud_init.subprocess.run")
@patch("fcm.core.cloud_init.logger")
def test_inject_cloud_init_mount_error(mock_logger, mock_run, tmp_path):
    """inject_cloud_init gracefully handles mount errors."""
    mock_run.side_effect = subprocess.CalledProcessError(1, "mount")

    cloud_init_dir = tmp_path / "cloud-init"
    cloud_init_dir.mkdir()

    rootfs = tmp_path / "rootfs.ext4"

    inject_cloud_init(rootfs, cloud_init_dir)

    mock_logger.warning.assert_called_once()
