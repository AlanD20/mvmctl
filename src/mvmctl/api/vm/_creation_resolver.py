"""VM creation resolver - coordinates _internal resolvers for VM-specific resolution."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.api._internal._resolvers._binary_resolver import BinaryResolver
from mvmctl.api._internal._resolvers._image_resolver import resolve_image_hash
from mvmctl.api._internal._resolvers._network_resolver import NetworkResolver
from mvmctl.constants import DEFAULT_NETWORK_NAME
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import AssetNotFoundError, VMCreateError

if TYPE_CHECKING:
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)


class VMCreationResolver:
    """Resolve all DB-backed defaults using a single DB instance."""

    def __init__(self) -> None:
        self._db = MVMDatabase()
        self._network_resolver = NetworkResolver()
        self._binary_resolver = BinaryResolver()

    def resolve(self, input: VMCreateInput) -> ResolvedVMInputs:
        """Resolve all inputs to explicit values."""
        name = input.name
        vcpus = input.vcpus
        mem = input.mem
        user = input.user

        image_path, image_id, image_fs_uuid, image_fs_type = self._resolve_image(input)
        kernel_path, kernel_id = self._resolve_kernel(input)
        network_name, network_id = self._resolve_network(input)
        binary_path, binary_id = self._resolve_binary(input)
        image_hash = resolve_image_hash(image_path, input.image_hash)
        kernel_args = self._build_kernel_args(input, image_fs_uuid)

        return ResolvedVMInputs(
            name=name,
            vm_id="",
            vcpus=vcpus,
            mem=mem,
            user=user,
            network_name=network_name,
            network_id=network_id,
            image_path=image_path,
            kernel_path=kernel_path,
            firecracker_bin=binary_path,
            image_id=image_id,
            kernel_id=kernel_id,
            binary_id=binary_id,
            kernel_args=kernel_args,
            cloud_init_mode=input.cloud_init_mode,
            image_fs_uuid=image_fs_uuid,
            image_fs_type=image_fs_type,
            image_hash=image_hash,
            mac=input.mac,
            ip=input.ip,
            ssh_key=input.ssh_key,
            user_data=input.user_data,
            disk_size=input.disk_size,
            enable_api_socket=input.enable_api_socket,
            enable_pci=input.enable_pci,
            enable_console=input.enable_console,
            enable_logging=input.enable_logging,
            enable_metrics=input.enable_metrics,
            lsm_flags=input.lsm_flags,
            cloud_init_iso_path=input.cloud_init_iso_path,
            keep_cloud_init_iso=input.keep_cloud_init_iso,
            nocloud_net_port=input.nocloud_net_port,
            skip_cleanup=input.skip_cleanup,
        )

    def _resolve_image(self, input: VMCreateInput) -> tuple[Path, str, str | None, str | None]:
        """Resolve image to path, ID, fs_uuid, and fs_type."""
        from mvmctl.api._internal._resolvers._image_resolver import resolve_image_multi_strategy
        from mvmctl.api.assets import resolve_image_fs_type, resolve_image_fs_uuid

        if input.image_path is not None:
            image_path = input.image_path
            fs_uuid = input.image_fs_uuid or (
                resolve_image_fs_uuid(input.image) if input.image else None
            )
            fs_type = input.image_fs_type or (
                resolve_image_fs_type(input.image) if input.image else None
            )
        else:
            image_name = input.image
            if image_name is None:
                default_image = self._db.get_default_image()
                if default_image is None:
                    raise AssetNotFoundError(
                        "No image specified and no default image set. "
                        "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', "
                        "or pass --image."
                    )
                image_name = default_image.os_slug

            image_path = resolve_image_multi_strategy(image_name)
            fs_uuid = input.image_fs_uuid or resolve_image_fs_uuid(image_name)
            fs_type = input.image_fs_type or resolve_image_fs_type(image_name)

        image_entry = None
        image_hash = resolve_image_hash(image_path, input.image_hash)
        if image_hash:
            image_entry = self._db.get_image(image_hash)
        elif input.image:
            image_entry = self._db.get_image_by_os_slug(input.image)

        if image_entry is None:
            image_id = image_hash or str(image_path)
        else:
            image_id = image_entry.id

        return image_path, image_id, fs_uuid, fs_type

    def _resolve_kernel(self, input: VMCreateInput) -> tuple[Path, str]:
        """Resolve kernel to path and ID."""
        from mvmctl.core.kernel import resolve_kernel_path
        from mvmctl.utils.fs import get_kernels_dir

        if input.kernel_path is not None:
            kernel_path = input.kernel_path
            kernel_entry = None
            if input.kernel:
                kernel_entry = self._db.get_kernel_by_name(input.kernel)
            if kernel_entry is None:
                kernel_entry = self._db.get_default_kernel()
            kernel_id = kernel_entry.id if kernel_entry else str(kernel_path)
        elif input.kernel:
            kernel_path = resolve_kernel_path(input.kernel)
            kernel_entry = self._db.get_kernel_by_name(input.kernel)
            kernel_id = kernel_entry.id if kernel_entry else str(kernel_path)
        else:
            default_kernel = self._db.get_default_kernel()
            if default_kernel is not None:
                kernel_path = get_kernels_dir() / default_kernel.path
                kernel_id = default_kernel.id
            else:
                import os

                env_kernel = os.environ.get("MVM_KERNEL")
                if env_kernel:
                    kernel_path = resolve_kernel_path(env_kernel)
                else:
                    from mvmctl.constants import DEFAULT_VM_KERNEL_FILENAME

                    kernel_path = get_kernels_dir() / DEFAULT_VM_KERNEL_FILENAME
                kernel_id = str(kernel_path)

        return kernel_path, kernel_id

    def _resolve_network(self, input: VMCreateInput) -> tuple[str, str]:
        """Resolve network to name and ID."""
        network_name = input.network_name

        if network_name is None:
            default_network = self._db.get_default_network()
            if default_network is None:
                network_name = DEFAULT_NETWORK_NAME
                db_net = self._db.get_network_by_name(network_name)
                network_id = db_net.id if db_net else ""
            else:
                network_name = default_network.name
                network_id = default_network.id
        else:
            db_net = self._db.get_network_by_name(network_name)
            network_id = db_net.id if db_net else ""

        return network_name, network_id

    def _resolve_binary(self, input: VMCreateInput) -> tuple[str, str]:
        """Resolve firecracker binary to path and ID."""
        binary_id = input.binary_id

        if binary_id is None:
            default_binary = self._db.get_default_binary("firecracker")
            if default_binary is None:
                raise VMCreateError(
                    "No firecracker binary specified and no default set. Run 'mvm bin fetch' first."
                )
            binary_path = default_binary.path
            binary_id = default_binary.id
        else:
            binary_entry = self._db.get_binary(binary_id)
            if binary_entry is None:
                raise VMCreateError(f"Binary not found: {binary_id}")
            binary_path = binary_entry.path

        return binary_path, binary_id

    def _build_kernel_args(self, input: VMCreateInput, root_uuid: str | None) -> str:
        """Build kernel boot arguments."""
        from mvmctl.constants import DEFAULT_BOOT_CONSOLE, DEFAULT_BOOT_PANIC, DEFAULT_BOOT_REBOOT

        args = f"{DEFAULT_BOOT_CONSOLE} {DEFAULT_BOOT_REBOOT} {DEFAULT_BOOT_PANIC}"

        if root_uuid:
            args += f" root=UUID={root_uuid}"

        return args


class ResolvedVMInputs:
    """Immutable resolved inputs - output of VMCreationResolver."""

    def __init__(
        self,
        name: str,
        vm_id: str,
        vcpus: int,
        mem: int,
        user: str,
        network_name: str,
        network_id: str,
        image_path: Path,
        kernel_path: Path,
        firecracker_bin: str,
        image_id: str,
        kernel_id: str,
        binary_id: str,
        kernel_args: str,
        cloud_init_mode: Any,
        image_fs_uuid: str | None = None,
        image_fs_type: str | None = None,
        image_hash: str | None = None,
        mac: str | None = None,
        ip: str | None = None,
        ssh_key: str | None = None,
        user_data: Path | None = None,
        disk_size: str | None = None,
        enable_api_socket: bool = False,
        enable_pci: bool = False,
        enable_console: bool = False,
        enable_logging: bool = False,
        enable_metrics: bool = False,
        lsm_flags: str = "",
        cloud_init_iso_path: Path | None = None,
        keep_cloud_init_iso: bool = False,
        nocloud_net_port: int = 0,
        skip_cleanup: bool = False,
    ):
        self.name = name
        self.vm_id = vm_id
        self.vcpus = vcpus
        self.mem = mem
        self.user = user
        self.network_name = network_name
        self.network_id = network_id
        self.image_path = image_path
        self.kernel_path = kernel_path
        self.firecracker_bin = firecracker_bin
        self.image_id = image_id
        self.kernel_id = kernel_id
        self.binary_id = binary_id
        self.kernel_args = kernel_args
        self.cloud_init_mode = cloud_init_mode
        self.image_fs_uuid = image_fs_uuid
        self.image_fs_type = image_fs_type
        self.image_hash = image_hash
        self.mac = mac
        self.ip = ip
        self.ssh_key = ssh_key
        self.user_data = user_data
        self.disk_size = disk_size
        self.enable_api_socket = enable_api_socket
        self.enable_pci = enable_pci
        self.enable_console = enable_console
        self.enable_logging = enable_logging
        self.enable_metrics = enable_metrics
        self.lsm_flags = lsm_flags
        self.cloud_init_iso_path = cloud_init_iso_path
        self.keep_cloud_init_iso = keep_cloud_init_iso
        self.nocloud_net_port = nocloud_net_port
        self.skip_cleanup = skip_cleanup


__all__ = ["VMCreationResolver", "ResolvedVMInputs"]
