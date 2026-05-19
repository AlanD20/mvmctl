"""VM creation resolver - coordinates _internal resolvers for VM-specific resolution."""

from __future__ import annotations

import ipaddress
import json
import logging
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_MEBIBYTE_BYTES,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_VCPU_MAX,
    CONST_VM_VCPU_MIN,
)
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.core.binary._service import BinaryService
from mvmctl.core.config._service import SettingsService
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.core.volume._service import VolumeService
from mvmctl.exceptions import (
    BinaryNotFoundError,
    CloudInitModeError,
    ImageNotFoundError,
    KernelNotFoundError,
    NetworkNotFoundError,
    VMCreateError,
)
from mvmctl.models import (
    BinaryItem,
    CloudInitMode,
    CpuConfig,
    DriveConfig,
    ImageItem,
    KernelItem,
    NetworkItem,
    ProvisionerType,
    SSHKeyItem,
    VMInstanceItem,
    VolumeItem,
)
from mvmctl.utils._disk import DiskUtils
from mvmctl.utils._validators import NetworkValidator, VMValidator
from mvmctl.utils.common import CommonUtils

logger = logging.getLogger(__name__)

__all__ = ["VMCreateInput", "VMCreateRequest", "ResolvedVMCreateInput"]


@dataclass
class VMCreateInput:
    """Input model for VM creation — replaces 31 function parameters."""

    # Required fields (no defaults)
    name: str
    ssh_keys: list[str]

    # Optional fields with CLI-layer defaults resolved in VMCreateRequest
    vcpu_count: int | None = None
    mem_size_mib: str | None = None
    user: str | None = None
    pci_enabled: bool | None = None
    nested_virt: bool | None = None
    cpu_template: Path | None = None
    cpu_config: CpuConfig | None = None  # Pre-resolved CPU config (from import)
    enable_console: bool | None = None
    enable_logging: bool | None = None
    enable_metrics: bool | None = None
    firecracker_bin: str | None = None
    image: str | None = None
    kernel_id: str | None = None
    binary_id: str | None = None
    disk_size: str | None = None
    requested_guest_ip: str | None = None
    skip_ci_network_config: bool = False
    boot_args: str | None = None
    lsm_flags: str | None = None
    network_name: str | None = None
    requested_guest_mac: str | None = None
    custom_user_data: Path | None = None
    cloud_init_mode: str | None = None
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False
    nocloud_net_port: int | None = None
    skip_cleanup: bool = False
    skip_deblob: bool = False
    count: int | None = None
    atomic: bool = False
    volumes: list[str] | None = None


@dataclass(frozen=True)
class ResolvedVMCreateInput:
    """Immutable resolved inputs - output of VMCreateRequest."""

    name: str
    vm_id: str
    vm_dir: Path
    vcpu_count: int
    mem_size_mib: int
    user: str
    dns_server: str
    root_uid: int
    root_gid: int
    user_uid: int
    user_gid: int
    guest_mac_prefix: str
    network: NetworkItem
    image: ImageItem
    kernel: KernelItem
    binary: BinaryItem
    network_prefix_len: int
    cloud_init_mode: CloudInitMode
    skip_ci_network_config: bool
    pci_enabled: bool
    nested_virt: bool
    enable_console: bool
    enable_logging: bool
    enable_metrics: bool

    keep_cloud_init_iso: bool
    skip_cleanup: bool
    skip_deblob: bool
    network_netmask: str
    disk_size_bytes: int
    disk_size_mib: int

    lsm_flags: str

    # Firecracker
    log_level: str
    log_filename: str
    serial_output_filename: str
    metrics_filename: str
    api_socket_filename: str
    pid_filename: str
    config_filename: str
    console_socket_filename: str
    console_pid_filename: str
    # Cloud-init
    cloud_init_iso_name: str
    nocloud_port_range_start: int
    nocloud_port_range_end: int
    nocloud_max_port_retries: int

    requested_guest_ip: str | None
    requested_guest_mac: str | None
    nocloud_net_port: int | None = None
    custom_user_data_path: Path | None = None
    cloud_init_iso_path: Path | None = None
    cpu_config: CpuConfig | None = None

    boot_args: str | None = None
    ssh_keys: list[SSHKeyItem] = field(default_factory=list)
    provisioner: ProvisionerType = ProvisionerType.LOOP_MOUNT
    extra_drives: list[DriveConfig] = field(default_factory=list)
    volumes: list[VolumeItem] = field(default_factory=list)


@dataclass
class CloudInitModeResolved:
    mode: CloudInitMode
    iso_path: Path | None


class VMCreateRequest:
    """Resolve all DB-backed defaults using a single DB instance."""

    _result: ResolvedVMCreateInput | None = None

    def __init__(
        self,
        *,
        vm_id: str,
        vm_dir: Path,
        inputs: VMCreateInput,
        db: Database | None = None,
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""

        self._vm_id = vm_id
        self._vm_dir = vm_dir
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._vm_repo = VMRepository(self._db)
        self._vol_repo = VolumeRepository(self._db)
        self._network_resolver = NetworkResolver(NetworkRepository(self._db))
        self._binary_resolver = BinaryResolver(BinaryRepository(self._db))
        self._image_resolver = ImageResolver(ImageRepository(self._db))
        self._kernel_resolver = KernelResolver(KernelRepository(self._db))
        self._key_resolver = KeyResolver(KeyRepository(self._db))
        self._volume_resolver = VolumeResolver(VolumeRepository(self._db))

    @property
    def result(self) -> ResolvedVMCreateInput | None:
        return self._result

    def _resolve_setting(self, category: str, key: str) -> Any:
        return SettingsService.resolve(self._db, category, key)

    @classmethod
    def from_vm(
        cls,
        vm: VMInstanceItem,
        *,
        db: Database | None = None,
    ) -> ResolvedVMCreateInput:
        """Build ResolvedVMCreateInput from an enriched VM's stored state for respawn.

        Maps VM fields directly for stored values and resolves DB defaults
        for everything else (filenames, UIDs, network config, etc.).
        """
        _db = db if db is not None else Database()

        if vm.network is None:
            raise NetworkNotFoundError(
                f"Network not found for VM '{vm.name}' (ID: {vm.network_id})"
            )
        if vm.image is None:
            raise ImageNotFoundError(
                f"Image not found for VM '{vm.name}' (ID: {vm.image_id})"
            )
        if vm.kernel is None:
            raise KernelNotFoundError(
                f"Kernel not found for VM '{vm.name}' (ID: {vm.kernel_id})"
            )
        if vm.binary is None:
            raise BinaryNotFoundError(
                f"Binary not found for VM '{vm.name}' (ID: {vm.binary_id})"
            )

        ipv4_net = ipaddress.IPv4Network(vm.network.subnet, strict=False)
        extra_drives = VolumeService.volumes_to_drives(vm.volumes)

        # Deserialize cpu_config if it's a JSON string from DB
        cpu_config = vm.cpu_config
        if isinstance(cpu_config, str):
            cpu_config = json.loads(cpu_config)

        return ResolvedVMCreateInput(
            name=vm.name,
            vm_id=vm.id,
            vm_dir=vm.vm_dir,
            vcpu_count=vm.vcpu_count,
            mem_size_mib=vm.mem_size_mib,
            user=vm.ssh_user
            if vm.ssh_user
            else str(SettingsService.resolve(_db, "defaults.vm", "ssh_user")),
            dns_server=str(
                SettingsService.resolve(_db, "defaults.vm", "dns_server")
            ),
            root_uid=int(
                SettingsService.resolve(_db, "defaults.vm", "root_uid")
            ),
            root_gid=int(
                SettingsService.resolve(_db, "defaults.vm", "root_gid")
            ),
            user_uid=int(
                SettingsService.resolve(_db, "defaults.vm", "user_uid")
            ),
            user_gid=int(
                SettingsService.resolve(_db, "defaults.vm", "user_gid")
            ),
            guest_mac_prefix=str(
                SettingsService.resolve(_db, "defaults.vm", "guest_mac_prefix")
            ),
            network=vm.network,
            image=vm.image,
            kernel=vm.kernel,
            binary=vm.binary,
            network_prefix_len=ipv4_net.prefixlen,
            cloud_init_mode=CloudInitMode(vm.cloud_init_mode)
            if vm.cloud_init_mode
            else CloudInitMode.OFF,
            skip_ci_network_config=False,
            pci_enabled=vm.pci_enabled,
            nested_virt=vm.nested_virt,
            cpu_config=cpu_config,
            enable_console=vm.enable_console,
            enable_logging=vm.enable_logging,
            enable_metrics=vm.enable_metrics,
            keep_cloud_init_iso=False,
            skip_cleanup=False,
            skip_deblob=False,
            network_netmask=str(ipv4_net.netmask),
            disk_size_bytes=vm.disk_size_mib * CONST_MEBIBYTE_BYTES,
            disk_size_mib=vm.disk_size_mib,
            lsm_flags=vm.lsm_flags
            if vm.lsm_flags
            else str(SettingsService.resolve(_db, "defaults.vm", "lsm_flags")),
            log_level=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "log_level"
                )
            ),
            log_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "log_filename"
                )
            ),
            serial_output_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "serial_output_filename"
                )
            ),
            metrics_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "metrics_filename"
                )
            ),
            api_socket_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "api_socket_filename"
                )
            ),
            pid_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "pid_filename"
                )
            ),
            config_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "config_filename"
                )
            ),
            console_socket_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "console_socket_filename"
                )
            ),
            console_pid_filename=str(
                SettingsService.resolve(
                    _db, "defaults.firecracker", "console_pid_filename"
                )
            ),
            cloud_init_iso_name=str(
                SettingsService.resolve(_db, "defaults.cloudinit", "iso_name")
            ),
            nocloud_port_range_start=int(
                SettingsService.resolve(
                    _db, "defaults.cloudinit", "nocloud_port_range_start"
                )
            ),
            nocloud_port_range_end=int(
                SettingsService.resolve(
                    _db, "defaults.cloudinit", "nocloud_port_range_end"
                )
            ),
            nocloud_max_port_retries=int(
                SettingsService.resolve(
                    _db, "defaults.cloudinit", "nocloud_max_port_retries"
                )
            ),
            requested_guest_ip=vm.ipv4,
            requested_guest_mac=vm.mac,
            nocloud_net_port=vm.nocloud_net_port,
            custom_user_data_path=None,
            cloud_init_iso_path=None,
            boot_args=vm.boot_args
            if vm.boot_args
            else str(SettingsService.resolve(_db, "defaults.vm", "boot_args")),
            ssh_keys=[],
            provisioner=ProvisionerType.LOOP_MOUNT,
            volumes=vm.volumes,
            extra_drives=extra_drives,
        )

    def resolve(self) -> ResolvedVMCreateInput:
        """Resolve all inputs to explicit values."""

        # Validate VM name early — before any DB or subprocess calls
        VMValidator.validate_name(self._inputs.name)

        image = self._resolve_image()
        kernel = self._resolve_kernel()
        network = self._resolve_network()
        fc_binary = self._resolve_binary()
        ssh_keys = self._resolve_ssh_keys()
        volumes = self._resolve_volumes()

        extra_drives = VolumeService.volumes_to_drives(volumes)
        ipv4_net = ipaddress.IPv4Network(network.subnet, strict=False)
        network_prefix_len = ipv4_net.prefixlen
        network_netmask = ipv4_net.netmask

        if self._inputs.disk_size is not None:
            rootfs_disk_size_mib = (
                DiskUtils.parse_disk_size_to_bytes(self._inputs.disk_size)
                // CONST_MEBIBYTE_BYTES
            )
        else:
            rootfs_disk_size_mib = image.minimum_rootfs_size_mib

        rootfs_disk_size_bytes = rootfs_disk_size_mib * CONST_MEBIBYTE_BYTES

        # ── Resolve mem_size_mib: human-readable (512M, 1G) or raw MiB ints ──
        if self._inputs.mem_size_mib is not None:
            mem_str = self._inputs.mem_size_mib.strip()
            try:
                mem_mib = int(mem_str)
            except ValueError:
                mem_mib = (
                    DiskUtils.parse_disk_size_to_bytes(mem_str)
                    // CONST_MEBIBYTE_BYTES
                )
        else:
            mem_mib = self._resolve_setting("defaults.vm", "mem_size_mib")

        ci_mode_result = self._resolve_cloud_init_mode()
        provisioner = self._resolve_provisioner()

        # ── Resolve nested_virt and cpu_config ──────────────────────────
        nested_virt = self._inputs.nested_virt
        if nested_virt is None:
            nested_virt = bool(
                self._resolve_setting("defaults.vm", "nested_virt")
            )
        else:
            nested_virt = bool(nested_virt)

        # Resolve CPU config: from cpu_template (CLI) or cpu_config (import)
        cpu_config: dict[str, Any] | None = self._inputs.cpu_config  # type: ignore[assignment]
        if self._inputs.cpu_template is not None:
            if cpu_config is not None:
                raise VMCreateError(
                    "Cannot specify both --cpu-template and a pre-resolved cpu_config"
                )
            try:
                cpu_config = json.loads(self._inputs.cpu_template.read_text())
            except json.JSONDecodeError as e:
                raise VMCreateError(f"Invalid CPU template JSON: {e}") from e
            if not isinstance(cpu_config, dict):
                raise VMCreateError("CPU template must be a JSON object")

        # Merge with nested virt base if nested_virt is enabled
        if nested_virt:
            base: dict[str, Any] = {"kvm_capabilities": []}
            if cpu_config is not None:
                cpu_config = CommonUtils.deep_merge_dict(base, cpu_config)
            else:
                cpu_config = base

        # Nested virt forces PCI on
        pci_enabled = self._inputs.pci_enabled
        if pci_enabled is not None:
            pci_enabled = bool(pci_enabled)
        else:
            pci_enabled = bool(
                self._resolve_setting("defaults.vm", "pci_enabled")
            )
        if nested_virt:
            pci_enabled = True

        self._result = ResolvedVMCreateInput(
            name=self._inputs.name,
            vm_id=self._vm_id,
            vm_dir=self._vm_dir,
            vcpu_count=self._inputs.vcpu_count
            if self._inputs.vcpu_count is not None
            else self._resolve_setting("defaults.vm", "vcpu_count"),
            mem_size_mib=mem_mib,
            user=self._inputs.user
            if self._inputs.user is not None
            else self._resolve_setting("defaults.vm", "ssh_user"),
            dns_server=str(self._resolve_setting("defaults.vm", "dns_server")),
            root_uid=int(self._resolve_setting("defaults.vm", "root_uid")),
            root_gid=int(self._resolve_setting("defaults.vm", "root_gid")),
            user_uid=int(self._resolve_setting("defaults.vm", "user_uid")),
            user_gid=int(self._resolve_setting("defaults.vm", "user_gid")),
            guest_mac_prefix=str(
                self._resolve_setting("defaults.vm", "guest_mac_prefix")
            ),
            network=network,
            image=image,
            kernel=kernel,
            binary=fc_binary,
            cloud_init_mode=ci_mode_result.mode,
            pci_enabled=pci_enabled,
            nested_virt=nested_virt,
            cpu_config=cpu_config,  # type: ignore[arg-type]
            enable_console=self._inputs.enable_console
            if self._inputs.enable_console is not None
            else self._resolve_setting("defaults.vm", "enable_console"),
            enable_logging=self._inputs.enable_logging
            if self._inputs.enable_logging is not None
            else self._resolve_setting("defaults.vm", "enable_logging"),
            enable_metrics=self._inputs.enable_metrics
            if self._inputs.enable_metrics is not None
            else self._resolve_setting("defaults.vm", "enable_metrics"),
            skip_cleanup=self._inputs.skip_cleanup,
            skip_deblob=self._inputs.skip_deblob,
            requested_guest_mac=self._inputs.requested_guest_mac,
            requested_guest_ip=self._inputs.requested_guest_ip,
            ssh_keys=ssh_keys,
            provisioner=provisioner,
            disk_size_bytes=rootfs_disk_size_bytes,
            disk_size_mib=rootfs_disk_size_mib,
            nocloud_net_port=self._inputs.nocloud_net_port,
            custom_user_data_path=self._inputs.custom_user_data,
            skip_ci_network_config=self._inputs.skip_ci_network_config,
            network_prefix_len=network_prefix_len,
            network_netmask=str(network_netmask),
            keep_cloud_init_iso=self._inputs.keep_cloud_init_iso
            if ci_mode_result.mode == CloudInitMode.ISO
            else False,
            cloud_init_iso_path=self._inputs.cloud_init_iso_path
            if ci_mode_result.mode == CloudInitMode.ISO
            else None,
            boot_args=self._inputs.boot_args
            if self._inputs.boot_args is not None
            else f"{self._resolve_setting('defaults.vm', 'boot_args')} root=UUID={image.fs_uuid}",
            lsm_flags=self._inputs.lsm_flags
            if self._inputs.lsm_flags is not None
            else self._resolve_setting("defaults.vm", "lsm_flags"),
            extra_drives=extra_drives,
            volumes=volumes,
            log_level=str(
                self._resolve_setting("defaults.firecracker", "log_level")
            ),
            log_filename=str(
                self._resolve_setting("defaults.firecracker", "log_filename")
            ),
            serial_output_filename=str(
                self._resolve_setting(
                    "defaults.firecracker", "serial_output_filename"
                )
            ),
            metrics_filename=str(
                self._resolve_setting(
                    "defaults.firecracker", "metrics_filename"
                )
            ),
            api_socket_filename=str(
                self._resolve_setting(
                    "defaults.firecracker", "api_socket_filename"
                )
            ),
            pid_filename=str(
                self._resolve_setting("defaults.firecracker", "pid_filename")
            ),
            config_filename=str(
                self._resolve_setting("defaults.firecracker", "config_filename")
            ),
            console_socket_filename=str(
                self._resolve_setting(
                    "defaults.firecracker", "console_socket_filename"
                )
            ),
            console_pid_filename=str(
                self._resolve_setting(
                    "defaults.firecracker", "console_pid_filename"
                )
            ),
            cloud_init_iso_name=str(
                self._resolve_setting("defaults.cloudinit", "iso_name")
            ),
            nocloud_port_range_start=int(
                self._resolve_setting(
                    "defaults.cloudinit", "nocloud_port_range_start"
                )
            ),
            nocloud_port_range_end=int(
                self._resolve_setting(
                    "defaults.cloudinit", "nocloud_port_range_end"
                )
            ),
            nocloud_max_port_retries=int(
                self._resolve_setting(
                    "defaults.cloudinit", "nocloud_max_port_retries"
                )
            ),
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved dependencies."""

        if self._result is None:
            raise VMCreateError(
                "Failed to resolve necessary dependencies to validate"
            )

        if self._result.requested_guest_mac is not None:
            NetworkValidator.validate_mac(self._result.requested_guest_mac)

        if self._result.requested_guest_ip is not None:
            NetworkValidator.validate_ipv4_address(
                self._result.requested_guest_ip,
                field_name="Guest IP",
                require_private=True,
                subnet=self._result.network.subnet,
                gateway=self._result.network.ipv4_gateway,
            )

        if not (
            CONST_VM_VCPU_MIN <= self._result.vcpu_count <= CONST_VM_VCPU_MAX
        ):
            raise VMCreateError(
                f"Invalid vcpus={self._result.vcpu_count}: must be between {CONST_VM_VCPU_MIN} and {CONST_VM_VCPU_MAX}"
            )
        if not (
            CONST_VM_MEM_MIN_MIB
            <= self._result.mem_size_mib
            <= CONST_VM_MEM_MAX_MIB
        ):
            raise VMCreateError(
                f"Invalid mem_size_mib={self._result.mem_size_mib}: must be between {CONST_VM_MEM_MIN_MIB} and {CONST_VM_MEM_MAX_MIB}"
            )

        if not self._result.kernel.resolved_path.exists():
            raise VMCreateError(
                f"Kernel not found: {self._result.kernel.resolved_path}"
            )

        fc_bin_path = Path(self._result.binary.path)
        if not fc_bin_path.exists() or not os.access(fc_bin_path, os.X_OK):
            raise VMCreateError(f"Firecracker binary not found: {fc_bin_path}")

        if (
            self._result.custom_user_data_path is not None
            and not self._result.custom_user_data_path.exists()
        ):
            raise VMCreateError(
                f"User-data file not found: {self._result.custom_user_data_path}"
            )

        if (
            self._result.image is None
            or self._result.image.minimum_rootfs_size_mib is None
        ):
            raise VMCreateError(
                f"Image {self._inputs.image} is missing"
                f" minimum_rootfs_size_mib. "
                f"This image was created with an older version. "
                f"Re-import the image: mvm image pull <slug> --force"
            )

        if self._result.disk_size_bytes is not None:
            min_required_bytes = (
                self._result.image.minimum_rootfs_size_mib
                * CONST_MEBIBYTE_BYTES
            )
            if self._result.disk_size_bytes < min_required_bytes:
                raise VMCreateError(
                    f"Requested disk size is smaller than "
                    f"minimum required ({self._result.image.minimum_rootfs_size_mib} MiB). "
                    f"Use a larger size or choose a different image."
                )

        if self._result.boot_args is not None:
            for component in self._result.boot_args.split():
                VMValidator.validate_boot_arg_component(component, "boot_args")

        if self._result.lsm_flags is not None:
            VMValidator.validate_boot_arg_component(
                self._result.lsm_flags, "lsm_flags"
            )

        # Batch validation
        count = self._inputs.count if self._inputs.count is not None else 1
        if count < 1:
            raise VMCreateError("--count must be at least 1")

        if count > 1:
            if self._inputs.requested_guest_ip is not None:
                raise VMCreateError("Cannot specify --ip with --count > 1")
            if self._inputs.requested_guest_mac is not None:
                raise VMCreateError("Cannot specify --mac with --count > 1")

            # Check subnet capacity
            from mvmctl.core.network._repository import LeaseRepository

            lease_repo = LeaseRepository(self._db)
            available = lease_repo.count_available(self._result.network.id)
            if count > available:
                raise VMCreateError(
                    f"Subnet has only {available} IPs available, "
                    f"but {count} VMs requested"
                )

            # Check global VM limit
            current = self._vm_repo.count()
            max_vms = int(self._resolve_setting("settings.vm", "max_vms"))
            if current + count > max_vms:
                raise VMCreateError(
                    f"Creating {count} VMs would exceed the limit "
                    f"({current}/{max_vms}). Remove existing VMs first."
                )

    def _resolve_image(self) -> ImageItem:
        """Resolve image to path, ID, fs_uuid, and fs_type."""

        image = (
            self._image_resolver.get_default()
            if self._inputs.image is None
            else self._image_resolver.resolve(self._inputs.image)
        )
        if image is None:
            raise ImageNotFoundError(
                "No image specified and no default image set. "
                "Use 'mvm image pull <name>' then 'mvm image default <name>', "
                "or pass --image."
            )

        return image

    def _resolve_kernel(self) -> KernelItem:
        """Resolve kernel to path and ID."""

        kernel = (
            self._kernel_resolver.get_default()
            if self._inputs.kernel_id is None
            else self._kernel_resolver.resolve(self._inputs.kernel_id)
        )
        if kernel is None:
            raise KernelNotFoundError(
                "No kernel specified and no default kernel set. "
                "Use 'mvm kernel pull --type <firecracker|official>' then 'mvm kernel default <id>', "
                "or pass --kernel."
            )

        return kernel

    def _resolve_network(self) -> NetworkItem:
        """Resolve network to name and ID."""

        network = (
            self._network_resolver.get_default()
            if self._inputs.network_name is None
            else self._network_resolver.resolve(self._inputs.network_name)
        )

        if network is None:
            raise NetworkNotFoundError(
                "No network specified and no default network set. "
                "Use 'mvm network create' then 'mvm network default <id>', "
                "or pass --network."
            )

        return network

    def _resolve_binary(self) -> BinaryItem:
        """Resolve firecracker binary to path and ID.

        Resolution order:
        1. ``binary_id`` (from DB, e.g. ``mvm bin default``).
        2. ``firecracker_bin`` (raw filesystem path, e.g. ``--firecracker-bin``).
        3. Default binary from ``BinaryService.get_default_firecracker()``.
        """

        fc_binary: BinaryItem | None

        if self._inputs.binary_id is not None:
            fc_binary = self._binary_resolver.resolve(self._inputs.binary_id)

        elif self._inputs.firecracker_bin is not None:
            bin_path = Path(self._inputs.firecracker_bin)
            if not bin_path.exists() or not os.access(bin_path, os.X_OK):
                raise BinaryNotFoundError(
                    f"Firecracker binary not found at {bin_path}. "
                    "Use 'mvm bin pull <version>' or provide a valid path."
                )

            # Extract version from filename.
            # firecracker-v1.15.1  ->  "1.15.1"
            # firecracker-dev-abc  ->  "dev-abc"
            # fallback             ->  "custom-{path_hash}"
            stem = bin_path.name
            if stem.startswith("firecracker-v"):
                version = stem[len("firecracker-v") :]
            elif stem.startswith("firecracker-"):
                version = stem[len("firecracker-") :]
            else:
                from hashlib import sha256

                version = (
                    f"custom-{sha256(str(bin_path).encode()).hexdigest()[:12]}"
                )

            binary_item = BinaryService._create_binary_item(
                "firecracker", version, bin_path
            )

            # Upsert so the binary is visible in ``mvm bin ls``.
            binary_repo = BinaryRepository(self._db)
            existing = binary_repo.get(binary_item.id)
            if existing is None:
                binary_repo.upsert(binary_item)

            fc_binary = binary_item

        else:
            binary_service = BinaryService(BinaryRepository(self._db))
            fc_binary = binary_service.get_default_firecracker()

        if fc_binary is None:
            raise BinaryNotFoundError(
                "No binary specified and no default binary set. "
                "Use 'mvm bin pull <version>' then 'mvm bin default <id>', "
                "or pass --firecracker-bin."
            )

        return fc_binary

    def _resolve_ssh_keys(self) -> list[SSHKeyItem]:
        """Resolve SSH keys to key items (carries both IDs and pubkey paths)."""

        ssh_keys = (
            self._key_resolver.get_defaults()
            if len(self._inputs.ssh_keys) == 0
            else self._key_resolver.resolve_many(self._inputs.ssh_keys).items
        )
        return ssh_keys

    def _resolve_volumes(self) -> list[VolumeItem]:
        """Resolve volume names to VolumeItems and DriveConfigs.

        Returns:
            Tuple of (resolved VolumeItems, drive configs for Firecracker).

        Raises:
            VolumeError: If any volume cannot be resolved or is unavailable.

        """
        if not self._inputs.volumes:
            return []

        result = self._volume_resolver.resolve_many(self._inputs.volumes)
        if result.errors and not result.items:
            from mvmctl.exceptions import VolumeError

            raise VolumeError(result.errors[0])

        return result.items

    def _resolve_cloud_init_mode(self) -> CloudInitModeResolved:

        # Off is default cloud-init mode — most stable across all image types.
        # SSH key injection happens directly via the provisioner during image
        # preparation regardless of cloud-init mode, so Off is sufficient.
        mode = CloudInitModeResolved(mode=CloudInitMode.OFF, iso_path=None)

        if self._inputs.cloud_init_mode is None:
            return mode

        mode_lower = self._inputs.cloud_init_mode.lower()
        valid_modes = ["inject", "iso", "off", "net"]
        if mode_lower not in valid_modes:
            raise CloudInitModeError(
                f"Invalid --cloud-init-mode '{self._inputs.cloud_init_mode}'. Valid modes: {', '.join(valid_modes)}"
            )

        if mode_lower == "iso":
            if self._inputs.cloud_init_iso_path is not None:
                iso_path = Path(self._inputs.cloud_init_iso_path)
                if not iso_path.exists():
                    raise CloudInitModeError(
                        f"Cloud-init ISO not found: {iso_path}"
                    )
                mode = CloudInitModeResolved(
                    mode=CloudInitMode.ISO, iso_path=iso_path
                )
            else:
                # Default: ISO will be created during provisioning
                mode = CloudInitModeResolved(
                    mode=CloudInitMode.ISO, iso_path=None
                )
        elif mode_lower == "net":
            mode = CloudInitModeResolved(mode=CloudInitMode.NET, iso_path=None)
        elif mode_lower == "inject":
            mode = CloudInitModeResolved(
                mode=CloudInitMode.INJECT, iso_path=None
            )
        else:  # "off"
            mode = CloudInitModeResolved(mode=CloudInitMode.OFF, iso_path=None)

        return mode

    def _resolve_provisioner(self) -> ProvisionerType:
        """Resolve which provisioner to use.

        Checks the loop-mount binary first, then falls back to guestfs
        if enabled in settings. Raises ``VMCreateError`` if neither is
        available.
        """
        from mvmctl.core._shared._loopmount import LoopMountManager

        guestfs_enabled = bool(
            self._resolve_setting("settings", "guestfs_enabled")
        )
        if guestfs_enabled:
            return ProvisionerType.GUESTFS
        elif LoopMountManager.is_binary_available():
            return ProvisionerType.LOOP_MOUNT

        raise VMCreateError(
            "No provisioner available: loop-mount binary not found and "
            "libguestfs is not enabled. "
            "Run 'mvm init' to set up service binaries or enable libguestfs."
        )
