"""Firecracker JSON config generation."""

import json
from pathlib import Path
from typing import Optional

from fcm.models.vm import VMConfig


class ConfigGenerator:
    """Generates Firecracker JSON configuration."""

    def __init__(self, vm_config: VMConfig):
        self.vm_config = vm_config

    def generate(self) -> dict:
        """Generate Firecracker config dictionary."""
        boot_args = self.vm_config.boot_args or self._build_default_boot_args()

        return {
            "boot-source": {
                "kernel_image_path": str(self.vm_config.kernel_path),
                "boot_args": boot_args,
                "initrd_path": None,
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
        ip_arg = (
            f"ip={self.vm_config.guest_ip}::10.10.0.1:255.255.255.252::eth0:off"
            if self.vm_config.guest_ip
            else ""
        )
        return f"console=ttyS0 reboot=k panic=1 {pci_arg} {ip_arg} rw rootwait"

    def _build_network_config(self) -> list:
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
        return Path(f"/tmp/fcm/run/{self.vm_config.name}/firecracker.log")

    def _get_metrics_path(self) -> Path:
        """Get path for metrics file."""
        return Path(f"/tmp/fcm/run/{self.vm_config.name}/firecracker.metrics")

    def write_to_file(self, path: Path) -> None:
        """Write config to JSON file."""
        config = self.generate()
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w") as f:
            json.dump(config, f, indent=2)
