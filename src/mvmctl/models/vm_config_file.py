"""Portable VM configuration for export/import.

Uses semantic references (os_slug, version, name) — NEVER internal SHA256 IDs.
Nested sub-key structure for clean JSON export.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

__all__ = [
    "VMExportComputeConfig",
    "VMExportImageConfig",
    "VMExportKernelConfig",
    "VMExportBinaryConfig",
    "VMExportNetworkConfig",
    "VMExportBootConfig",
    "VMExportFirecrackerConfig",
    "VMExportCloudInitConfig",
    "VMExportConfig",
]


def _omit_none(obj: Any) -> Any:
    """Recursively remove None values from dicts and lists."""
    if isinstance(obj, dict):
        return {k: _omit_none(v) for k, v in obj.items() if v is not None}
    if isinstance(obj, list):
        return [_omit_none(item) for item in obj if item is not None]
    return obj


@dataclass
class VMExportComputeConfig:
    """Compute resources (vcpus, memory)."""

    vcpus: int | None = None
    mem: int | None = None


@dataclass
class VMExportImageConfig:
    """Image specification using portable semantic refs.

    image_id is FORBIDDEN — use os_slug + arch instead.
    """

    os_slug: str = ""  # e.g. "ubuntu-24.04" — required for import
    arch: str = ""  # e.g. "x86_64" — required for import
    disk_size: str | None = None  # e.g. "2G"


@dataclass
class VMExportKernelConfig:
    """Kernel specification using portable semantic refs.

    kernel_id is FORBIDDEN — use version + arch + type instead.
    """

    version: str | None = None  # e.g. "6.1.0"
    arch: str | None = None  # e.g. "x86_64"
    type: str | None = None  # "vmlinux" or "bzImage"


@dataclass
class VMExportBinaryConfig:
    """Firecracker binary specification using portable semantic refs.

    binary_id is FORBIDDEN — use name + version instead.
    """

    name: str = "firecracker"  # structural constant, not config-backed
    version: str | None = None  # e.g. "v1.15.0"


@dataclass
class VMExportNetworkConfig:
    """Network configuration with portable semantic refs.

    network_id is FORBIDDEN — use name for identification.
    subnet/gateway/nat are hints for auto-recreation on import.
    """

    name: str | None = None  # e.g. "default"
    subnet: str | None = None  # e.g. "172.35.0.0/24"
    ipv4_gateway: str | None = None  # e.g. "172.35.0.1"
    nat_gateways: str | None = None  # comma-separated gateway list
    nat_enabled: bool | None = None
    ip: str | None = None  # assigned guest IP
    mac: str | None = None  # assigned guest MAC


@dataclass
class VMExportBootConfig:
    """Boot configuration (kernel boot args, console)."""

    args: str | None = None  # kernel boot arguments
    enable_console: bool | None = None


@dataclass
class VMExportFirecrackerConfig:
    """Firecracker feature flags."""

    enable_api_socket: bool | None = None
    enable_pci: bool | None = None
    lsm_flags: str | None = None


@dataclass
class VMExportCloudInitConfig:
    """Cloud-init configuration."""

    mode: str | None = None  # "inject", "iso", "net", "off"
    user: str | None = None  # SSH user
    ssh_key: str | None = None  # SSH key name/path
    keep_iso: bool | None = None  # retain cloud-init ISO after boot
    nocloud_net_port: int | None = None  # 0 or None = auto-assign


@dataclass
class VMExportConfig:
    """Portable VM configuration for export/import across hosts.

    Uses semantic field references (os_slug, version, name) — NEVER internal IDs.
    On import, API layer resolves semantic refs → actual paths via DB queries.

    None values mean "use the target system's default at import time."
    They are omitted from JSON export for cleanliness.

    **NEVER add:** image_id, kernel_id, binary_id, network_id — those are internal.
    """

    # Schema version — fixed, not config-backed
    schema_version: str = "1.0"

    # VM identity
    name: str = ""

    # Nested sub-configs (all optional at export time)
    compute: VMExportComputeConfig = field(default_factory=VMExportComputeConfig)
    image: VMExportImageConfig = field(default_factory=VMExportImageConfig)
    kernel: VMExportKernelConfig = field(default_factory=VMExportKernelConfig)
    binary: VMExportBinaryConfig = field(default_factory=VMExportBinaryConfig)
    network: VMExportNetworkConfig = field(default_factory=VMExportNetworkConfig)
    boot: VMExportBootConfig = field(default_factory=VMExportBootConfig)
    firecracker: VMExportFirecrackerConfig = field(default_factory=VMExportFirecrackerConfig)
    cloud_init: VMExportCloudInitConfig = field(default_factory=VMExportCloudInitConfig)

    def to_dict(self) -> dict[str, Any]:
        """Serialize to dictionary, omitting None values for clean export."""
        raw: dict[str, Any] = asdict(self)
        return _omit_none(raw)  # type: ignore[no-any-return]

    def to_json_file(self, path: Path) -> None:
        """Export to JSON file (creates parent directories if needed)."""
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.to_dict(), indent=2, default=str))

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "VMExportConfig":
        """Deserialize from dictionary, ignoring unknown fields.

        Handles the nested sub-key structure by delegating to sub-config classes.
        """
        known_fields = {f.name for f in cls.__dataclass_fields__.values()}

        # Build sub-configs from nested dicts
        sub_configs = {}
        for field_name, field_class in [
            ("compute", VMExportComputeConfig),
            ("image", VMExportImageConfig),
            ("kernel", VMExportKernelConfig),
            ("binary", VMExportBinaryConfig),
            ("network", VMExportNetworkConfig),
            ("boot", VMExportBootConfig),
            ("firecracker", VMExportFirecrackerConfig),
            ("cloud_init", VMExportCloudInitConfig),
        ]:
            if field_name in data and isinstance(data[field_name], dict):
                sub_data = data[field_name]
                sub_known = {f for f in field_class.__dataclass_fields__}  # type: ignore[attr-defined]
                sub_filtered = {k: v for k, v in sub_data.items() if k in sub_known}
                sub_configs[field_name] = field_class(**sub_filtered)

        # Build main config from filtered data + sub-configs
        filtered = {k: v for k, v in data.items() if k in known_fields and k not in sub_configs}
        filtered.update(sub_configs)

        return cls(**filtered)

    @classmethod
    def from_json_file(cls, path: Path) -> "VMExportConfig":
        """Import from JSON file."""
        if not path.exists():
            raise FileNotFoundError(f"VM config file not found: {path}")
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError as e:
            raise ValueError(f"Invalid JSON in VM config file {path}: {e}") from e
        if not isinstance(data, dict):
            raise ValueError(f"VM config file must be a JSON object: {path}")
        return cls.from_dict(data)
