"""VM data models."""

from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from pathlib import Path
from typing import Optional


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
    guest_ip: Optional[str] = None
    guest_mac: Optional[str] = None
    tap_device: Optional[str] = None
    boot_args: Optional[str] = None
    enable_socket: bool = False
    enable_pci: bool = False
    lsm_flags: str = "landlock,lockdown,yama,integrity,selinux,bpf"


@dataclass
class VMInstance:
    """VM instance metadata."""

    name: str
    pid: Optional[int] = None
    socket_path: Optional[Path] = None
    ip: Optional[str] = None
    mac: Optional[str] = None
    created_at: datetime = field(default_factory=datetime.now)
    status: VMState = VMState.STOPPED
    config: Optional[VMConfig] = None
