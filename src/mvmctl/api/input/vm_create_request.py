"""VM creation resolver - coordinates _internal resolvers for VM-specific resolution."""

from __future__ import annotations

import ipaddress
import logging
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import (
    CONST_MEBIBYTE_BYTES,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_VCPU_MAX,
    CONST_VM_VCPU_MIN,
    DEFAULT_VM_BOOT_ARGS,
    DEFAULT_VM_ENABLE_CONSOLE,
    DEFAULT_VM_ENABLE_LOGGING,
    DEFAULT_VM_ENABLE_METRICS,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LSM_FLAGS,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.core._internal._db import Database
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.vm._firecracker import DriveConfig
from mvmctl.db.models import Binary, Image, Kernel, Network
from mvmctl.exceptions import (
    BinaryNotFoundError,
    CloudInitModeError,
    ImageNotFoundError,
    KernelNotFoundError,
    NetworkNotFoundError,
    VMBuilderError,
)
from mvmctl.models.cloud_init import CloudInitMode
from mvmctl.utils.disk_size import parse_disk_size
from mvmctl.utils.validation import validate_boot_arg_component, validate_mac
from src.mvmctl.core.binary._repository import BinaryRepository
from src.mvmctl.core.binary._resolver import BinaryResolver
from src.mvmctl.core.image._repository import ImageRepository
from src.mvmctl.core.image._resolver import ImageResolver
from src.mvmctl.core.kernel._repository import KernelRepository
from src.mvmctl.core.key._repository import KeyRepository
from src.mvmctl.core.key._service import KeyService
from src.mvmctl.core.network._repository import NetworkRepository
from src.mvmctl.core.network._resolver import NetworkResolver

if TYPE_CHECKING:
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)

__all__ = ["VMCreateRequest", "ResolvedVMCreateRequest"]


@dataclass(frozen=True)
class ResolvedVMCreateRequest:
    """Immutable resolved inputs - output of VMCreateRequest."""

    name: str
    vm_id: str
    vm_dir: Path
    vcpu_count: int
    mem_size_mib: int
    user: str
    network: Network
    image: Image
    kernel: Kernel
    binary: Binary
    network_prefix_len: int
    cloud_init_mode: CloudInitMode
    skip_ci_network_config: bool
    enable_pci: bool
    enable_console: bool
    enable_logging: bool
    enable_metrics: bool

    keep_cloud_init_iso: bool
    skip_cleanup: bool
    network_netmask: str
    disk_size_bytes: int
    disk_size_mib: int
    ssh_keys: list[str]

    lsm_flags: str

    requested_guest_ip: str | None
    requested_guest_mac: str | None
    nocloud_net_port: int | None = None
    custom_user_data_path: Path | None = None
    cloud_init_iso_path: Path | None = None

    boot_args: str | None = None
    extra_drives: list[DriveConfig] = field(default_factory=list)


@dataclass
class CloudInitModeResolved:
    mode: CloudInitMode
    iso_path: Path | None


class VMCreateRequest:
    """Resolve all DB-backed defaults using a single DB instance."""

    _result: ResolvedVMCreateRequest | None = None

    def __init__(self, db: Database | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._db = db if db is not None else Database()
        self._network_resolver = NetworkResolver(NetworkRepository(self._db))
        self._binary_resolver = BinaryResolver(BinaryRepository(self._db))
        self._image_resolver = ImageResolver(ImageRepository(self._db))
        self._kernel_resolver = KernelResolver(KernelRepository(self._db))
        self._key_resolver = KeyResolver(KeyRepository(self._db))

    @property
    def result(self) -> ResolvedVMCreateRequest | None:
        return self._result

    def resolve(self, input: VMCreateInput, vm_id: str, vm_dir: Path) -> ResolvedVMCreateRequest:
        """Resolve all inputs to explicit values."""

        image = self._resolve_image(input)
        kernel = self._resolve_kernel(input)
        network = self._resolve_network(input)
        fc_binary = self._resolve_binary(input)
        ssh_keys = self._resolve_ssh_keys(input)

        ipv4_net = ipaddress.IPv4Network(network.subnet, strict=False)
        network_prefix_len = ipv4_net.prefixlen
        network_netmask = ipv4_net.netmask

        rootfs_disk_size_bytes = image.minimum_rootfs_size_mib * CONST_MEBIBYTE_BYTES
        if input.disk_size is not None:
            rootfs_disk_size_bytes = parse_disk_size(input.disk_size) * CONST_MEBIBYTE_BYTES

        rootfs_disk_size_mib = image.minimum_rootfs_size_mib // CONST_MEBIBYTE_BYTES

        ci_mode_result = self._resolve_cloud_init_mode(input, vm_dir)

        self._result = ResolvedVMCreateRequest(
            name=input.name,
            vm_id=vm_id,
            vm_dir=vm_dir,
            vcpu_count=input.vcpu_count if input.vcpu_count != 0 else DEFAULT_VM_VCPU_COUNT,
            mem_size_mib=input.mem_size_mib if input.mem_size_mib != 0 else DEFAULT_VM_MEM_MIB,
            user=input.user if input.user else DEFAULT_VM_SSH_USER,
            network=network,
            image=image,
            kernel=kernel,
            binary=fc_binary,
            cloud_init_mode=ci_mode_result.mode,
            enable_pci=input.enable_pci if input.enable_pci is not None else DEFAULT_VM_ENABLE_PCI,
            enable_console=input.enable_console
            if input.enable_console is not None
            else DEFAULT_VM_ENABLE_CONSOLE,
            enable_logging=input.enable_logging
            if input.enable_logging is not None
            else DEFAULT_VM_ENABLE_LOGGING,
            enable_metrics=input.enable_metrics
            if input.enable_metrics is not None
            else DEFAULT_VM_ENABLE_METRICS,
            skip_cleanup=input.skip_cleanup,
            requested_guest_mac=input.requested_guest_mac,
            requested_guest_ip=input.requested_guest_ip,
            ssh_keys=ssh_keys,
            disk_size_bytes=rootfs_disk_size_bytes,
            disk_size_mib=rootfs_disk_size_mib,
            nocloud_net_port=input.nocloud_net_port
            if ci_mode_result.mode == CloudInitMode.NET
            else None,
            custom_user_data_path=input.custom_user_data,
            skip_ci_network_config=input.skip_ci_network_config,
            network_prefix_len=network_prefix_len,
            network_netmask=str(network_netmask),
            keep_cloud_init_iso=input.keep_cloud_init_iso
            if ci_mode_result.mode == CloudInitMode.ISO
            else False,
            cloud_init_iso_path=input.cloud_init_iso_path
            if ci_mode_result.mode == CloudInitMode.ISO
            else None,
            boot_args=input.boot_args
            if input.boot_args is not None
            else f"{DEFAULT_VM_BOOT_ARGS} root=UUID={image.fs_uuid}",
            lsm_flags=input.lsm_flags if input.lsm_flags is not None else DEFAULT_VM_LSM_FLAGS,
            extra_drives=[],
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved dependencies."""

        if self._result is None:
            raise VMBuilderError("Failed to resolve necessary dependencies to validate")

        if self._result.requested_guest_mac is not None:
            validate_mac(self._result.requested_guest_mac)

        if not (CONST_VM_VCPU_MIN <= self._result.vcpu_count <= CONST_VM_VCPU_MAX):
            raise VMBuilderError(
                f"Invalid vcpus={self._result.vcpu_count}: must be between {CONST_VM_VCPU_MIN} and {CONST_VM_VCPU_MAX}"
            )
        if not (CONST_VM_MEM_MIN_MIB <= self._result.mem_size_mib <= CONST_VM_MEM_MAX_MIB):
            raise VMBuilderError(
                f"Invalid mem_size_mib={self._result.mem_size_mib}: must be between 128 and 65536"
            )

        if not Path(self._result.kernel.path).exists():
            raise VMBuilderError(f"Kernel not found: {self._result.kernel.path}")

        fc_bin_path = Path(self._result.binary.path)
        if (fc_bin_path.is_absolute() or "/" in self._result.binary.path) and (
            not fc_bin_path.exists() or not os.access(fc_bin_path, os.X_OK)
        ):
            raise VMBuilderError(f"Firecracker binary not found: {self._result.binary.path}")

        if (
            self._result.custom_user_data_path is not None
            and not self._result.custom_user_data_path.exists()
        ):
            raise VMBuilderError(f"User-data file not found: {self._result.custom_user_data_path}")

        if self._result.image is None or self._result.image.minimum_rootfs_size_mib is None:
            raise VMBuilderError(
                f"Image {input.image} is missing minimum_rootfs_size_mib. "
                f"This image was created with an older version. "
                f"Re-import the image: mvm image fetch <slug> --force"
            )

        if self._result.disk_size_bytes is not None:
            min_required_bytes = self._result.image.minimum_rootfs_size_mib * CONST_MEBIBYTE_BYTES
            if self._result.disk_size_bytes < min_required_bytes:
                raise VMBuilderError(
                    f"Requested disk size is smaller than "
                    f"minimum required ({self._result.image.minimum_rootfs_size_mib} MiB). "
                    f"Use a larger size or choose a different image."
                )

        if self._result.boot_args is not None:
            for component in self._result.boot_args.split():
                validate_boot_arg_component(component, "boot_args")

        if self._result.lsm_flags is not None:
            validate_boot_arg_component(self._result.lsm_flags, "lsm_flags")

    def _resolve_image(self, input: VMCreateInput) -> Image:
        """Resolve image to path, ID, fs_uuid, and fs_type."""

        if input.image_path is not None:
            # TODO: need to identify fs uuid since this is non-imported image
            # maybe enforce importing?
            pass

        image = (
            self._image_resolver.get_default()
            if input.image is None
            else self._image_resolver.resolve(input.image)
        )
        if image is None:
            raise ImageNotFoundError(
                "No image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', "
                "or pass --image."
            )

        return image

    def _resolve_kernel(self, input: VMCreateInput) -> Kernel:
        """Resolve kernel to path and ID."""

        if input.kernel_path is not None:
            # TODO: kernel is fine to be passed, but maybe enforce importing?
            pass

        kernel = (
            self._kernel_resolver.get_default()
            if input.kernel is None
            else self._kernel_resolver.resolve(input.kernel)
        )
        if kernel is None:
            raise KernelNotFoundError(
                "No kernel specified and no default kernel set. "
                "Use 'mvm kernel fetch --type <firecracker|official>' then 'mvm kernel set-default <id>', "
                "or pass --kernel."
            )

        return kernel

    def _resolve_network(self, input: VMCreateInput) -> Network:
        """Resolve network to name and ID."""

        network = (
            self._network_resolver.get_default()
            if input.network_name is None
            else self._network_resolver.resolve(input.network_name)
        )

        if network is None:
            raise NetworkNotFoundError(
                "No network specified and no default network set. "
                "Use 'mvm network create' then 'mvm network set-default <id>', "
                "or pass --network."
            )

        return network

    def _resolve_binary(self, input: VMCreateInput) -> Binary:
        """Resolve firecracker binary to path and ID."""

        fc_binary = (
            self._binary_resolver.get_default("firecracker")
            if input.binary_id is None
            else self._binary_resolver.resolve(input.binary_id)
        )

        if fc_binary is None:
            raise BinaryNotFoundError(
                "No binary specified and no default binary set. "
                "Use 'mvm bin fetch <version>' then 'mvm bin set-default <id>', "
                "or pass --firecracker-bin."
            )

        return fc_binary

    def _resolve_ssh_keys(self, input: VMCreateInput) -> list[str]:
        """Resolve SSH keys to public key content"""

        ssh_keys = (
            self._key_resolver.get_defaults()
            if len(input.ssh_keys) == 0
            else self._key_resolver.resolve_many(input.ssh_keys).items
        )

        return KeyService.get_pubkeys(ssh_keys, self._db)

    def _resolve_cloud_init_mode(self, input: VMCreateInput, vm_dir: Path) -> CloudInitModeResolved:

        # Inject is default cloud-init mode!
        mode = CloudInitModeResolved(mode=CloudInitMode.INJECT, iso_path=None)

        if input.cloud_init_mode is None:
            return mode

        mode_lower = input.cloud_init_mode.lower()
        valid_modes = ["inject", "iso", "off", "net"]
        if mode_lower not in valid_modes:
            raise CloudInitModeError(
                f"Invalid --cloud-init-mode '{input.cloud_init_mode}'. Valid modes: {', '.join(valid_modes)}"
            )

        if mode_lower == "iso":
            iso_path = vm_dir / "cloud-init.iso"

            if input.cloud_init_iso_path is not None:
                iso_path = Path(input.cloud_init_iso_path)

            if not iso_path.exists():
                raise CloudInitModeError(f"Cloud-init ISO not found: {iso_path}")

            mode = CloudInitModeResolved(mode=CloudInitMode.ISO, iso_path=iso_path)
        elif mode_lower == "net":
            mode = CloudInitModeResolved(mode=CloudInitMode.NET, iso_path=None)
        else:
            mode = CloudInitModeResolved(mode=CloudInitMode.OFF, iso_path=None)

        return mode
