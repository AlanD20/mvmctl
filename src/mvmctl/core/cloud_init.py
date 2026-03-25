import logging
import shutil
import subprocess
from pathlib import Path
from typing import Any

import yaml

from mvmctl.exceptions import ConfigError

logger = logging.getLogger(__name__)

# Cloud-init directives that could be security risks if misused
_DANGEROUS_CLOUD_INIT_DIRECTIVES = {
    "write_files": "Can write arbitrary files to the system",
    "runcmd": "Can execute arbitrary commands",
    "bootcmd": "Can execute commands at boot",
    "snap": "Can install snap packages",
    "apt": "Can install packages (use with caution)",
    "yum": "Can install packages (use with caution)",
    "packages": "Can install packages (use with caution)",
}


def _validate_user_data(user_data: dict[str, Any]) -> None:
    """Validate user-data for dangerous cloud-init directives.

    Args:
        user_data: The parsed user-data dictionary.

    Raises:
        ConfigError: If dangerous directives are found without proper safeguards.
    """
    dangerous_directives = [
        directive for directive in _DANGEROUS_CLOUD_INIT_DIRECTIVES if directive in user_data
    ]
    if not dangerous_directives:
        return

    details = "; ".join(
        f"{directive}: {_DANGEROUS_CLOUD_INIT_DIRECTIVES[directive]}"
        for directive in dangerous_directives
    )
    raise ConfigError(
        "Custom cloud-init user-data contains blocked directive(s): "
        f"{', '.join(dangerous_directives)}. {details}"
    )


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
        ud: dict[str, Any] = {}
        content = custom_user_data.read_text()
        if not (content.startswith("#cloud-config") or content.startswith("Content-Type:")):
            logger.warning(
                "user-data file does not start with '#cloud-config' or MIME boundary header"
            )
        try:
            loaded = yaml.safe_load(content)
            if isinstance(loaded, dict):
                ud = loaded
                _validate_user_data(ud)
            elif loaded is not None:
                raise ConfigError("Custom user-data must parse to a YAML mapping/object")
        except yaml.YAMLError as exc:
            raise ConfigError(f"Invalid YAML in user-data file: {exc}") from exc
        if ssh_pub_key:
            if "users" not in ud:
                ud["users"] = [{"name": user, "ssh-authorized-keys": [ssh_pub_key]}]
            else:
                users_list = ud["users"]
                if isinstance(users_list, list):
                    user_found = False
                    for u in users_list:
                        if isinstance(u, dict) and u.get("name") == user:
                            keys = u.setdefault("ssh-authorized-keys", [])
                            if ssh_pub_key not in keys:
                                keys.append(ssh_pub_key)
                            user_found = True
                            break
                    if not user_found:
                        users_list.append({"name": user, "ssh-authorized-keys": [ssh_pub_key]})
        (cloud_init_dir / "user-data").write_text(
            "#cloud-config\n" + yaml.dump(ud, default_flow_style=False)
        )
    else:
        ud = {
            "users": ["default"],
            "package_update": False,
            "package_upgrade": False,
            "runcmd": ["systemctl disable --now snapd.socket 2>/dev/null || true"],
            "final_message": "mvm cloud-init done",
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

    with tempfile.TemporaryDirectory(prefix="mvm-mount-") as tmp_dir:
        mount_point = Path(tmp_dir)
        mount_point.chmod(0o700)
        mounted = False
        try:
            # Mount the rootfs ext4 image
            subprocess.run(
                ["mount", "-o", "loop", str(rootfs_path), str(mount_point)],
                check=True,
                capture_output=True,
            )
            mounted = True
            target = mount_point / seed_target.lstrip("/")
            target.mkdir(parents=True, exist_ok=True)
            for f in cloud_init_dir.iterdir():
                shutil.copy2(f, target / f.name)
        except subprocess.CalledProcessError as e:
            logger.warning("Could not inject cloud-init (requires root): %s", e)
            logger.info("VM will boot without cloud-init pre-seeding")
        finally:
            if mounted:
                subprocess.run(
                    ["umount", str(mount_point)],
                    check=False,
                    capture_output=True,
                )
