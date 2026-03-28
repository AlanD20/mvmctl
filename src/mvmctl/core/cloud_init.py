import functools
import importlib.resources
import logging
import textwrap
from pathlib import Path
from typing import Any

import yaml
from jinja2 import StrictUndefined
from jinja2.sandbox import SandboxedEnvironment

from mvmctl.constants import (
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


def _generate_network_config_v2(guest_ip: str, gateway: str, prefix_len: int) -> dict[str, Any]:
    """Generate cloud-init network configuration version 2.

    Version 2 is required for systemd-networkd compatibility in cloud-init 24.0+.

    Args:
        guest_ip: The guest VM IP address.
        gateway: The gateway IP address.
        prefix_len: The network prefix length (default 24).

    Returns:
        Network configuration dictionary in v2 format.
    """
    return {
        "version": 2,
        "ethernets": {
            DEFAULT_GUEST_NETWORK_IFACE: {
                "dhcp4": False,
                "addresses": [f"{guest_ip}/{prefix_len}"],
                "routes": [{"to": "default", "via": gateway}],
                "nameservers": {"addresses": list(DEFAULT_DNS_NAMESERVERS)},
            }
        },
    }


@functools.lru_cache(maxsize=1)
def _load_cloud_init_template() -> str:
    """Load the cloud-init template from the assets directory.

    Returns:
        The template string content.
    """
    template_path = importlib.resources.files("mvmctl.assets") / "cloud-init.template.yaml"
    return template_path.read_text()


def _render_cloud_init_template(
    vm_name: str,
    user: str,
    guest_ip: str,
    gateway: str,
    prefix_len: int,
    ssh_pub_key: str | None = None,
) -> dict[str, str]:
    """Render the cloud-init template with the given parameters.

    Uses SandboxedEnvironment with StrictUndefined for security.

    Args:
        vm_name: VM name for hostname and instance-id.
        user: SSH username.
        guest_ip: Guest IP address.
        gateway: Gateway IP address.
        prefix_len: Network prefix length.
        ssh_pub_key: Optional SSH public key.

    Returns:
        Dictionary with keys 'user_data', 'meta_data', 'network_config', 'nocloud_cfg'.
    """
    env = SandboxedEnvironment(undefined=StrictUndefined)
    template_str = _load_cloud_init_template()
    template = env.from_string(template_str)

    rendered = template.render(
        vm_name=vm_name,
        user=user,
        guest_ip=guest_ip,
        gateway=gateway,
        prefix_len=prefix_len,
        ssh_pub_key=ssh_pub_key,
    )

    # Parse the rendered YAML sections
    # The template uses literal block scalars (|) with indented content
    result: dict[str, str] = {}
    current_key: str | None = None
    current_content: list[str] = []

    # Top-level section headers in the template (not nested ones like write_files content)
    _SECTION_HEADERS = {"user_data", "meta_data", "network_config", "nocloud_cfg"}

    for line in rendered.splitlines():
        # Only treat unindented lines with known section names as section headers
        # This prevents nested content like "content: |" in write_files from being
        # treated as a new section
        if (
            not line.startswith(" ")
            and not line.startswith("\t")
            and (line.endswith(": |") or line.endswith(":|>") or line.endswith(":|-"))
        ):
            section_name = line.rsplit(":", 1)[0]
            if section_name in _SECTION_HEADERS:
                # Found a new top-level section header
                if current_key is not None:
                    # Preserve indentation by joining without stripping
                    result[current_key] = "\n".join(current_content)
                current_key = section_name
                current_content = []
                continue
        if current_key is not None:
            current_content.append(line)

    if current_key is not None:
        result[current_key] = "\n".join(current_content)

    for key, value in result.items():
        result[key] = textwrap.dedent(value).lstrip("\n")

    return result


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
    skip_network_config: bool = False,
) -> None:
    """Write cloud-init seed files (meta-data, network-config, user-data).

    Args:
        cloud_init_dir: Directory to write cloud-init files to.
        vm_name: Name of the VM (used for instance-id and local-hostname).
        guest_ip: The guest VM IP address.
        user: Default username for SSH access.
        gateway: Gateway IP address for network configuration.
        ssh_pub_key: SSH public key to inject (optional).
        custom_user_data: Path to custom user-data YAML file (optional).
        prefix_len: Network prefix length (default: 24).
        skip_network_config: If True, skip writing network-config file.
            Used for NO_CLOUD_NET mode where kernel ip= configures networking.
    """
    # Load and render template for meta-data and network-config
    rendered = _render_cloud_init_template(
        vm_name=vm_name,
        user=user,
        guest_ip=guest_ip,
        gateway=gateway,
        prefix_len=prefix_len,
        ssh_pub_key=ssh_pub_key,
    )

    # Write meta-data from template (always)
    (cloud_init_dir / "meta-data").write_text(rendered["meta_data"])

    # Write network-config from template (unless skip_network_config=True)
    if not skip_network_config:
        (cloud_init_dir / "network-config").write_text(rendered["network_config"])

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
        if "network" in ud:
            logger.warning(
                "Custom user-data already contains 'network' key; "
                "cloud-init network stage will apply it. "
                "Ensure this is intentional."
            )
        (cloud_init_dir / "user-data").write_text(
            "#cloud-config\n" + yaml.dump(ud, default_flow_style=False)
        )
    else:
        # Use template for user-data when no custom user-data provided
        (cloud_init_dir / "user-data").write_text(rendered["user_data"])


def create_cloud_init_iso(cloud_init_dir: Path, output_iso: Path) -> None:
    """Create a cloud-init ISO from the seed directory.

    Args:
        cloud_init_dir: Directory containing meta-data, user-data, and optionally network-config
        output_iso: Path where the ISO should be written

    Raises:
        CloudInitError: If ISO creation fails
    """
    # Validate required files exist (network-config is optional for NO_CLOUD_NET mode)
    required_files = ["meta-data", "user-data"]
    for filename in required_files:
        filepath = cloud_init_dir / filename
        if not filepath.exists():
            raise CloudInitError(f"Missing required cloud-init file: {filename}")

    network_config_path = cloud_init_dir / "network-config"
    has_network_config = network_config_path.exists()

    # Run cloud-localds to create ISO
    # Use -N flag for network-config only if it exists (compatible with older versions)
    cmd = [
        REQUIRED_ISO_TOOL,  # "cloud-localds"
        "-v",  # Verbose
    ]
    if has_network_config:
        cmd.extend(["-N", str(network_config_path)])
    cmd.extend(
        [
            str(output_iso),
            str(cloud_init_dir / "user-data"),
            str(cloud_init_dir / "meta-data"),
        ]
    )

    from mvmctl.utils.process import run_cmd

    try:
        run_cmd(cmd, check=True)
    except ProcessError as e:
        raise CloudInitError(f"Failed to create cloud-init ISO: {e}") from e
