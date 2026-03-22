"""VM data models."""

from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from pathlib import Path


class VMState(Enum):
    """VM lifecycle states."""

    RUNNING = "running"
    STOPPED = "stopped"
    PAUSED = "paused"
    ERROR = "error"


@dataclass
class VMConfig:
    """VM configuration parameters."""

    name: str
    vcpu_count: int = 2
    mem_size_mib: int = 2048
    kernel_path: Path = field(default_factory=lambda: Path("vmlinux"))
    rootfs_path: Path = field(default_factory=lambda: Path("rootfs.ext4"))
    guest_ip: str | None = None
    guest_mac: str | None = None
    tap_device: str | None = None
    boot_args: str | None = None
    enable_socket: bool = False
    enable_pci: bool = False
    lsm_flags: str = "landlock,lockdown,yama,integrity,selinux,bpf"


@dataclass
class VMInstance:
    """VM instance metadata."""

    name: str
    pid: int | None = None
    socket_path: Path | None = None
    ip: str | None = None
    mac: str | None = None
    created_at: datetime = field(default_factory=datetime.now)
    status: VMState = VMState.STOPPED
    config: VMConfig | None = None
