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
    """VM static creation parameters.

    Attributes:
        name: VM name; also used as hostname inside the guest.
        vm_id: Pre-generated unique VM ID (16-char hex string).
        vcpu_count: Number of vCPUs to allocate (1-32).
        mem_size_mib: Memory in MiB (128-65536).
        disk_size_mib: Disk size in MiB for the VM root filesystem.
        kernel_path: Path to vmlinux kernel image.
        rootfs_path: Path to root filesystem ext4 image.
        boot_args: Override kernel boot arguments (uses defaults if None).
        enable_api_socket: Enable Firecracker HTTP API socket.
        enable_pci: Enable PCI device support.
        lsm_flags: Linux Security Module flags for the kernel cmdline.
        cloud_init_mode: Cloud-init configuration mode (auto/custom/disabled).
        cloud_init_iso_path: Path to custom cloud-init ISO (used when mode is CUSTOM).
        keep_cloud_init_iso: Retain the generated cloud-init ISO after boot.
        root_fs_type: Filesystem type of the root image (e.g. ext4, btrfs, xfs).
    """

    # Required fields (CLI/API layer resolves these)
    name: str
    vcpu_count: int
    mem_size_mib: int
    disk_size_mib: int
    enable_api_socket: bool
    enable_pci: bool
    lsm_flags: str
    enable_logging: bool
    enable_metrics: bool
    enable_console: bool
    cloud_init_mode: CloudInitMode

    # Optional fields with defaults
    vm_id: str = ""  # Empty string is valid sentinel for "not set"
    kernel_path: Path | None = None
    rootfs_path: Path | None = None
    boot_args: str | None = None
    root_uuid: str | None = None
    root_fs_type: str | None = None
    extra_drives: list[DriveConfig] = field(default_factory=list)  # Technical default OK
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False  # Operational flag, not config-backed
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
            "vm_id": self.vm_id,
            "vcpu_count": self.vcpu_count,
            "mem_size_mib": self.mem_size_mib,
            "kernel_path": str(self.kernel_path),
            "rootfs_path": str(self.rootfs_path),
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
            "cloud_init_iso_path": (
                str(self.cloud_init_iso_path) if self.cloud_init_iso_path else None
            ),
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
            vm_id=data.get("vm_id", ""),
            vcpu_count=data["vcpu_count"],  # Required - no default
            mem_size_mib=data["mem_size_mib"],  # Required - no default
            disk_size_mib=data["disk_size_mib"],  # Required - no default
            kernel_path=Path(data["kernel_path"]) if data.get("kernel_path") else None,
            rootfs_path=Path(data["rootfs_path"]) if data.get("rootfs_path") else None,
            boot_args=data.get("boot_args"),
            root_uuid=data.get("root_uuid"),
            root_fs_type=data.get("root_fs_type"),
            enable_api_socket=data["enable_api_socket"],  # Required - no default
            enable_pci=data["enable_pci"],  # Required - no default
            lsm_flags=data["lsm_flags"],  # Required - no default
            extra_drives=extra_drives,
            enable_logging=data["enable_logging"],  # Required - no default
            enable_metrics=data["enable_metrics"],  # Required - no default
            enable_console=data["enable_console"],  # Required - no default
            cloud_init_mode=(
                CloudInitMode(data["cloud_init_mode"])
                if data.get("cloud_init_mode")
                else CloudInitMode.INJECT
            ),
            cloud_init_iso_path=(
                Path(data["cloud_init_iso_path"]) if data.get("cloud_init_iso_path") else None
            ),
            keep_cloud_init_iso=data.get("keep_cloud_init_iso", False),
            nocloud_net_url=data.get("nocloud_net_url"),
        )


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


@dataclass
class VMInstance:
    """VM instance metadata.

    Attributes:
        name: VM name (matches VMConfig.name).
        id: Full 16-char hex string (unique identifier).
        pid: PID of the running firecracker process.
        api_socket_path: Path to Firecracker API socket (if enabled).
        ipv4: Assigned guest IP address.
        mac: Assigned guest MAC address.
        network_id: ID of the attached named network.
        tap_device: TAP device name for this VM's network interface.
        ipv4_gateway: Host-side gateway IP for the guest (runtime network info).
        subnet_mask: Subnet mask for the guest network (runtime network info).
        created_at: Creation timestamp (UTC).
        updated_at: Last update timestamp (UTC).
        status: Current lifecycle state.
        config: Original VM configuration used to launch this instance.
        config_path: Path to the VM configuration file.
        nocloud_net_port: HTTP port for nocloud-net datasource server (if enabled).
        nocloud_server_pid: PID of the running nocloud-net HTTP server process (None if stopped).
        console_relay_pid: PID of the running console relay process (None if stopped).
        console_socket_path: Path to the console socket (if enabled).
        exit_code: Exit code of the firecracker process (if stopped).
        rootfs_suffix: File extension suffix of the rootfs image (e.g., '.ext4', '.btrfs').
        kernel_id: Hash ID of the kernel used by this VM (for asset removal protection).
        image_id: Hash ID of the image used by this VM (for asset removal protection).
        binary_id: Hash ID of the firecracker binary used by this VM.
        disk_size_mib: Disk size in MiB for the VM root filesystem.
    """

    # Required fields (no defaults)
    name: str
    id: str
    pid: int
    ipv4: str
    mac: str
    network_id: str
    tap_device: str
    created_at: datetime
    updated_at: datetime
    status: VMStatus
    rootfs_suffix: str
    kernel_id: str
    image_id: str
    binary_id: str
    disk_size_mib: int

    # Optional fields
    api_socket_path: Path | None = None
    ipv4_gateway: str | None = None
    subnet_mask: str | None = None
    config: VMConfig | None = None
    config_path: Path | None = None
    nocloud_net_port: int | None = None
    nocloud_server_pid: int | None = None
    console_relay_pid: int | None = None
    console_socket_path: Path | None = None
    exit_code: int | None = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize VMInstance to a dictionary."""
        return {
            "name": self.name,
            "id": self.id,
            "pid": self.pid,
            "api_socket_path": (str(self.api_socket_path) if self.api_socket_path else None),
            "ipv4": self.ipv4,
            "mac": self.mac,
            "network_id": self.network_id,
            "tap_device": self.tap_device,
            "ipv4_gateway": self.ipv4_gateway,
            "subnet_mask": self.subnet_mask,
            "created_at": self.created_at.isoformat(),
            "updated_at": self.updated_at.isoformat(),
            "status": self.status.value,
            "config": self.config.to_dict() if self.config else None,
            "config_path": str(self.config_path) if self.config_path else None,
            "nocloud_net_port": self.nocloud_net_port,
            "nocloud_server_pid": self.nocloud_server_pid,
            "console_relay_pid": self.console_relay_pid,
            "console_socket_path": (
                str(self.console_socket_path) if self.console_socket_path else None
            ),
            "exit_code": self.exit_code,
            "rootfs_suffix": self.rootfs_suffix,
            "kernel_id": self.kernel_id,
            "image_id": self.image_id,
            "binary_id": self.binary_id,
            "disk_size_mib": self.disk_size_mib,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> VMInstance:
        """Deserialize VMInstance from a dictionary."""
        config = None
        if data.get("config") and isinstance(data["config"], dict):
            config = VMConfig.from_dict(data["config"])

        created_at = datetime.now(tz=timezone.utc)
        if data.get("created_at"):
            try:
                created_at = datetime.fromisoformat(data["created_at"])
            except ValueError:
                pass

        updated_at = datetime.now(tz=timezone.utc)
        if data.get("updated_at"):
            try:
                updated_at = datetime.fromisoformat(data["updated_at"])
            except ValueError:
                pass

        return cls(
            name=data.get("name", ""),
            id=data.get("id", ""),
            pid=data.get("pid", 0),
            ipv4=data.get("ipv4", ""),
            mac=data.get("mac", ""),
            network_id=data.get("network_id", ""),
            tap_device=data.get("tap_device", ""),
            created_at=created_at,
            updated_at=updated_at,
            status=VMStatus(data["status"]) if data.get("status") else VMStatus.STOPPED,
            rootfs_suffix=data.get("rootfs_suffix", ""),
            kernel_id=data.get("kernel_id", ""),
            image_id=data.get("image_id", ""),
            binary_id=data.get("binary_id", ""),
            disk_size_mib=data.get("disk_size_mib", 0),
            api_socket_path=(
                Path(data["api_socket_path"]) if data.get("api_socket_path") else None
            ),
            ipv4_gateway=data.get("ipv4_gateway"),
            subnet_mask=data.get("subnet_mask"),
            config=config,
            config_path=Path(data["config_path"]) if data.get("config_path") else None,
            nocloud_net_port=data.get("nocloud_net_port"),
            nocloud_server_pid=data.get("nocloud_server_pid"),
            console_relay_pid=data.get("console_relay_pid"),
            console_socket_path=(
                Path(data["console_socket_path"]) if data.get("console_socket_path") else None
            ),
            exit_code=data.get("exit_code"),
        )


@dataclass
class VMCreateInput:
    """Input model for VM creation — replaces 31 function parameters."""

    # Required fields (no defaults)
    name: str
    vcpu_count: int
    mem_size_mib: int
    ssh_keys: list[str]

    # Optional fields (DB-backed at API layer)
    user: str | None
    enable_pci: bool | None
    enable_console: bool | None
    enable_logging: bool | None
    enable_metrics: bool | None
    firecracker_bin: str | None = None
    image: str | None = None
    kernel: str | None = None
    image_path: Path | None = None
    kernel_path: Path | None = None
    disk_size: str | None = None
    guest_ip: str | None = None
    skip_ci_network_config: bool = False
    boot_args: str | None = None
    lsm_flags: str | None = None
    network_name: str | None = None
    guest_mac: str | None = None
    custom_user_data: Path | None = None
    cloud_init_mode: str | None = None
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False
    nocloud_net_port: int = 0

    # Additional discovered fields
    binary_id: str | None = None
    skip_cleanup: bool = False
