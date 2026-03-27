"""Firecracker JSON config generation."""

import json
import re
from pathlib import Path
from typing import TypedDict

from mvmctl.constants import (
    DEFAULT_BOOT_CONSOLE,
    DEFAULT_BOOT_PANIC,
    DEFAULT_BOOT_PCI_OFF,
    DEFAULT_BOOT_REBOOT,
    DEFAULT_CLOUD_INIT_DRIVE_ID,
    DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_NOCLOUD,
    DEFAULT_FC_DRIVE_CACHE_TYPE,
    DEFAULT_FC_DRIVE_IO_ENGINE,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_LOG_LEVEL,
    DEFAULT_FC_METRICS_FILENAME,
    DEFAULT_GUEST_MAC_DEFAULT,
    DEFAULT_GUEST_NETWORK_IFACE,
)
from mvmctl.exceptions import MVMError
from mvmctl.models.vm import CloudInitMode, VMConfig
from mvmctl.utils.fs import get_vm_dir
from mvmctl.utils.validation import validate_boot_arg_component


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
        "logger": LoggerConfig | None,
        "metrics": MetricsConfig | None,
    },
)


class ConfigGenerator:
    """Generates Firecracker JSON configuration."""

    def __init__(self, vm_config: VMConfig):
        self.vm_config = vm_config

    def validate(self) -> None:
        if self.vm_config.boot_args:
            for component in self.vm_config.boot_args.split():
                validate_boot_arg_component(component, "boot_args")
        self._validate_boot_components()

    def _validate_boot_components(self) -> None:
        if not self.vm_config.gateway:
            raise MVMError("VM gateway IP is required but not set")
        if not self.vm_config.subnet_mask:
            raise MVMError("VM subnet mask is required but not set")

        if self.vm_config.guest_ip:
            validate_boot_arg_component(self.vm_config.guest_ip, "guest_ip")
        validate_boot_arg_component(self.vm_config.gateway, "gateway")
        validate_boot_arg_component(self.vm_config.subnet_mask, "subnet_mask")

        lsm_flags = self.vm_config.lsm_flags or None
        if lsm_flags:
            validate_boot_arg_component(lsm_flags, "lsm_flags")

    def generate(self) -> FirecrackerConfig:
        boot_args = self.vm_config.boot_args or self._build_default_boot_args()
        boot_args = self._ensure_root_uuid_in_boot_args(boot_args)

        context = {
            "kernel_image_path": str(self.vm_config.kernel_path),
            "boot_args": boot_args,
            "drives": json.dumps(self._build_drives_config()),
            "network_interfaces": json.dumps(self._build_network_config()),
            "vcpu_count": str(self.vm_config.vcpu_count),
            "mem_size_mib": str(self.vm_config.mem_size_mib),
            "logger": json.dumps(self._build_logger_config()),
            "metrics": json.dumps(self._build_metrics_config()),
        }

        template_path = Path(__file__).parent.parent / "assets" / "firecracker.template.json"
        try:
            template_str = template_path.read_text(encoding="utf-8")
        except OSError as exc:
            raise MVMError(f"Failed to load Firecracker config template: {template_path}") from exc

        filled = template_str
        for key, value in context.items():
            placeholder = "{" + key + "}"
            if placeholder not in filled:
                raise MVMError(
                    f"Missing template placeholder '{placeholder}' in firecracker.template.json"
                )
            filled = filled.replace(placeholder, value)

        try:
            config: FirecrackerConfig = json.loads(filled)
        except json.JSONDecodeError as exc:
            raise MVMError(f"Generated Firecracker config is not valid JSON: {exc}") from exc

        return config

    def _build_drives_config(self) -> list[DriveConfig]:
        drives: list[DriveConfig] = []

        # Rootfs drive
        rootfs: DriveConfig = {
            "drive_id": "rootfs",
            "path_on_host": str(self.vm_config.rootfs_path),
            "is_root_device": True,
            "is_read_only": False,
            "partuuid": None,
            "cache_type": DEFAULT_FC_DRIVE_CACHE_TYPE,
            "io_engine": DEFAULT_FC_DRIVE_IO_ENGINE,
            "rate_limiter": None,
            "socket": None,
        }
        drives.append(rootfs)

        # Cloud-init ISO drive (if configured)
        if (
            self.vm_config.cloud_init_mode != CloudInitMode.DISABLED
            and self.vm_config.cloud_init_iso_path is not None
        ):
            cloud_init_drive: DriveConfig = {
                "drive_id": DEFAULT_CLOUD_INIT_DRIVE_ID,
                "path_on_host": str(self.vm_config.cloud_init_iso_path),
                "is_root_device": False,
                "is_read_only": True,
                "partuuid": None,
                "cache_type": DEFAULT_FC_DRIVE_CACHE_TYPE,
                "io_engine": DEFAULT_FC_DRIVE_IO_ENGINE,
                "rate_limiter": None,
                "socket": None,
            }
            drives.append(cloud_init_drive)

        # Extra drives
        drives.extend(self.vm_config.extra_drives)

        return drives

    def _build_logger_config(self) -> LoggerConfig | None:
        if not self.vm_config.enable_logging:
            return None
        return {
            "log_path": str(get_vm_dir(self.vm_config.name) / DEFAULT_FC_LOG_FILENAME),
            "level": DEFAULT_FC_LOG_LEVEL,
            "show_level": True,
            "show_log_origin": True,
        }

    def _build_metrics_config(self) -> MetricsConfig | None:
        if not self.vm_config.enable_metrics:
            return None
        return {
            "metrics_path": str(get_vm_dir(self.vm_config.name) / DEFAULT_FC_METRICS_FILENAME),
        }

    def _build_default_boot_args(self) -> str:
        pci_arg = DEFAULT_BOOT_PCI_OFF if not self.vm_config.enable_pci else ""
        gateway = self.vm_config.gateway or ""
        subnet_mask = self.vm_config.subnet_mask or ""

        ip_arg = (
            f"ip={self.vm_config.guest_ip}::{gateway}:{subnet_mask}::eth0:off"
            if self.vm_config.guest_ip
            else ""
        )
        lsm_flags = self.vm_config.lsm_flags or None
        lsm_arg = f"lsm={lsm_flags}" if lsm_flags else ""

        # Determine cloud-init datasource string
        if self.vm_config.datasource_mode == CloudInitMode.NO_CLOUD_NET:
            # For nocloud-net, use network datasource with gateway as HTTP server
            ds_arg = f"ds=nocloud-net;s=http://{gateway}:80/"
        elif self.vm_config.cloud_init_mode == CloudInitMode.DISABLED:
            ds_arg = ""
        else:
            ds_arg = DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_NOCLOUD

        root_arg = (
            f"root=UUID={self.vm_config.root_uuid}" if self.vm_config.root_uuid else "root=/dev/vda"
        )

        parts = [
            DEFAULT_BOOT_CONSOLE,
            DEFAULT_BOOT_REBOOT,
            DEFAULT_BOOT_PANIC,
            pci_arg,
            ip_arg,
            root_arg,
            "rw",
            "rootwait",
            "rootfstype=ext4",
            ds_arg,
            lsm_arg,
        ]
        return " ".join(p for p in parts if p).strip()

    def _ensure_root_uuid_in_boot_args(self, boot_args: str) -> str:
        root_uuid = self.vm_config.root_uuid
        if not root_uuid:
            return boot_args

        replacement = f"root=UUID={root_uuid}"
        if re.search(r"\broot=[^\s]+", boot_args):
            return re.sub(r"\broot=[^\s]+", replacement, boot_args, count=1)
        return f"{boot_args} {replacement}".strip()

    def _build_network_config(self) -> list[NetworkInterfaceConfig]:
        if not self.vm_config.tap_device:
            return []

        return [
            {
                "iface_id": DEFAULT_GUEST_NETWORK_IFACE,
                "guest_mac": self.vm_config.guest_mac or DEFAULT_GUEST_MAC_DEFAULT,
                "host_dev_name": self.vm_config.tap_device,
            }
        ]

    def write_to_file(self, path: Path) -> None:
        self.validate()
        config = self.generate()
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w") as f:
            json.dump(config, f, indent=2)
