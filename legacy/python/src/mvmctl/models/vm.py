"""VM data models."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from enum import StrEnum, auto
from pathlib import Path
from typing import Any

from mvmctl.models.binary import BinaryItem
from mvmctl.models.firecracker import CpuConfig
from mvmctl.models.image import ImageItem
from mvmctl.models.kernel import KernelItem
from mvmctl.models.network import NetworkItem
from mvmctl.models.volume import VolumeItem
from mvmctl.utils.common import CommonUtils


class VMStatus(StrEnum):
    """VM lifecycle states."""

    STARTING = auto()
    RUNNING = auto()
    PAUSED = auto()
    STOPPING = auto()
    STOPPED = auto()
    CRASHED = auto()
    ERROR = auto()


@dataclass
class VMInstanceItem:
    """VM instance record — maps to vm_instances table."""

    id: str
    name: str
    status: str
    pid: int
    ipv4: str
    mac: str
    network_id: str
    tap_device: str
    image_id: str
    kernel_id: str
    binary_id: str
    api_socket_path: str
    config_path: str
    cloud_init_mode: str
    vcpu_count: int
    mem_size_mib: int
    disk_size_mib: int
    rootfs_path: str
    rootfs_suffix: str
    pci_enabled: bool
    nested_virt: bool
    enable_logging: bool
    enable_metrics: bool
    enable_console: bool
    created_at: str
    updated_at: str

    relay_socket_path: str | None = None
    process_start_time: int | None = None
    nocloud_net_port: int | None = None
    nocloud_net_pid: int | None = None
    relay_pid: int | None = None
    exit_code: int | None = None
    log_path: str | None = None
    serial_output_path: str | None = None
    lsm_flags: str | None = None
    boot_args: str | None = None
    ssh_keys: list[str] = field(
        default_factory=list
    )  # SSH key fingerprints stored in VM
    ssh_user: str | None = None  # SSH user for this VM
    volume_ids: list[str] | None = None  # Attached volume IDs
    cpu_config: CpuConfig | None = None  # JSON: merged CPU template config

    def __post_init__(self) -> None:
        """Deserialize ssh_keys, volume_ids, and cpu_config from JSON strings when loading from DB."""
        if isinstance(self.ssh_keys, str):
            self.ssh_keys = json.loads(self.ssh_keys)
        if isinstance(self.volume_ids, str):
            self.volume_ids = json.loads(self.volume_ids)
        if isinstance(self.cpu_config, str):
            self.cpu_config = json.loads(self.cpu_config)
        CommonUtils.coerce_bool_fields(
            self,
            {
                "pci_enabled",
                "nested_virt",
                "enable_logging",
                "enable_metrics",
                "enable_console",
            },
        )

    # Resolved relations
    kernel: KernelItem | None = None
    image: ImageItem | None = None
    binary: BinaryItem | None = None
    network: NetworkItem | None = None
    volumes: list[VolumeItem] = field(default_factory=list)

    @property
    def vm_dir(self) -> Path:
        """Absolute VM directory path."""
        from mvmctl.utils.common import CacheUtils

        return CacheUtils.get_vm_dir(self.id)


@dataclass
class ConsoleInfo:
    """Information about a VM console socket."""

    socket_path: Path
    vm_name: str


@dataclass
class ConsoleState:
    """Current state of the console relay process."""

    running: bool
    pid: int | None
    socket_path: str | None


@dataclass
class VMInspectInfo:
    """Complete inspection output for a VM."""

    id: str
    name: str
    status: str
    created_at: str | None
    pid: int | None
    ip: str | None
    mac: str | None
    network_name: str | None
    tap_device: str | None
    cloud_init_mode: str
    image_id: str | None
    image_name: str | None
    kernel_id: str | None
    kernel_name: str | None
    paths: dict[str, str | None]  # vm_dir, rootfs, config
    features: dict[str, bool]  # api_socket, console, nocloud_net
    nocloud_net: dict[str, Any] | None = None
    console: dict[str, Any] | None = None
