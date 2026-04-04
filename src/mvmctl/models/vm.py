"""VM data models."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum, auto
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.models.cloud_init import CloudInitMode

if TYPE_CHECKING:
    from mvmctl.core.config_gen import DriveConfig

from mvmctl.constants import (
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_CONSOLE,
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
        cloud_init_iso_path: Path to custom cloud-init ISO (used when mode is CUSTOM).
        keep_cloud_init_iso: Retain the generated cloud-init ISO after boot.
        root_fs_type: Filesystem type of the root image (e.g. ext4, btrfs, xfs).
    """

    name: str
    vcpu_count: int = DEFAULT_VM_VCPU_COUNT
    mem_size_mib: int = DEFAULT_VM_MEM_MIB
    kernel_path: Path = field(default_factory=lambda: Path(DEFAULT_VM_KERNEL_FILENAME))
    rootfs_path: Path = field(default_factory=lambda: Path(DEFAULT_VM_ROOTFS_FILENAME))
    guest_ip: str | None = None
    guest_mac: str | None = None
    ipv4_gateway: str | None = None
    subnet_mask: str = DEFAULT_VM_SUBNET_MASK
    tap_device: str | None = None
    boot_args: str | None = None
    root_uuid: str | None = None
    root_fs_type: str | None = None
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI
    lsm_flags: str = DEFAULT_VM_LSM_FLAGS
    extra_drives: list[DriveConfig] = field(default_factory=list)
    enable_logging: bool = DEFAULT_VM_ENABLE_LOGGING
    enable_metrics: bool = DEFAULT_VM_ENABLE_METRICS
    enable_console: bool = DEFAULT_VM_ENABLE_CONSOLE
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT
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

    def to_dict(self) -> dict[str, Any]:
        """Serialize VMConfig to a dictionary."""
        return {
            "name": self.name,
            "vcpu_count": self.vcpu_count,
            "mem_size_mib": self.mem_size_mib,
            "kernel_path": str(self.kernel_path),
            "rootfs_path": str(self.rootfs_path),
            "guest_ip": self.guest_ip,
            "guest_mac": self.guest_mac,
            "ipv4_gateway": self.ipv4_gateway,
            "subnet_mask": self.subnet_mask,
            "tap_device": self.tap_device,
            "boot_args": self.boot_args,
            "root_uuid": self.root_uuid,
            "root_fs_type": self.root_fs_type,
            "enable_api_socket": self.enable_api_socket,
            "enable_pci": self.enable_pci,
            "lsm_flags": self.lsm_flags,
            "extra_drives": [
                {
                    "path_on_host": d["path_on_host"],
                    "drive_id": d["drive_id"],
                    "is_root_device": d["is_root_device"],
                }
                for d in self.extra_drives
            ],
            "enable_logging": self.enable_logging,
            "enable_metrics": self.enable_metrics,
            "enable_console": self.enable_console,
            "cloud_init_mode": self.cloud_init_mode.value,
            "cloud_init_iso_path": str(self.cloud_init_iso_path)
            if self.cloud_init_iso_path
            else None,
            "keep_cloud_init_iso": self.keep_cloud_init_iso,
            "nocloud_net_url": self.nocloud_net_url,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> VMConfig:
        """Deserialize VMConfig from a dictionary."""
        from typing import cast

        from mvmctl.core.config_gen import DriveConfig

        extra_drives = []
        for d in data.get("extra_drives", []):
            extra_drives.append(
                cast(
                    DriveConfig,
                    {
                        "drive_id": d.get("drive_id", ""),
                        "path_on_host": d.get("path_on_host", ""),
                        "is_root_device": d.get("is_root_device", False),
                        "is_read_only": d.get("is_read_only", False),
                        "partuuid": d.get("partuuid"),
                        "cache_type": d.get("cache_type", ""),
                        "io_engine": d.get("io_engine", ""),
                        "rate_limiter": d.get("rate_limiter"),
                        "socket": d.get("socket"),
                    },
                )
            )

        return cls(
            name=data.get("name", ""),
            vcpu_count=data.get("vcpu_count", DEFAULT_VM_VCPU_COUNT),
            mem_size_mib=data.get("mem_size_mib", DEFAULT_VM_MEM_MIB),
            kernel_path=Path(data.get("kernel_path", DEFAULT_VM_KERNEL_FILENAME)),
            rootfs_path=Path(data.get("rootfs_path", DEFAULT_VM_ROOTFS_FILENAME)),
            guest_ip=data.get("guest_ip"),
            guest_mac=data.get("guest_mac"),
            ipv4_gateway=data.get("ipv4_gateway"),
            subnet_mask=data.get("subnet_mask", DEFAULT_VM_SUBNET_MASK),
            tap_device=data.get("tap_device"),
            boot_args=data.get("boot_args"),
            root_uuid=data.get("root_uuid"),
            root_fs_type=data.get("root_fs_type"),
            enable_api_socket=data.get("enable_api_socket", DEFAULT_VM_ENABLE_API_SOCKET),
            enable_pci=data.get("enable_pci", DEFAULT_VM_ENABLE_PCI),
            lsm_flags=data.get("lsm_flags", DEFAULT_VM_LSM_FLAGS),
            extra_drives=extra_drives,
            enable_logging=data.get("enable_logging", DEFAULT_VM_ENABLE_LOGGING),
            enable_metrics=data.get("enable_metrics", DEFAULT_VM_ENABLE_METRICS),
            enable_console=data.get("enable_console", DEFAULT_VM_ENABLE_CONSOLE),
            cloud_init_mode=CloudInitMode(data["cloud_init_mode"])
            if data.get("cloud_init_mode")
            else CloudInitMode.INJECT,
            cloud_init_iso_path=Path(data["cloud_init_iso_path"])
            if data.get("cloud_init_iso_path")
            else None,
            keep_cloud_init_iso=data.get("keep_cloud_init_iso", False),
            nocloud_net_url=data.get("nocloud_net_url"),
        )


@dataclass
class VMInstance:
    """VM instance metadata.

    Attributes:
        name: VM name (matches VMConfig.name).
        id: Full 16-char hex string (unique identifier).
        pid: PID of the running firecracker process (None if stopped).
        socket_path: Path to Firecracker API socket (if enabled).
        ip: Assigned guest IP address.
        mac: Assigned guest MAC address.
        network_name: Name of the attached named network (if any).
        tap_device: TAP device name for this VM's network interface.
        created_at: Creation timestamp (UTC).
        status: Current lifecycle state.
        config: Original VM configuration used to launch this instance.
        cloud_init_mode: Cloud-init configuration mode for this VM instance.
        nocloud_net_port: HTTP port for nocloud-net datasource server (if enabled).
        nocloud_server_pid: PID of the running nocloud-net HTTP server process (None if stopped).
        rootfs_suffix: File extension suffix of the rootfs image (e.g., '.ext4', '.btrfs').
        kernel_id: Path or hash ID of the kernel used by this VM (for asset removal protection).
        image_id: Path or hash ID of the image used by this VM (for asset removal protection).
    """

    name: str
    id: str = ""  # Full 16-char hex string
    pid: int | None = None
    api_socket_path: Path | None = None
    ipv4: str | None = None
    mac: str | None = None
    network_name: str | None = None
    tap_device: str | None = None
    created_at: datetime = field(default_factory=lambda: datetime.now(tz=timezone.utc))
    status: VMStatus = VMStatus.STOPPED
    config: VMConfig | None = None
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT
    nocloud_net_port: int | None = None
    nocloud_server_pid: int | None = None
    console_relay_pid: int | None = None
    console_socket_path: Path | None = None
    exit_code: int | None = None
    rootfs_suffix: str = ".ext4"  # Default to .ext4 for backward compatibility
    kernel_id: str | None = None  # Kernel path or hash for asset removal protection
    image_id: str | None = None  # Image path or hash for asset removal protection

    def to_dict(self) -> dict[str, Any]:
        """Serialize VMInstance to a dictionary."""
        return {
            "name": self.name,
            "id": self.id,
            "pid": self.pid,
            "api_socket_path": str(self.api_socket_path) if self.api_socket_path else None,
            "ipv4": self.ipv4,
            "mac": self.mac,
            "network_name": self.network_name,
            "tap_device": self.tap_device,
            "created_at": self.created_at.isoformat(),
            "status": self.status.value,
            "config": self.config.to_dict() if self.config else None,
            "cloud_init_mode": self.cloud_init_mode.value,
            "nocloud_net_port": self.nocloud_net_port,
            "nocloud_server_pid": self.nocloud_server_pid,
            "console_relay_pid": self.console_relay_pid,
            "console_socket_path": str(self.console_socket_path)
            if self.console_socket_path
            else None,
            "exit_code": self.exit_code,
            "rootfs_suffix": self.rootfs_suffix,
            "kernel_id": self.kernel_id,
            "image_id": self.image_id,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> VMInstance:
        """Deserialize VMInstance from a dictionary."""
        config = None
        if data.get("config") and isinstance(data["config"], dict):
            config = VMConfig.from_dict(data["config"])

        cloud_init_mode = CloudInitMode.INJECT
        if data.get("cloud_init_mode"):
            cloud_init_mode = CloudInitMode(data["cloud_init_mode"])

        created_at = datetime.now(tz=timezone.utc)
        if data.get("created_at"):
            try:
                created_at = datetime.fromisoformat(data["created_at"])
            except ValueError:
                pass

        return cls(
            name=data.get("name", ""),
            id=data.get("id", ""),
            pid=data.get("pid"),
            api_socket_path=Path(data["api_socket_path"]) if data.get("api_socket_path") else None,
            ipv4=data.get("ipv4"),
            mac=data.get("mac"),
            network_name=data.get("network_name"),
            tap_device=data.get("tap_device"),
            created_at=created_at,
            status=VMStatus(data["status"]) if data.get("status") else VMStatus.STOPPED,
            config=config,
            cloud_init_mode=cloud_init_mode,
            nocloud_net_port=data.get("nocloud_net_port"),
            nocloud_server_pid=data.get("nocloud_server_pid"),
            console_relay_pid=data.get("console_relay_pid"),
            console_socket_path=Path(data["console_socket_path"])
            if data.get("console_socket_path")
            else None,
            exit_code=data.get("exit_code"),
            rootfs_suffix=data.get("rootfs_suffix", ".ext4"),
            kernel_id=data.get("kernel_id"),
            image_id=data.get("image_id"),
        )
