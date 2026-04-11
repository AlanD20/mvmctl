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
    DEFAULT_GUEST_NETWORK_BOOT_MODE,
    DEFAULT_GUEST_NETWORK_IFACE,
    DEFAULT_LIBGUESTFS_SEED_DIR,
    DEFAULT_VM_ROOT_FS_TYPE,
)
from mvmctl.exceptions import ConfigError, MVMError
from mvmctl.models import CloudInitMode, VMConfig
from mvmctl.models.vm import VMInstance
from mvmctl.utils.fs import get_vm_dir
from mvmctl.utils.validation import validate_boot_arg_component, validate_fs_type, validate_fs_uuid


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


FirecrackerBootConfig = TypedDict(
    "FirecrackerBootConfig",
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

    def __init__(self, vm_config: VMConfig, instance: VMInstance, vm_dir: Path | None = None):
        self.vm_config = vm_config
        self.instance = instance
        self.vm_dir = vm_dir

    def validate(self) -> None:
        if self.vm_config.boot_args:
            for component in self.vm_config.boot_args.split():
                validate_boot_arg_component(component, "boot_args")

        # Validate root UUID and filesystem type
        validate_fs_uuid(self.vm_config.root_uuid, "root_uuid")
        validate_fs_type(self.vm_config.root_fs_type, "root_fs_type")

        self._validate_boot_components()

    def _validate_boot_components(self) -> None:
        if not self.instance.ipv4_gateway:
            raise MVMError("VM IPv4 gateway is required but not set")
        if not self.instance.subnet_mask:
            raise MVMError("VM subnet mask is required but not set")

        if self.instance.ipv4:
            validate_boot_arg_component(self.instance.ipv4, "guest_ip")
        validate_boot_arg_component(self.instance.ipv4_gateway, "ipv4_gateway")
        validate_boot_arg_component(self.instance.subnet_mask, "subnet_mask")

        lsm_flags = self.vm_config.lsm_flags or None
        if lsm_flags:
            validate_boot_arg_component(lsm_flags, "lsm_flags")

    def generate(self) -> FirecrackerBootConfig:
        boot_args = self.vm_config.boot_args or self._build_default_boot_args()
        boot_args = self._ensure_root_uuid_in_boot_args(boot_args)

        context = {
            "kernel_image_path": str(self.vm_config.kernel_path)
            if self.vm_config.kernel_path
            else "vmlinux",
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
            config: FirecrackerBootConfig = json.loads(filled)
        except json.JSONDecodeError as exc:
            raise MVMError(f"Generated Firecracker config is not valid JSON: {exc}") from exc

        return config

    def _build_drives_config(self) -> list[DriveConfig]:
        drives: list[DriveConfig] = []

        # Rootfs drive
        rootfs: DriveConfig = {
            "drive_id": "rootfs",
            "path_on_host": str(self.vm_config.rootfs_path)
            if self.vm_config.rootfs_path
            else "rootfs.ext4",
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
            self.vm_config.cloud_init_mode != CloudInitMode.OFF
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
        # Use vm_dir if provided (hash-based), otherwise fall back to name-based lookup
        vm_dir = self.vm_dir if self.vm_dir is not None else get_vm_dir(self.vm_config.name)
        return {
            "log_path": str(vm_dir / DEFAULT_FC_LOG_FILENAME),
            "level": DEFAULT_FC_LOG_LEVEL,
            "show_level": True,
            "show_log_origin": True,
        }

    def _build_metrics_config(self) -> MetricsConfig | None:
        if not self.vm_config.enable_metrics:
            return None
        # Use vm_dir if provided (hash-based), otherwise fall back to name-based lookup
        vm_dir = self.vm_dir if self.vm_dir is not None else get_vm_dir(self.vm_config.name)
        return {
            "metrics_path": str(vm_dir / DEFAULT_FC_METRICS_FILENAME),
        }

    def _build_default_boot_args(self) -> str:
        pci_arg = DEFAULT_BOOT_PCI_OFF if not self.vm_config.enable_pci else ""
        ipv4_gateway = self.instance.ipv4_gateway or ""
        subnet_mask = self.instance.subnet_mask or ""

        # Use static kernel ip= parameter for early network bringup
        # This ensures network is ready before cloud-init runs
        # For NO_CLOUD_NET mode, also include kernel ip= for initial network bringup
        # cloud-init's network-config will ensure the IP stays consistent
        if self.instance.ipv4:
            ip_arg = f"ip={self.instance.ipv4}::{ipv4_gateway}:{subnet_mask}::eth0:{DEFAULT_GUEST_NETWORK_BOOT_MODE}"
        else:
            ip_arg = ""
        lsm_flags = self.vm_config.lsm_flags or None
        lsm_arg = f"lsm={lsm_flags}" if lsm_flags else ""

        # Determine cloud-init datasource string
        if self.vm_config.cloud_init_mode == CloudInitMode.NET:
            # For nocloud-net, validate URL is configured
            if not self.vm_config.nocloud_net_url:
                raise ConfigError("nocloud_net_url must be set when using NO_CLOUD_NET mode")
            ds_arg = f"ds=nocloud;seedfrom={self.vm_config.nocloud_net_url}"
            # Mask systemd-networkd-wait-online to prevent 2+ minute boot delay
            # The kernel ip= parameter already configures the network; this service
            # would block waiting for systemd-networkd to mark it as "online"
            mask_arg = "systemd.mask=systemd-networkd-wait-online.service"
        elif self.vm_config.cloud_init_mode == CloudInitMode.INJECT:
            ds_arg = f"ds=nocloud;s=file://{DEFAULT_LIBGUESTFS_SEED_DIR}/"
            mask_arg = "systemd.mask=systemd-networkd-wait-online.service"
        elif self.vm_config.cloud_init_mode == CloudInitMode.ISO:
            # ISO mode: local nocloud datasource
            ds_arg = DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_NOCLOUD
            # Also mask systemd-networkd-wait-online for ISO mode
            mask_arg = "systemd.mask=systemd-networkd-wait-online.service"
        elif self.vm_config.cloud_init_mode == CloudInitMode.OFF:
            ds_arg = ""
            mask_arg = ""
        else:
            ds_arg = DEFAULT_CLOUD_INIT_KERNEL_CMDLINE_NOCLOUD
            mask_arg = ""

        # Build root argument with validation
        if self.vm_config.root_uuid:
            # Validate UUID format before using
            validate_fs_uuid(self.vm_config.root_uuid)
            root_arg = f"root=UUID={self.vm_config.root_uuid}"
        else:
            root_arg = "root=/dev/vda"

        # Validate filesystem type
        fs_type = self.vm_config.root_fs_type or DEFAULT_VM_ROOT_FS_TYPE
        validate_fs_type(fs_type)

        parts = [
            DEFAULT_BOOT_CONSOLE,
            DEFAULT_BOOT_REBOOT,
            DEFAULT_BOOT_PANIC,
            "net.ifnames=0",  # prevent interface renaming
            pci_arg,
            ip_arg,
            root_arg,
            "rw",
            "rootwait",
            f"rootfstype={fs_type}",
            ds_arg,
            mask_arg,
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
        if not self.instance.tap_device:
            return []

        return [
            {
                "iface_id": DEFAULT_GUEST_NETWORK_IFACE,
                "guest_mac": self.instance.mac or DEFAULT_GUEST_MAC_DEFAULT,
                "host_dev_name": self.instance.tap_device,
            }
        ]

    def write_to_file(self, path: Path) -> None:
        config = self.generate()
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w") as f:
            json.dump(config, f)
