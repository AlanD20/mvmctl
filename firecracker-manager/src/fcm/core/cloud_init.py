import shutil
import subprocess
from pathlib import Path
import yaml

from fcm.utils.console import print_error, print_info


def write_cloud_init(
    cloud_init_dir: Path,
    vm_name: str,
    guest_ip: str,
    user: str,
    ssh_pub_key: str | None = None,
    custom_user_data: Path | None = None,
    gateway: str = "10.20.0.1",
    prefix_len: int = 24,
) -> None:
    """Write cloud-init seed files (meta-data, network-config, user-data)."""
    meta_data = {"instance-id": vm_name, "local-hostname": vm_name}
    (cloud_init_dir / "meta-data").write_text(yaml.dump(meta_data, default_flow_style=False))

    network_config = {
        "version": 1,
        "config": [
            {
                "type": "physical",
                "name": "eth0",
                "subnets": [
                    {
                        "type": "static",
                        "address": f"{guest_ip}/{prefix_len}",
                        "gateway": gateway,
                        "dns_nameservers": ["8.8.8.8", "1.1.1.1"],
                    }
                ],
            }
        ],
    }
    (cloud_init_dir / "network-config").write_text(
        yaml.dump(network_config, default_flow_style=False)
    )

    if custom_user_data is not None:
        content = custom_user_data.read_text()
        if ssh_pub_key and "ssh_authorized_keys" not in content:
            extra = yaml.dump(
                {"users": [{"name": user, "ssh-authorized-keys": [ssh_pub_key]}]},
                default_flow_style=False,
            )
            content += "\n" + extra
        elif ssh_pub_key and "ssh_authorized_keys" in content:
            content = content.replace(
                "ssh_authorized_keys:",
                f"ssh_authorized_keys:\n      - {ssh_pub_key}",
                1,
            )
        (cloud_init_dir / "user-data").write_text(content)
    else:
        ud: dict[str, object] = {
            "users": ["default"],
            "package_update": False,
            "package_upgrade": False,
            "runcmd": ["systemctl disable --now snapd.socket 2>/dev/null || true"],
            "final_message": "fcm cloud-init done",
        }
        if ssh_pub_key:
            ud["users"] = [
                "default",
                {
                    "name": user,
                    "groups": "sudo",
                    "shell": "/bin/bash",
                    "sudo": "ALL=(ALL) NOPASSWD:ALL",
                    "ssh-authorized-keys": [ssh_pub_key],
                },
            ]
        (cloud_init_dir / "user-data").write_text(
            "#cloud-config\n" + yaml.dump(ud, default_flow_style=False)
        )


def inject_cloud_init(rootfs_path: Path, cloud_init_dir: Path) -> None:
    """Loop-mount rootfs and inject cloud-init seed files.

    Requires root. Falls back gracefully if loop mount fails.
    """
    import tempfile

    seed_target = "/var/lib/cloud/seed/nocloud"
    mount_point = Path(tempfile.mkdtemp(prefix="fcm-mount-"))

    try:
        # Mount the rootfs ext4 image
        subprocess.run(
            ["mount", "-o", "loop", str(rootfs_path), str(mount_point)],
            check=True,
            capture_output=True,
        )
        try:
            target = mount_point / seed_target.lstrip("/")
            target.mkdir(parents=True, exist_ok=True)
            for f in cloud_init_dir.iterdir():
                shutil.copy2(f, target / f.name)
        finally:
            subprocess.run(
                ["umount", str(mount_point)],
                check=False,
                capture_output=True,
            )
    except subprocess.CalledProcessError as e:
        print_error(f"Warning: could not inject cloud-init (requires root): {e}")
        print_info("VM will boot without cloud-init pre-seeding")
    finally:
        try:
            mount_point.rmdir()
        except OSError:
            pass
