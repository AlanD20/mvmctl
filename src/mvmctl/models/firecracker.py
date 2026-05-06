"""Firecracker spawn configuration."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from mvmctl.models.cloudinit import CloudInitMode


@dataclass
class FirecrackerConfig:
    """
    Explicit configuration for spawning a Firecracker VM.

    All values are resolved by the API layer before passing to the spawner.
    No defaults, no None for required fields.
    """

    # Paths
    vm_dir: Path
    rootfs_path: Path

    # Binary / kernel
    binary_path: str
    kernel_path: str

    # Machine
    vcpu_count: int
    mem_size_mib: int

    # Network
    guest_ip: str
    guest_mac: str
    tap_name: str
    network_gateway: str
    network_netmask: str

    # Image metadata
    image_fs_uuid: str | None
    image_fs_type: str

    # Boot
    boot_args: str | None
    lsm_flags: str | None

    # Feature flags
    enable_pci: bool
    enable_console: bool
    enable_logging: bool
    enable_metrics: bool

    # File/path overrides (resolved from defaults.firecracker)
    log_level: str
    log_filename: str
    serial_output_filename: str
    metrics_filename: str
    api_socket_filename: str
    pid_filename: str
    config_filename: str

    # Cloud-init
    cloud_init_mode: CloudInitMode | None
    cloud_init_iso_path: Path | None
    cloud_init_nocloud_url: str | None

    # Spawn behavior
    relay_enabled: bool = False
    relay_client_fd: int | None = None
    snapshot_mode: bool = False
