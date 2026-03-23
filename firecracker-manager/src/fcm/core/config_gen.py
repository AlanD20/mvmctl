"""Firecracker JSON config generation."""

import json
from pathlib import Path

from fcm.models.vm import VMConfig
from fcm.utils.fs import get_vm_dir
from fcm.utils.validation import validate_boot_arg_component



from typing import TypedDict

class BootSourceConfig(TypedDict):
    kernel_image_path: str
    boot_args: str

class DriveConfig(TypedDict):
    drive_id: str
    path_on_host: str
    is_root_device: bool
    is_read_only: bool
    partuuid: str | None
    cache_type: str
    io_engine: str
    rate_limiter: object | None
    socket: str | None

class NetworkInterfaceConfig(TypedDict):
    iface_id: str
    guest_mac: str
    host_dev_name: str

class MachineConfig(TypedDict):
    vcpu_count: int
    mem_size_mib: int
    smt: bool
    cpu_template: str | None

class LoggerConfig(TypedDict):
    log_path: str
    level: str
    show_level: bool
    show_log_origin: bool

class MetricsConfig(TypedDict):
    metrics_path: str

FirecrackerConfig = TypedDict(
    "FirecrackerConfig",
    {
        "boot-source": BootSourceConfig,
        "drives": list[DriveConfig],
        "network-interfaces": list[NetworkInterfaceConfig],
        "machine-config": MachineConfig,
        "cpu-config": object | None,
        "balloon": object | None,
        "vsock": object | None,
        "logger": LoggerConfig,
        "metrics": MetricsConfig,
    },
)

class ConfigGenerator:
    """Generates Firecracker JSON configuration."""

    def __init__(self, vm_config: VMConfig):
        self.vm_config = vm_config

    def generate(self) -> FirecrackerConfig:
        """Generate Firecracker config dictionary."""
        if self.vm_config.boot_args:
            for component in self.vm_config.boot_args.split():
                validate_boot_arg_component(component, "boot_args")
            boot_args = self.vm_config.boot_args
        else:
            boot_args = self._build_default_boot_args()

        return {
            "boot-source": {
                "kernel_image_path": str(self.vm_config.kernel_path),
                "boot_args": boot_args,
            },
            "drives": [
                {
                    "drive_id": "rootfs",
                    "path_on_host": str(self.vm_config.rootfs_path),
                    "is_root_device": True,
                    "is_read_only": False,
                    "partuuid": None,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                    "rate_limiter": None,
                    "socket": None,
                }
            ],
            "network-interfaces": self._build_network_config(),
            "machine-config": {
                "vcpu_count": self.vm_config.vcpu_count,
                "mem_size_mib": self.vm_config.mem_size_mib,
                "smt": False,
                "cpu_template": None,
            },
            "cpu-config": None,
            "balloon": None,
            "vsock": None,
            "logger": {
                "log_path": str(self._get_log_path()),
                "level": "Debug",
                "show_level": True,
                "show_log_origin": True,
            },
            "metrics": {
                "metrics_path": str(self._get_metrics_path()),
            },
        }

    def _build_default_boot_args(self) -> str:
        """Build default boot arguments."""
        pci_arg = "pci=off" if not self.vm_config.enable_pci else ""
        gateway = self.vm_config.gateway or "10.20.0.1"
        subnet_mask = self.vm_config.subnet_mask or "255.255.255.0"

        # Validate user-controllable boot arg components
        if self.vm_config.guest_ip:
            validate_boot_arg_component(self.vm_config.guest_ip, "guest_ip")
        validate_boot_arg_component(gateway, "gateway")
        validate_boot_arg_component(subnet_mask, "subnet_mask")

        ip_arg = (
            f"ip={self.vm_config.guest_ip}::{gateway}:{subnet_mask}::eth0:off"
            if self.vm_config.guest_ip
            else ""
        )
        lsm_flags = getattr(self.vm_config, "lsm_flags", None)
        if lsm_flags:
            validate_boot_arg_component(lsm_flags, "lsm_flags")
        lsm_arg = f"lsm={lsm_flags}" if lsm_flags else ""
        parts = [
            "console=ttyS0",
            "reboot=k",
            "panic=1",
            pci_arg,
            ip_arg,
            "rw",
            "rootwait",
            "rootfstype=ext4",
            "ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/",
            lsm_arg,
        ]
        return " ".join(p for p in parts if p).strip()

    def _build_network_config(self) -> list[NetworkInterfaceConfig]:
        """Build network interface configuration."""
        if not self.vm_config.tap_device:
            return []

        return [
            {
                "iface_id": "eth0",
                "guest_mac": self.vm_config.guest_mac or "02:FC:00:00:00:01",
                "host_dev_name": self.vm_config.tap_device,
            }
        ]

    def _get_log_path(self) -> Path:
        """Get path for firecracker.log."""
        return get_vm_dir(self.vm_config.name) / "firecracker.log"

    def _get_metrics_path(self) -> Path:
        """Get path for metrics file."""
        return get_vm_dir(self.vm_config.name) / "firecracker.metrics"

    def write_to_file(self, path: Path) -> None:
        """Write config to JSON file."""
        config = self.generate()
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w") as f:
            json.dump(config, f, indent=2)
