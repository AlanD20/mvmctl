import logging
from pathlib import Path
from typing import Any

import yaml

from mvmctl.constants import (
    DEFAULT_CLOUD_INIT_DISABLE_SNAPD_CMD,
    DEFAULT_CLOUD_INIT_FINAL_MESSAGE,
    DEFAULT_CLOUD_INIT_ISO_VOLUME_LABEL,
    DEFAULT_DNS_NAMESERVERS,
    DEFAULT_GUEST_NETWORK_IFACE,
    REQUIRED_ISO_TOOL,
)
from mvmctl.exceptions import CloudInitError, ConfigError, ProcessError

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
    *,
    gateway: str,
    ssh_pub_key: str | None = None,
    custom_user_data: Path | None = None,
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
                "name": DEFAULT_GUEST_NETWORK_IFACE,
                "subnets": [
                    {
                        "type": "static",
                        "address": f"{guest_ip}/{prefix_len}",
                        "gateway": gateway,
                        "dns_nameservers": DEFAULT_DNS_NAMESERVERS,
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
            "runcmd": [DEFAULT_CLOUD_INIT_DISABLE_SNAPD_CMD],
            "final_message": DEFAULT_CLOUD_INIT_FINAL_MESSAGE,
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


def create_cloud_init_iso(cloud_init_dir: Path, output_iso: Path) -> None:
    """Create a cloud-init ISO from the seed directory.

    Args:
        cloud_init_dir: Directory containing meta-data, network-config, user-data
        output_iso: Path where the ISO should be written

    Raises:
        CloudInitError: If ISO creation fails
    """
    # Validate required files exist
    required_files = ["meta-data", "network-config", "user-data"]
    for filename in required_files:
        filepath = cloud_init_dir / filename
        if not filepath.exists():
            raise CloudInitError(f"Missing required cloud-init file: {filename}")

    # Run cloud-localds to create ISO
    cmd = [
        REQUIRED_ISO_TOOL,  # "cloud-localds"
        str(output_iso),
        str(cloud_init_dir / "meta-data"),
        "--network-config",
        str(cloud_init_dir / "network-config"),
        "--user-data",
        str(cloud_init_dir / "user-data"),
        "-v",  # Verbose
        DEFAULT_CLOUD_INIT_ISO_VOLUME_LABEL,  # "cidata"
    ]

    from mvmctl.utils.process import run_cmd

    try:
        run_cmd(cmd, check=True)
    except ProcessError as e:
        raise CloudInitError(f"Failed to create cloud-init ISO: {e}") from e
