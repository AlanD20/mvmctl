"""VM data models."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum, auto
from pathlib import Path
from typing import Any


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
class VMInstance:
    """VM instance state — maps to vm_instances database table."""

    id: str
    name: str
    status: VMStatus
    pid: int
    ipv4: str
    mac: str
    network_id: str
    tap_device: str
    image_id: str
    kernel_id: str
    binary_id: str
    config_path: str
    vcpu_count: int
    mem_size_mib: int
    api_socket_path: str
    disk_size_mib: int
    rootfs_path: str
    rootfs_suffix: str
    created_at: str
    updated_at: str
    enable_pci: bool
    enable_logging: bool
    enable_metrics: bool
    enable_console: bool
    cloud_init_mode: str

    log_path: str | None = None
    serial_output_path: str | None = None
    nocloud_net_port: int | None = None
    nocloud_net_pid: int | None = None
    relay_socket_path: str | None = None
    relay_pid: int | None = None
    exit_code: int | None = None
    lsm_flags: str | None = None
    boot_args: str | None = None


## FIXME: require migration


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
