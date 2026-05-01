"""Cloud-init configuration management - OOP implementation.

This module provides class-based cloud-init file generation and ISO creation.
The logic mirrors src/mvmctl/core/cloud_init.py but uses OOP patterns.
"""

from __future__ import annotations

import logging
import textwrap
from pathlib import Path
from typing import Any

from passlib.hash import bcrypt, sha512_crypt

from mvmctl.constants import DEFAULT_VM_USER_PASSWORD, REQUIRED_ISO_TOOL
from mvmctl.core._shared import AssetManager
from mvmctl.core.cloudinit._provisioner import CloudInitProvisionConfig
from mvmctl.exceptions import (
    CloudInitError,
    CloudInitProvisionError,
    ProcessError,
)

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


class CloudInitManager:
    """Manages cloud-init configuration file generation and ISO creation.

    This class encapsulates the logic for generating cloud-init seed files
    (meta-data, user-data, network-config) and creating ISO images from them.
    """

    def __init__(self, config: CloudInitProvisionConfig) -> None:
        """Initialize the CloudInitManager."""
        self._config = config

    def write_config_files(self) -> None:
        """Write cloud-init configuration files to the specified directory.

        Args:
            config: CloudInitWriteConfig containing all parameters for cloud-init file generation.

        Raises:
            ConfigError: If custom user-data is invalid or contains dangerous directives.
        """

        rendered = self._render_cloud_init_template()

        (self._config.cloud_init_dir / "meta-data").write_text(
            rendered["meta_data"]
        )

        if not self._config.skip_network_config:
            (self._config.cloud_init_dir / "network-config").write_text(
                rendered["network_config"]
            )

        # Custom user-data loader
        if self._config.custom_user_data_path is not None:
            self._parse_custom_user_data()
        else:
            (self._config.cloud_init_dir / "user-data").write_text(
                rendered["user_data"]
            )

    def _parse_custom_user_data(self) -> None:
        """Process custom user data provided to the API."""

        if self._config.custom_user_data_path is None:
            return

        import yaml

        custom_userdata: dict[str, Any] = {}
        content = self._config.custom_user_data_path.read_text()
        if not (
            content.startswith("#cloud-config")
            or content.startswith("Content-Type:")
        ):
            logger.warning(
                "user-data file does not start with '#cloud-config' or MIME boundary header"
            )
        try:
            loaded = yaml.safe_load(content)
            if isinstance(loaded, dict):
                custom_userdata = loaded
                self._validate_user_data(custom_userdata)
            elif loaded is not None:
                raise CloudInitProvisionError(
                    "Custom user-data must parse to a YAML mapping/object"
                )
        except yaml.YAMLError as exc:
            raise CloudInitProvisionError(
                f"Invalid YAML in user-data file: {exc}"
            ) from exc

        if self._config.ssh_pubkeys:
            if "users" not in custom_userdata:
                custom_userdata["users"] = [
                    {
                        "name": self._config.user,
                        "ssh-authorized-keys": list(self._config.ssh_pubkeys),
                    }
                ]
            else:
                users_list = custom_userdata["users"]
                if isinstance(users_list, list):
                    user_found = False
                    for u in users_list:
                        if (
                            isinstance(u, dict)
                            and u.get("name") == self._config.user
                        ):
                            existing_keys: list[str] = u.setdefault(
                                "ssh-authorized-keys", []
                            )
                            for k in self._config.ssh_pubkeys:
                                if k not in existing_keys:
                                    existing_keys.append(k)
                            user_found = True
                            break
                    if not user_found:
                        users_list.append(
                            {
                                "name": self._config.user,
                                "ssh-authorized-keys": list(
                                    self._config.ssh_pubkeys
                                ),
                            }
                        )
        if "network" in custom_userdata:
            logger.warning(
                "Custom user-data already contains 'network' key; "
                "cloud-init network stage will apply it. "
                "Ensure this is intentional."
            )

        (self._config.cloud_init_dir / "user-data").write_text(
            "#cloud-config\n"
            + yaml.dump(custom_userdata, default_flow_style=False)
        )

    def create_seed_iso(self, cloud_init_dir: Path, output_iso: Path) -> None:
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
                raise CloudInitError(
                    f"Missing required cloud-init file: {filename}"
                )

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

        from mvmctl.utils._system import run_cmd

        try:
            run_cmd(cmd, check=True)
        except ProcessError as e:
            raise CloudInitError(f"Failed to create cloud-init ISO: {e}") from e

    def _validate_user_data(self, user_data: dict[str, Any]) -> None:
        """Validate user-data for dangerous cloud-init directives.

        Args:
            user_data: The parsed user-data dictionary.

        Raises:
            ConfigError: If dangerous directives are found without proper safeguards.
        """
        dangerous_directives = [
            directive
            for directive in _DANGEROUS_CLOUD_INIT_DIRECTIVES
            if directive in user_data
        ]
        if not dangerous_directives:
            return

        details = "; ".join(
            f"{directive}: {_DANGEROUS_CLOUD_INIT_DIRECTIVES[directive]}"
            for directive in dangerous_directives
        )
        raise CloudInitProvisionError(
            "Custom cloud-init user-data contains blocked directive(s): "
            f"{', '.join(dangerous_directives)}. {details}"
        )

    def generate_password_hash(
        self, password: str, algorithm: str = "sha512"
    ) -> str:
        """Generate Unix password hash for cloud-init.

        Args:
            password: Plain text password to hash.
            algorithm: Hash algorithm - "sha512" or "bcrypt" (default: sha512).

        Returns:
            Unix crypt-style password hash.

        Raises:
            ValueError: If unsupported algorithm specified.
        """
        algorithms = {
            "sha512": sha512_crypt,
            "bcrypt": bcrypt,
        }

        hasher = algorithms.get(algorithm.lower())
        if hasher is None:
            raise ValueError(
                f"Unsupported algorithm: {algorithm}. Use: {list(algorithms.keys())}"
            )

        return hasher.hash(password)

    def _render_cloud_init_template(self) -> dict[str, str]:
        """Render the cloud-init template with provided values.

        Returns:
            Dictionary with rendered template sections (user_data, meta_data, network_config, nocloud_cfg)
        """
        from jinja2 import StrictUndefined
        from jinja2.sandbox import SandboxedEnvironment

        env = SandboxedEnvironment(undefined=StrictUndefined)
        template_str = AssetManager().read_file("cloud-init.template.yaml")
        template = env.from_string(template_str)

        rendered = template.render(
            vm_name=self._config.vm_name,
            user=self._config.user,
            guest_ip=self._config.guest_ip,
            ipv4_gateway=self._config.network.ipv4_gateway,
            prefix_len=self._config.network_prefix_len,
            ssh_pubkeys=self._config.ssh_pubkeys,
            password_hash=self.generate_password_hash(DEFAULT_VM_USER_PASSWORD),
        )

        # Parse the rendered YAML sections
        # The template uses literal block scalars (|) with indented content
        result: dict[str, str] = {}
        current_key: str | None = None
        current_content: list[str] = []

        # Top-level section headers in the template (not nested ones like write_files content)
        _SECTION_HEADERS = {
            "user_data",
            "meta_data",
            "network_config",
            "nocloud_cfg",
        }

        for line in rendered.splitlines():
            # Only treat unindented lines with known section names as section headers
            # This prevents nested content like "content: |" in write_files from being
            # treated as a new section
            if (
                not line.startswith(" ")
                and not line.startswith("\t")
                and (
                    line.endswith(": |")
                    or line.endswith(":|>")
                    or line.endswith(":|-")
                )
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
