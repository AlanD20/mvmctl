"""Configuration loading and validation."""

import json
from dataclasses import fields
from pathlib import Path
from typing import Any

from mvmctl.constants import CONST_VM_VCPU_MIN
from mvmctl.models.config import SystemDefaultsConfig

_config_cache: dict[Path, SystemDefaultsConfig] = {}


def load_json(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}

    try:
        with open(path) as f:
            result: dict[str, Any] = json.loads(f.read()) or {}
            return result
    except (json.JSONDecodeError, ValueError):
        return {}


def load_config(config_dir: Path, defaults: SystemDefaultsConfig) -> SystemDefaultsConfig:
    """Load config from config.json and merge with defaults."""
    config_key = config_dir.resolve(strict=False)
    if config_key in _config_cache:
        return _config_cache[config_key]

    defaults_path = config_key / "config.json"
    data = load_json(defaults_path)

    # Flatten nested structure if present (legacy format)
    flat_data: dict[str, Any] = {}

    # Handle vm_defaults nested dict
    if "vm_defaults" in data:
        flat_data.update(data["vm_defaults"])

    # Handle other root keys
    for key in ["firecracker", "network", "paths"]:
        if key in data and isinstance(data[key], dict):
            # Extract relevant fields from nested structures
            if key == "firecracker":
                flat_data["firecracker_binary"] = data[key].get("binary", "")
            elif key == "network":
                flat_data["default_network_name"] = data[key].get("name", "default")
                flat_data["network_subnet"] = data[key].get("subnet", "")
                flat_data["network_gateway"] = data[key].get("ipv4_gateway", "")

    # Direct keys at root level
    for key in [
        "vcpu_count",
        "mem_size_mib",
        "ssh_user",
        "boot_args",
        "disk_size",
        "enable_api_socket",
        "enable_pci",
        "enable_logging",
        "enable_metrics",
        "enable_console",
        "lsm_flags",
        "cloud_init_mode",
        "network_interface",
        "default_network_name",
    ]:
        if key in data:
            flat_data[key] = data[key]

    # Get valid fields from SystemDefaultsConfig
    valid_fields = {f.name for f in fields(SystemDefaultsConfig)}

    # Filter to only valid fields
    filtered_data = {k: v for k, v in flat_data.items() if k in valid_fields}

    # Merge with defaults
    base: dict[str, Any] = {}
    for f in fields(SystemDefaultsConfig):
        base[f.name] = getattr(defaults, f.name)

    result = SystemDefaultsConfig(**{**base, **filtered_data})
    _config_cache[config_key] = result
    return result


def validate_config(config: SystemDefaultsConfig) -> list[str]:
    """Validate config and return list of error messages."""
    errors = []

    if config.vcpu_count < CONST_VM_VCPU_MIN:
        errors.append(f"vcpu_count: Must be at least {CONST_VM_VCPU_MIN}")

    if config.mem_size_mib < 64:
        errors.append("mem_size_mib: Must be at least 64 MiB")

    return errors


def dump_config(config: SystemDefaultsConfig, section: str | None = None) -> dict[str, object]:
    """Dump config to dictionary format."""
    result: dict[str, object] = {
        "vm_defaults": {
            "vcpu_count": config.vcpu_count,
            "mem_size_mib": config.mem_size_mib,
            "ssh_user": config.ssh_user,
            "boot_args": config.boot_args,
            "disk_size": config.disk_size,
            "enable_api_socket": config.enable_api_socket,
            "enable_pci": config.enable_pci,
            "enable_logging": config.enable_logging,
            "enable_metrics": config.enable_metrics,
            "enable_console": config.enable_console,
            "lsm_flags": config.lsm_flags,
            "cloud_init_mode": config.cloud_init_mode,
            "network_interface": config.network_interface,
            "default_network_name": config.default_network_name,
        },
    }

    if section:
        return {section: result.get(section, {})}

    return result
