"""VM data models."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum, auto
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mvmctl.core.config_gen import DriveConfig

from mvmctl.constants import (
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_LOGGING,
    DEFAULT_VM_ENABLE_METRICS,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_KERNEL_FILENAME,
    DEFAULT_VM_LSM_FLAGS,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_ROOTFS_FILENAME,
    DEFAULT_VM_SUBNET_MASK,
    DEFAULT_VM_VCPU_COUNT,
)


class VMState(StrEnum):
    """VM lifecycle states."""

    RUNNING = auto()
    STOPPED = auto()
    ERROR = auto()


class CloudInitMode(StrEnum):
    """Cloud-init configuration mode.

    Attributes:
        AUTO: Generate cloud-init ISO from config files (default).
        CUSTOM: Use a pre-existing custom cloud-init ISO.
        DISABLED: Skip cloud-init entirely (no ISO mounted).
        NO_CLOUD_NET: Serve cloud-init files via HTTP (nocloud-net datasource).
    """

    AUTO = "auto"
    CUSTOM = "custom"
    DISABLED = "disabled"
    NO_CLOUD_NET = "nocloud-net"


@dataclass
class VMConfig:
    """VM configuration parameters.

    Attributes:
        name: VM name; also used as hostname inside the guest.
        vcpu_count: Number of vCPUs to allocate (1-32).
        mem_size_mib: Memory in MiB (128-65536).
        kernel_path: Path to vmlinux kernel image.
        rootfs_path: Path to root filesystem ext4 image.
        guest_ip: Static IP address for the guest NIC.
        guest_mac: MAC address for the guest NIC (auto-generated if None).
        gateway: Host-side gateway IP for the guest.
        subnet_mask: Subnet mask for the guest network.
        tap_device: Host TAP interface name (auto-created if None).
        boot_args: Override kernel boot arguments (uses defaults if None).
        enable_api_socket: Enable Firecracker HTTP API socket.
        enable_pci: Enable PCI device support.
        lsm_flags: Linux Security Module flags for the kernel cmdline.
        cloud_init_mode: Cloud-init configuration mode (auto/custom/disabled).
        datasource_mode: Cloud-init datasource mode (auto/nocloud-net).
        cloud_init_iso_path: Path to custom cloud-init ISO (used when mode is CUSTOM).
        keep_cloud_init_iso: Retain the generated cloud-init ISO after boot.
    """

    name: str
    vcpu_count: int = DEFAULT_VM_VCPU_COUNT
    mem_size_mib: int = DEFAULT_VM_MEM_MIB
    kernel_path: Path = field(default_factory=lambda: Path(DEFAULT_VM_KERNEL_FILENAME))
    rootfs_path: Path = field(default_factory=lambda: Path(DEFAULT_VM_ROOTFS_FILENAME))
    guest_ip: str | None = None
    guest_mac: str | None = None
    gateway: str | None = None
    subnet_mask: str = DEFAULT_VM_SUBNET_MASK
    tap_device: str | None = None
    boot_args: str | None = None
    root_partuuid: str | None = None
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI
    lsm_flags: str = DEFAULT_VM_LSM_FLAGS
    extra_drives: list[DriveConfig] = field(default_factory=list)
    enable_logging: bool = DEFAULT_VM_ENABLE_LOGGING
    enable_metrics: bool = DEFAULT_VM_ENABLE_METRICS
    cloud_init_mode: CloudInitMode = CloudInitMode.AUTO
    datasource_mode: CloudInitMode = CloudInitMode.AUTO
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False
    nocloud_net_url: str | None = None

    def __post_init__(self) -> None:
        """Validate that vCPU count and memory size are within acceptable bounds."""
        if not 1 <= self.vcpu_count <= 32:
            raise ValueError(
                f"vcpu_count must be between 1 and 32 (inclusive), got {self.vcpu_count}"
            )
        if not 128 <= self.mem_size_mib <= 65536:
            raise ValueError(
                f"mem_size_mib must be between 128 and 65536 (inclusive), got {self.mem_size_mib}"
            )


@dataclass
class VMInstance:
    """VM instance metadata.

    Attributes:
        name: VM name (matches VMConfig.name).
        id: Full 64-char SHA256 hex string (unique identifier).
        pid: PID of the running firecracker process (None if stopped).
        socket_path: Path to Firecracker API socket (if enabled).
        ip: Assigned guest IP address.
        mac: Assigned guest MAC address.
        network_name: Name of the attached named network (if any).
        created_at: Creation timestamp (UTC).
        status: Current lifecycle state.
        config: Original VM configuration used to launch this instance.
    """

    name: str
    id: str = ""  # Full 64-char SHA256 hex string
    pid: int | None = None
    socket_path: Path | None = None
    ip: str | None = None
    mac: str | None = None
    network_name: str | None = None
    tap_device: str | None = None
    created_at: datetime = field(default_factory=lambda: datetime.now(tz=timezone.utc))
    status: VMState = VMState.STOPPED
    config: VMConfig | None = None
