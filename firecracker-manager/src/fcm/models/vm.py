"""VM data models."""

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum, auto
from pathlib import Path


class VMState(StrEnum):
    """VM lifecycle states."""

    RUNNING = auto()
    STOPPED = auto()
    ERROR = auto()


@dataclass
class VMConfig:
    """VM configuration parameters."""

    name: str  # VM name; also used as hostname inside the guest
    vcpu_count: int = 2  # Number of vCPUs to allocate
    mem_size_mib: int = 2048  # Memory in MiB
    kernel_path: Path = field(default_factory=lambda: Path("vmlinux"))  # Path to vmlinux kernel image
    rootfs_path: Path = field(default_factory=lambda: Path("rootfs.ext4"))  # Path to root filesystem ext4 image
    guest_ip: str | None = None  # Static IP address for the guest NIC
    guest_mac: str | None = None  # MAC address for the guest NIC (auto-generated if None)
    gateway: str | None = None  # Host-side gateway IP for the guest
    subnet_mask: str = "255.255.255.0"  # Subnet mask for the guest network
    tap_device: str | None = None  # Host TAP interface name (auto-created if None)
    boot_args: str | None = None  # Override kernel boot arguments (uses defaults if None)
    enable_api_socket: bool = False  # Enable Firecracker HTTP API socket
    enable_pci: bool = False  # Enable PCI device support
    lsm_flags: str = "landlock,lockdown,yama,integrity,selinux,bpf"  # Linux Security Module flags

    def __post_init__(self) -> None:
        """Validate that vCPU count and memory size are within acceptable bounds."""
        if not 1 <= self.vcpu_count <= 32:
            raise ValueError(
                f"vcpu_count must be between 1 and 32 (inclusive), got {self.vcpu_count}"
            )
        if not 128 <= self.mem_size_mib <= 32768:
            raise ValueError(
                f"mem_size_mib must be between 128 and 32768 (inclusive), got {self.mem_size_mib}"
            )


@dataclass
class VMInstance:
    """VM instance metadata."""

    name: str  # VM name (matches VMConfig.name)
    pid: int | None = None  # PID of the running firecracker process (None if stopped)
    socket_path: Path | None = None  # Path to Firecracker API socket (if enabled)
    ip: str | None = None  # Assigned guest IP address
    mac: str | None = None  # Assigned guest MAC address
    network_name: str | None = None  # Name of the attached named network (if any)
    created_at: datetime = field(default_factory=lambda: datetime.now(tz=timezone.utc))  # Creation timestamp (UTC)
    status: VMState = VMState.STOPPED  # Current lifecycle state
    config: VMConfig | None = None  # Original VM configuration used to launch this instance
