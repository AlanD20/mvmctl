"""Configuration loading and validation."""

import yaml
from pathlib import Path
from dataclasses import dataclass, field, fields
from typing import Any

from fcm.constants import BRIDGE_NAME, CLI_NAME


@dataclass
class FirecrackerConfig:
    """Firecracker binary configuration."""

    binary: str = "/usr/local/bin/firecracker"
    socket_dir: str = ""
    run_dir: str = ""
    log_dir: str = ""


@dataclass
class VMDefaultsConfig:
    """Default VM settings."""

    vcpu_count: int = 2
    mem_size_mib: int = 2048
    network_interface: str = "eth0"
    boot_args: str = "console=ttyS0 reboot=k panic=1 pci=off"
    disk_size: str = "2G"
    enable_api_socket: bool = False
    enable_pci: bool = False
    lsm_flags: str = "landlock,lockdown,yama,integrity,selinux,bpf"


@dataclass
class MultiVMNetworkConfig:
    """Multi VM network settings."""

    bridge_name: str = BRIDGE_NAME
    bridge_ip: str = "10.20.0.1/24"
    tap_prefix: str = CLI_NAME


@dataclass
class NetworkTopologyConfig:
    """Network topology configuration (wraps multi-VM network settings)."""

    multi_vm: MultiVMNetworkConfig = field(default_factory=MultiVMNetworkConfig)


@dataclass
class PathsConfig:
    """Directory paths."""

    assets_dir: str = ""


@dataclass
class FCMConfig:
    """Main configuration."""

    firecracker: FirecrackerConfig = field(default_factory=FirecrackerConfig)
    vm_defaults: VMDefaultsConfig = field(default_factory=VMDefaultsConfig)
    network: NetworkTopologyConfig = field(default_factory=NetworkTopologyConfig)
    paths: PathsConfig = field(default_factory=PathsConfig)


_config_cache: dict[Path, FCMConfig] = {}


def load_yaml(path: Path) -> dict[str, Any]:
    """Load YAML file."""
    if not path.exists():
        return {}

    with open(path, "r") as f:
        result: dict[str, Any] = yaml.safe_load(f) or {}
        return result


def load_config(config_dir: Path) -> FCMConfig:
    """Load configuration from YAML files.

    Loads defaults.yaml, images.yaml, and kernel.yaml from config_dir.

    Args:
        config_dir: Directory containing config files

    Returns:
        Parsed configuration
    """
    if config_dir in _config_cache:
        return _config_cache[config_dir]

    defaults_path = config_dir / "defaults.yaml"
    data = load_yaml(defaults_path)

    firecracker_data = data.get("firecracker", {})
    vm_defaults_data = data.get("vm_defaults", {})
    network_data = data.get("network", {})
    paths_data = data.get("paths", {})

    # Filter paths_data to only known PathsConfig fields
    valid_path_fields = {f.name for f in fields(PathsConfig)}
    paths_data_filtered = {k: v for k, v in paths_data.items() if k in valid_path_fields}

    # Filter multi_vm data to only known MultiVMNetworkConfig fields
    multi_vm_data = network_data.get("multi_vm", {})
    valid_multi_vm_fields = {f.name for f in fields(MultiVMNetworkConfig)}
    multi_vm_data_filtered = {k: v for k, v in multi_vm_data.items() if k in valid_multi_vm_fields}

    result = FCMConfig(
        firecracker=FirecrackerConfig(**firecracker_data),
        vm_defaults=VMDefaultsConfig(**vm_defaults_data),
        network=NetworkTopologyConfig(
            multi_vm=MultiVMNetworkConfig(**multi_vm_data_filtered),
        ),
        paths=PathsConfig(**paths_data_filtered),
    )
    _config_cache[config_dir] = result
    return result


def validate_config(config: FCMConfig) -> list[str]:
    """Validate configuration and return list of errors."""
    errors = []

    # Validate paths exist
    if not config.firecracker.binary:
        errors.append("firecracker.binary: Must not be empty")
    else:
        if not Path(config.firecracker.binary).exists():
            errors.append(f"firecracker.binary: File not found: {config.firecracker.binary}")

    # Validate VM resources
    if config.vm_defaults.vcpu_count < 1:
        errors.append("vm_defaults.vcpu_count: Must be at least 1")

    if config.vm_defaults.mem_size_mib < 64:
        errors.append("vm_defaults.mem_size_mib: Must be at least 64 MiB")

    # Validate network ranges
    try:
        import ipaddress

        ipaddress.ip_network(config.network.multi_vm.bridge_ip, strict=False)
    except ValueError as e:
        errors.append(f"network.multi_vm.bridge_ip: Invalid CIDR: {e}")

    return errors


def dump_config(config: FCMConfig, section: str | None = None) -> dict[str, object]:
    """Dump configuration as dictionary.

    Args:
        config: Configuration to dump
        section: Optional section to limit output

    Returns:
        Configuration dictionary
    """
    result: dict[str, object] = {
        "firecracker": config.firecracker.__dict__,
        "vm_defaults": config.vm_defaults.__dict__,
        "network": {
            "multi_vm": config.network.multi_vm.__dict__,
        },
        "paths": config.paths.__dict__,
    }

    if section:
        return {section: result.get(section, {})}

    return result
