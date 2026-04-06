"""Configuration loading and validation."""

import json
from dataclasses import dataclass, fields
from pathlib import Path
from typing import Any


@dataclass
class FirecrackerConfig:
    binary: str


@dataclass
class VMDefaultsConfig:
    vcpu_count: int
    mem_size_mib: int
    ssh_user: str
    network_interface: str
    boot_args: str
    disk_size: str
    enable_api_socket: bool
    enable_pci: bool
    lsm_flags: str


@dataclass
class NetworkDefaultsConfig:
    name: str
    subnet: str
    ipv4_gateway: str


@dataclass
class PathsConfig:
    assets_dir: str


@dataclass
class MVMConfig:
    firecracker: FirecrackerConfig
    vm_defaults: VMDefaultsConfig
    network: NetworkDefaultsConfig
    paths: PathsConfig


_config_cache: dict[Path, MVMConfig] = {}


def load_json(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}

    try:
        with open(path) as f:
            result: dict[str, Any] = json.loads(f.read()) or {}
            return result
    except (json.JSONDecodeError, ValueError):
        return {}


def load_config(config_dir: Path, defaults: MVMConfig) -> MVMConfig:
    config_key = config_dir.resolve(strict=False)
    if config_key in _config_cache:
        return _config_cache[config_key]

    defaults_path = config_key / "config.json"
    data = load_json(defaults_path)

    firecracker_data = data.get("firecracker", {})
    vm_defaults_data = data.get("vm_defaults", {})
    network_data = data.get("network", {})
    paths_data = data.get("paths", {})

    base_firecracker: dict[str, Any] = defaults.firecracker.__dict__.copy()
    base_vm_defaults: dict[str, Any] = defaults.vm_defaults.__dict__.copy()
    base_network: dict[str, Any] = defaults.network.__dict__.copy()
    base_paths: dict[str, Any] = defaults.paths.__dict__.copy()

    valid_firecracker_fields = {f.name for f in fields(FirecrackerConfig)}
    valid_vm_defaults_fields = {f.name for f in fields(VMDefaultsConfig)}
    valid_network_fields = {f.name for f in fields(NetworkDefaultsConfig)}
    valid_path_fields = {f.name for f in fields(PathsConfig)}

    firecracker_data_filtered = {
        k: v for k, v in firecracker_data.items() if k in valid_firecracker_fields
    }
    vm_defaults_data_filtered = {
        k: v for k, v in vm_defaults_data.items() if k in valid_vm_defaults_fields
    }
    network_data_filtered = {k: v for k, v in network_data.items() if k in valid_network_fields}
    paths_data_filtered = {k: v for k, v in paths_data.items() if k in valid_path_fields}

    result = MVMConfig(
        firecracker=FirecrackerConfig(**{**base_firecracker, **firecracker_data_filtered}),
        vm_defaults=VMDefaultsConfig(**{**base_vm_defaults, **vm_defaults_data_filtered}),
        network=NetworkDefaultsConfig(**{**base_network, **network_data_filtered}),
        paths=PathsConfig(**{**base_paths, **paths_data_filtered}),
    )
    _config_cache[config_key] = result
    return result


def validate_config(config: MVMConfig) -> list[str]:
    errors = []

    if not config.firecracker.binary:
        errors.append("firecracker.binary: Must not be empty")
    else:
        if not Path(config.firecracker.binary).exists():
            errors.append(f"firecracker.binary: File not found: {config.firecracker.binary}")

    if config.vm_defaults.vcpu_count < 1:
        errors.append("vm_defaults.vcpu_count: Must be at least 1")

    if config.vm_defaults.mem_size_mib < 64:
        errors.append("vm_defaults.mem_size_mib: Must be at least 64 MiB")

    try:
        import ipaddress

        ipaddress.ip_network(config.network.subnet, strict=False)
    except ValueError as e:
        errors.append(f"network.defaults.cidr: Invalid CIDR: {e}")

    try:
        import ipaddress

        ipaddress.ip_address(config.network.ipv4_gateway)
    except ValueError as e:
        errors.append(f"network.defaults.gateway: Invalid IP: {e}")

    return errors


def dump_config(config: MVMConfig, section: str | None = None) -> dict[str, object]:
    result: dict[str, object] = {
        "firecracker": config.firecracker.__dict__,
        "vm_defaults": config.vm_defaults.__dict__,
        "network": config.network.__dict__,
        "paths": config.paths.__dict__,
    }

    if section:
        return {section: result.get(section, {})}

    return result
