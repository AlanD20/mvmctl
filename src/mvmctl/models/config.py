"""System configuration models — pure dataclass containers.

This module contains dataclasses for resolved system defaults.
All values are fully resolved (no None) — populated by load_system_defaults()
from _defaults.py overlaid with config.json.

Asset defaults (image/kernel/binary/network) are NOT stored here —
those are SQLite-backed and resolved at the API layer.
"""

from __future__ import annotations

from dataclasses import dataclass

__all__ = ["SystemDefaultsConfig"]


@dataclass
class SystemDefaultsConfig:
    """Resolved system-wide defaults — all fields required, no None values.

    Populated by load_system_defaults() which reads from:
    1. _defaults.py (base values via constants.py)
    2. config.json (user overrides)

    Asset defaults (image/kernel/binary/network) are NOT stored here —
    those are SQLite-backed and resolved at the API layer via MVMDatabase.

    **NO DEFAULT VALUES** — default resolution belongs ONLY in the CLI layer.
    """

    # Compute defaults
    vcpu_count: int
    mem_size_mib: int

    # SSH
    ssh_user: str

    # Disk
    disk_size: str

    # Boot
    boot_args: str
    enable_console: bool

    # Firecracker features
    enable_api_socket: bool
    enable_pci: bool
    lsm_flags: str
    enable_logging: bool
    enable_metrics: bool

    # Cloud-init
    cloud_init_mode: str  # "inject", "iso", "net", "off"

    # Network interface and name
    network_interface: str
    default_network_name: str
