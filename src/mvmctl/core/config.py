"""Configuration loading and validation."""

from dataclasses import dataclass, field, fields
from pathlib import Path
from typing import Any

import yaml

from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BINARY_PATH,
    DEFAULT_NETWORK_IPV4_GATEWAY,
    DEFAULT_NETWORK_NAME,
    DEFAULT_NETWORK_SUBNET,
    DEFAULT_VM_BOOT_ARGS,
    DEFAULT_VM_DISK_SIZE,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LSM_FLAGS,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_NETWORK_INTERFACE,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)


def _default_assets_dir() -> str:
    from mvmctl.utils.fs import get_cache_dir

    return str(get_cache_dir())


@dataclass
class FirecrackerConfig:
    binary: str = DEFAULT_FIRECRACKER_BINARY_PATH


@dataclass
class VMDefaultsConfig:
    vcpu_count: int = DEFAULT_VM_VCPU_COUNT
    mem_size_mib: int = DEFAULT_VM_MEM_MIB
    ssh_user: str = DEFAULT_VM_SSH_USER
    network_interface: str = DEFAULT_VM_NETWORK_INTERFACE
    boot_args: str = DEFAULT_VM_BOOT_ARGS
    disk_size: str = DEFAULT_VM_DISK_SIZE
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI
    lsm_flags: str = DEFAULT_VM_LSM_FLAGS


@dataclass
class NetworkDefaultsConfig:
    name: str = DEFAULT_NETWORK_NAME
    subnet: str = DEFAULT_NETWORK_SUBNET
    ipv4_gateway: str = DEFAULT_NETWORK_IPV4_GATEWAY


@dataclass
class NetworkTopologyConfig:
    defaults: NetworkDefaultsConfig = field(default_factory=NetworkDefaultsConfig)


@dataclass
class PathsConfig:
    assets_dir: str = field(default_factory=_default_assets_dir)


@dataclass
class MVMConfig:
    """Main configuration."""

    firecracker: FirecrackerConfig = field(default_factory=FirecrackerConfig)
    vm_defaults: VMDefaultsConfig = field(default_factory=VMDefaultsConfig)
    network: NetworkTopologyConfig = field(default_factory=NetworkTopologyConfig)
    paths: PathsConfig = field(default_factory=PathsConfig)


_config_cache: dict[Path, MVMConfig] = {}


def load_yaml(path: Path) -> dict[str, Any]:
    """Load YAML file."""
    if not path.exists():
        return {}

    try:
        with open(path, "r") as f:
            result: dict[str, Any] = yaml.safe_load(f) or {}
            return result
    except yaml.YAMLError:
        return {}


def load_config(config_dir: Path) -> MVMConfig:
    """Load configuration from a YAML override file in config_dir.

    Args:
        config_dir: Directory optionally containing a config.yaml override file

    Returns:
        Parsed configuration
    """
    config_key = config_dir.resolve(strict=False)
    if config_key in _config_cache:
        return _config_cache[config_key]

    defaults_path = config_key / "config.yaml"
    data = load_yaml(defaults_path)

    firecracker_data = data.get("firecracker", {})
    vm_defaults_data = data.get("vm_defaults", {})
    network_data = data.get("network", {})
    paths_data = data.get("paths", {})

    # Filter to only known fields for each dataclass
    valid_firecracker_fields = {f.name for f in fields(FirecrackerConfig)}
    firecracker_data_filtered = {
        k: v for k, v in firecracker_data.items() if k in valid_firecracker_fields
    }

    valid_vm_defaults_fields = {f.name for f in fields(VMDefaultsConfig)}
    vm_defaults_data_filtered = {
        k: v for k, v in vm_defaults_data.items() if k in valid_vm_defaults_fields
    }

    valid_path_fields = {f.name for f in fields(PathsConfig)}
    paths_data_filtered = {k: v for k, v in paths_data.items() if k in valid_path_fields}

    # Filter network defaults to only known NetworkDefaultsConfig fields
    network_defaults_data = network_data.get("defaults", {})
    valid_network_defaults_fields = {f.name for f in fields(NetworkDefaultsConfig)}
    network_defaults_data_filtered = {
        k: v for k, v in network_defaults_data.items() if k in valid_network_defaults_fields
    }

    result = MVMConfig(
        firecracker=FirecrackerConfig(**firecracker_data_filtered),
        vm_defaults=VMDefaultsConfig(**vm_defaults_data_filtered),
        network=NetworkTopologyConfig(
            defaults=NetworkDefaultsConfig(**network_defaults_data_filtered),
        ),
        paths=PathsConfig(**paths_data_filtered),
    )
    _config_cache[config_key] = result
    return result


def validate_config(config: MVMConfig) -> list[str]:
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

        ipaddress.ip_network(config.network.defaults.subnet, strict=False)
    except ValueError as e:
        errors.append(f"network.defaults.cidr: Invalid CIDR: {e}")

    try:
        import ipaddress

        ipaddress.ip_address(config.network.defaults.ipv4_gateway)
    except ValueError as e:
        errors.append(f"network.defaults.gateway: Invalid IP: {e}")

    return errors


def dump_config(config: MVMConfig, section: str | None = None) -> dict[str, object]:
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
            "defaults": config.network.defaults.__dict__,
        },
        "paths": config.paths.__dict__,
    }

    if section:
        return {section: result.get(section, {})}

    return result
