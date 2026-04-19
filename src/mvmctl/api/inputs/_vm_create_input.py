"""VM creation resolver - coordinates _internal resolvers for VM-specific resolution."""

from __future__ import annotations

import ipaddress
import logging
import os
from dataclasses import dataclass, field
from pathlib import Path

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
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.key._service import KeyService
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.core.vm._firecracker import DriveConfig
from mvmctl.exceptions import (
    BinaryNotFoundError,
    CloudInitModeError,
    ImageNotFoundError,
    KernelNotFoundError,
    NetworkNotFoundError,
    VMCreateError,
)
from mvmctl.models.binary import BinaryItem
from mvmctl.models.cloudinit import CloudInitMode
from mvmctl.models.image import ImageItem
from mvmctl.models.kernel import KernelItem
from mvmctl.models.network import NetworkItem
from mvmctl.utils._network_validator import NetworkValidator
from mvmctl.utils._vm_validator import VMValidator
from mvmctl.utils.disk_size import parse_disk_size

logger = logging.getLogger(__name__)

__all__ = ["VMCreateInput", "VMCreateRequest", "ResolvedVMCreateInput"]


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
    kernel_id: str | None = None
    binary_id: str | None = None
    image_path: Path | None = None
    kernel_path: Path | None = None
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
    nocloud_net_port: int = 0
    skip_cleanup: bool = False


@dataclass(frozen=True)
class ResolvedVMCreateInput:
    """Immutable resolved inputs - output of VMCreateRequest."""

    name: str
    vm_id: str
    vm_dir: Path
    vcpu_count: int
    mem_size_mib: int
    user: str
    network: NetworkItem
    image: ImageItem
    kernel: KernelItem
    binary: BinaryItem
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
        self._network_resolver = NetworkResolver(NetworkRepository(self._db))
        self._binary_resolver = BinaryResolver(BinaryRepository(self._db))
        self._image_resolver = ImageResolver(ImageRepository(self._db))
        self._kernel_resolver = KernelResolver(KernelRepository(self._db))
        self._key_resolver = KeyResolver(KeyRepository(self._db))

    @property
    def result(self) -> ResolvedVMCreateInput | None:
        return self._result

    def resolve(self) -> ResolvedVMCreateInput:
        """Resolve all inputs to explicit values."""

        # Validate VM name early — before any DB or subprocess calls
        VMValidator.validate_name(self._inputs.name)

        image = self._resolve_image()
        kernel = self._resolve_kernel()
        network = self._resolve_network()
        fc_binary = self._resolve_binary()
        ssh_keys = self._resolve_ssh_keys()

        ipv4_net = ipaddress.IPv4Network(network.subnet, strict=False)
        network_prefix_len = ipv4_net.prefixlen
        network_netmask = ipv4_net.netmask

        rootfs_disk_size_bytes = (
            image.minimum_rootfs_size_mib * CONST_MEBIBYTE_BYTES
        )
        if self._inputs.disk_size is not None:
            rootfs_disk_size_bytes = (
                parse_disk_size(self._inputs.disk_size) * CONST_MEBIBYTE_BYTES
            )

        rootfs_disk_size_mib = (
            image.minimum_rootfs_size_mib // CONST_MEBIBYTE_BYTES
        )

        ci_mode_result = self._resolve_cloud_init_mode()

        self._result = ResolvedVMCreateInput(
            name=self._inputs.name,
            vm_id=self._vm_id,
            vm_dir=self._vm_dir,
            vcpu_count=self._inputs.vcpu_count
            if self._inputs.vcpu_count != 0
            else DEFAULT_VM_VCPU_COUNT,
            mem_size_mib=self._inputs.mem_size_mib
            if self._inputs.mem_size_mib != 0
            else DEFAULT_VM_MEM_MIB,
            user=self._inputs.user
            if self._inputs.user
            else DEFAULT_VM_SSH_USER,
            network=network,
            image=image,
            kernel=kernel,
            binary=fc_binary,
            cloud_init_mode=ci_mode_result.mode,
            enable_pci=self._inputs.enable_pci
            if self._inputs.enable_pci is not None
            else DEFAULT_VM_ENABLE_PCI,
            enable_console=self._inputs.enable_console
            if self._inputs.enable_console is not None
            else DEFAULT_VM_ENABLE_CONSOLE,
            enable_logging=self._inputs.enable_logging
            if self._inputs.enable_logging is not None
            else DEFAULT_VM_ENABLE_LOGGING,
            enable_metrics=self._inputs.enable_metrics
            if self._inputs.enable_metrics is not None
            else DEFAULT_VM_ENABLE_METRICS,
            skip_cleanup=self._inputs.skip_cleanup,
            requested_guest_mac=self._inputs.requested_guest_mac,
            requested_guest_ip=self._inputs.requested_guest_ip,
            ssh_keys=ssh_keys,
            disk_size_bytes=rootfs_disk_size_bytes,
            disk_size_mib=rootfs_disk_size_mib,
            nocloud_net_port=self._inputs.nocloud_net_port
            if ci_mode_result.mode == CloudInitMode.NET
            else None,
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
            else f"{DEFAULT_VM_BOOT_ARGS} root=UUID={image.fs_uuid}",
            lsm_flags=self._inputs.lsm_flags
            if self._inputs.lsm_flags is not None
            else DEFAULT_VM_LSM_FLAGS,
            extra_drives=[],
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
                f"Invalid mem_size_mib={self._result.mem_size_mib}: must be between 128 and 65536"
            )

        if not Path(self._result.kernel.path).exists():
            raise VMCreateError(f"Kernel not found: {self._result.kernel.path}")

        fc_bin_path = Path(self._result.binary.path)
        if (fc_bin_path.is_absolute() or "/" in self._result.binary.path) and (
            not fc_bin_path.exists() or not os.access(fc_bin_path, os.X_OK)
        ):
            raise VMCreateError(
                f"Firecracker binary not found: {self._result.binary.path}"
            )

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
                f"Image {input.image} is missing minimum_rootfs_size_mib. "
                f"This image was created with an older version. "
                f"Re-import the image: mvm image fetch <slug> --force"
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

    def _resolve_image(self) -> ImageItem:
        """Resolve image to path, ID, fs_uuid, and fs_type."""

        if self._inputs.image_path is not None:
            # TODO: need to identify fs uuid since this is non-imported image
            # maybe enforce importing?
            pass

        image = (
            self._image_resolver.get_default()
            if self._inputs.image is None
            else self._image_resolver.resolve(self._inputs.image)
        )
        if image is None:
            raise ImageNotFoundError(
                "No image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', "
                "or pass --image."
            )

        return image

    def _resolve_kernel(self) -> KernelItem:
        """Resolve kernel to path and ID."""

        if self._inputs.kernel_path is not None:
            # TODO: kernel is fine to be passed, but maybe enforce importing?
            pass

        kernel = (
            self._kernel_resolver.get_default()
            if self._inputs.kernel_id is None
            else self._kernel_resolver.resolve(self._inputs.kernel_id)
        )
        if kernel is None:
            raise KernelNotFoundError(
                "No kernel specified and no default kernel set. "
                "Use 'mvm kernel fetch --type <firecracker|official>' then 'mvm kernel set-default <id>', "
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
                "Use 'mvm network create' then 'mvm network set-default <id>', "
                "or pass --network."
            )

        return network

    def _resolve_binary(self) -> BinaryItem:
        """Resolve firecracker binary to path and ID."""

        fc_binary = (
            self._binary_resolver.get_default("firecracker")
            if self._inputs.binary_id is None
            else self._binary_resolver.resolve(self._inputs.binary_id)
        )

        if fc_binary is None:
            raise BinaryNotFoundError(
                "No binary specified and no default binary set. "
                "Use 'mvm bin fetch <version>' then 'mvm bin set-default <id>', "
                "or pass --firecracker-bin."
            )

        return fc_binary

    def _resolve_ssh_keys(self) -> list[str]:
        """Resolve SSH keys to public key content"""

        ssh_keys = (
            self._key_resolver.get_defaults()
            if len(self._inputs.ssh_keys) == 0
            else self._key_resolver.resolve_many(self._inputs.ssh_keys).items
        )

        return KeyService.get_pubkeys(ssh_keys, self._db)

    def _resolve_cloud_init_mode(self) -> CloudInitModeResolved:

        # Inject is default cloud-init mode!
        mode = CloudInitModeResolved(mode=CloudInitMode.INJECT, iso_path=None)

        if self._inputs.cloud_init_mode is None:
            return mode

        mode_lower = self._inputs.cloud_init_mode.lower()
        valid_modes = ["inject", "iso", "off", "net"]
        if mode_lower not in valid_modes:
            raise CloudInitModeError(
                f"Invalid --cloud-init-mode '{self._inputs.cloud_init_mode}'. Valid modes: {', '.join(valid_modes)}"
            )

        if mode_lower == "iso":
            iso_path = self._vm_dir / "cloud-init.iso"

            if self._inputs.cloud_init_iso_path is not None:
                iso_path = Path(self._inputs.cloud_init_iso_path)

            if not iso_path.exists():
                raise CloudInitModeError(
                    f"Cloud-init ISO not found: {iso_path}"
                )

            mode = CloudInitModeResolved(
                mode=CloudInitMode.ISO, iso_path=iso_path
            )
        elif mode_lower == "net":
            mode = CloudInitModeResolved(mode=CloudInitMode.NET, iso_path=None)
        else:
            mode = CloudInitModeResolved(mode=CloudInitMode.OFF, iso_path=None)

        return mode
