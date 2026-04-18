import functools
import logging
import textwrap
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    REQUIRED_ISO_TOOL,
)
from mvmctl.exceptions import CloudInitError, ConfigError, ProcessError
from mvmctl.models import CloudInitWriteConfig

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


@functools.lru_cache(maxsize=1)
def _load_cloud_init_template() -> str:
    """Load the cloud-init template from the assets directory.

    Returns:
        The template string content.
    """
    import importlib.resources

    template_path = importlib.resources.files("mvmctl.assets") / "cloud-init.template.yaml"
    return template_path.read_text()


def _normalize_ssh_pub_keys(
    ssh_pub_key: "str | list[str] | None",
) -> list[str]:
    if ssh_pub_key is None:
        return []
    if isinstance(ssh_pub_key, str):
        stripped = ssh_pub_key.strip()
        return [stripped] if stripped else []
    return [k.strip() for k in ssh_pub_key if k.strip()]


def _render_cloud_init_template(
    vm_name: str,
    user: str,
    guest_ip: str,
    ipv4_gateway: str,
    prefix_len: int,
    ssh_pub_key: "str | list[str] | None" = None,
) -> dict[str, str]:
    from jinja2 import StrictUndefined
    from jinja2.sandbox import SandboxedEnvironment

    env = SandboxedEnvironment(undefined=StrictUndefined)
    template_str = _load_cloud_init_template()
    template = env.from_string(template_str)

    ssh_pub_keys = _normalize_ssh_pub_keys(ssh_pub_key)

    rendered = template.render(
        vm_name=vm_name,
        user=user,
        guest_ip=guest_ip,
        ipv4_gateway=ipv4_gateway,
        prefix_len=prefix_len,
        ssh_pub_keys=ssh_pub_keys,
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


def write_cloud_init(config: CloudInitWriteConfig) -> None:
    """Write cloud-init configuration files to the specified directory.

    Args:
        config: CloudInitWriteConfig containing all parameters for cloud-init file generation.

    Raises:
        ConfigError: If custom user-data is invalid or contains dangerous directives.
    """
    import yaml

    ssh_pub_keys = _normalize_ssh_pub_keys(config.ssh_pub_key)

    rendered = _render_cloud_init_template(
        vm_name=config.vm_name,
        user=config.user,
        guest_ip=config.guest_ip,
        ipv4_gateway=config.ipv4_gateway,
        prefix_len=config.prefix_len,
        ssh_pub_key=ssh_pub_keys,
    )

    (config.cloud_init_dir / "meta-data").write_text(rendered["meta_data"])

    if not config.skip_network_config:
        (config.cloud_init_dir / "network-config").write_text(rendered["network_config"])

    if config.custom_user_data is not None:
        ud: dict[str, Any] = {}
        content = config.custom_user_data.read_text()
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
        if ssh_pub_keys:
            if "users" not in ud:
                ud["users"] = [{"name": config.user, "ssh-authorized-keys": list(ssh_pub_keys)}]
            else:
                users_list = ud["users"]
                if isinstance(users_list, list):
                    user_found = False
                    for u in users_list:
                        if isinstance(u, dict) and u.get("name") == config.user:
                            existing_keys: list[str] = u.setdefault("ssh-authorized-keys", [])
                            for k in ssh_pub_keys:
                                if k not in existing_keys:
                                    existing_keys.append(k)
                            user_found = True
                            break
                    if not user_found:
                        users_list.append(
                            {"name": config.user, "ssh-authorized-keys": list(ssh_pub_keys)}
                        )
        if "network" in ud:
            logger.warning(
                "Custom user-data already contains 'network' key; "
                "cloud-init network stage will apply it. "
                "Ensure this is intentional."
            )
        (config.cloud_init_dir / "user-data").write_text(
            "#cloud-config\n" + yaml.dump(ud, default_flow_style=False)
        )
    else:
        (config.cloud_init_dir / "user-data").write_text(rendered["user_data"])


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
