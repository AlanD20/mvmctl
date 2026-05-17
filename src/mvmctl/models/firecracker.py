"""Firecracker spawn configuration."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import NotRequired, TypedDict

from mvmctl.models.cloudinit import CloudInitMode


class DriveConfig(TypedDict):
    drive_id: str
    path_on_host: str
    is_root_device: bool
    is_read_only: bool
    partuuid: NotRequired[str]
    cache_type: str
    io_engine: str
    rate_limiter: NotRequired[object | None]
    socket: NotRequired[str | None]


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
    pci_enabled: bool
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

    # Extra drives (volumes)
    extra_drives: list[DriveConfig] = field(default_factory=list)

    # Spawn behavior
    relay_enabled: bool = False
    relay_client_fd: int | None = None
    snapshot_mode: bool = False
