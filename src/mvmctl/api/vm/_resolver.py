"""VM creation resolver - coordinates _internal resolvers for VM-specific resolution."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.api._internal._resolvers import (
    BinaryResolver,
    ImageResolver,
    NetworkResolver,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import (
    BinaryNotFoundError,
    ImageNotFoundError,
    KernelNotFoundError,
    NetworkNotFoundError,
)
from src.mvmctl.api._internal._resolvers._kernel_resolver import KernelResolver
from src.mvmctl.db.models import Binary, Image, Kernel, Network
from src.mvmctl.utils.validation import validate_mac

if TYPE_CHECKING:
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)


class VMInputResolver:
    """Resolve all DB-backed defaults using a single DB instance."""

    _result: VMResolvedDependencies | None = None

    def __init__(self, db: MVMDatabase | None = None) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._db = db if db is not None else MVMDatabase()
        self._network_resolver = NetworkResolver(self._db)
        self._binary_resolver = BinaryResolver(self._db)
        self._image_resolver = ImageResolver(self._db)
        self._kernel_resolver = KernelResolver(self._db)

    def get_result(self) -> VMResolvedDependencies | None:
        return self._result

    def resolve(self, input: VMCreateInput, vm_id: str) -> VMResolvedDependencies:
        """Resolve all inputs to explicit values."""

        image = self._resolve_image(input)
        kernel = self._resolve_kernel(input)
        network = self._resolve_network(input)
        fc_binary = self._resolve_binary(input)
        kernel_args = self._build_kernel_args(input, image.fs_uuid)

        self._result = VMResolvedDependencies(
            name=input.name,
            vm_id=vm_id,
            vcpus=input.vcpus,
            mem=input.mem,
            user=input.user,
            network_name=network.name,
            network_id=network.id,
            image_path=Path(image.path),
            kernel_path=Path(kernel.path),
            firecracker_bin=fc_binary.path,
            image_id=image.id,
            kernel_id=kernel.id,
            binary_id=fc_binary.id,
            kernel_args=kernel_args,
            cloud_init_mode=input.cloud_init_mode,
            enable_api_socket=input.enable_api_socket,
            enable_pci=input.enable_pci,
            enable_console=input.enable_console,
            enable_logging=input.enable_logging,
            enable_metrics=input.enable_metrics,
            lsm_flags=input.lsm_flags,
            keep_cloud_init_iso=input.keep_cloud_init_iso,
            nocloud_net_port=input.nocloud_net_port,
            skip_cleanup=input.skip_cleanup,
            image_fs_uuid=image.fs_uuid,
            image_fs_type=image.fs_type,
            mac=input.mac,
            ip=input.ip,
            ssh_key=input.ssh_key,
            user_data=input.user_data,
            disk_size=input.disk_size,
            cloud_init_iso_path=input.cloud_init_iso_path,
        )

        return self._result

    def validate(self) -> bool:
        if self._result is None:
            return False

        return True

    def _resolve_image(self, input: VMCreateInput) -> Image:
        """Resolve image to path, ID, fs_uuid, and fs_type."""

        if input.image_path is not None:
            # TODO: need to identify fs uuid since this is non-imported image
            # maybe enforce importing?
            pass

        image = (
            self._db.get_default_image()
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
            self._db.get_default_kernel()
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

        network = None

        if input.network_name is None:
            network = self._db.get_default_network()
        else:
            network = self._network_resolver.resolve(input.network_name)

        if network is None:
            raise NetworkNotFoundError(
                "No network specified and no default network set. "
                "Use 'mvm network create' then 'mvm network set-default <id>', "
                "or pass --network."
            )

        return network

    def _resolve_binary(self, input: VMCreateInput) -> Binary:
        """Resolve firecracker binary to path and ID."""
        fc_binary = None

        if input.binary_id is None:
            fc_binary = self._db.get_default_binary("firecracker")
        else:
            fc_binary = self._binary_resolver.resolve(input.binary_id)

        if fc_binary is None:
            raise BinaryNotFoundError(
                "No binary specified and no default binary set. "
                "Use 'mvm bin fetch <version>' then 'mvm bin set-default <id>', "
                "or pass --firecracker-bin."
            )

        return fc_binary

    def _build_kernel_args(self, input: VMCreateInput, root_uuid: str | None) -> str:
        """Build kernel boot arguments."""
        from mvmctl.constants import DEFAULT_BOOT_CONSOLE, DEFAULT_BOOT_PANIC, DEFAULT_BOOT_REBOOT

        args = f"{DEFAULT_BOOT_CONSOLE} {DEFAULT_BOOT_REBOOT} {DEFAULT_BOOT_PANIC}"

        if root_uuid:
            args += f" root=UUID={root_uuid}"

        return args


@dataclass(frozen=True)
class VMResolvedDependencies:
    """Immutable resolved inputs - output of VMInputResolver."""

    name: str
    vm_id: str
    vcpus: int
    mem: int
    user: str
    network_name: str
    network_id: str
    image_path: Path
    kernel_path: Path
    firecracker_bin: str
    image_id: str
    kernel_id: str
    binary_id: str
    kernel_args: str
    cloud_init_mode: Any
    enable_api_socket: bool
    enable_pci: bool
    enable_console: bool
    enable_logging: bool
    enable_metrics: bool
    lsm_flags: str
    keep_cloud_init_iso: bool
    nocloud_net_port: int
    skip_cleanup: bool
    image_fs_uuid: str | None = None
    image_fs_type: str | None = None
    mac: str | None = None
    ip: str | None = None
    ssh_key: str | None = None
    user_data: Path | None = None
    disk_size: str | None = None
    cloud_init_iso_path: Path | None = None


__all__ = ["VMInputResolver", "VMResolvedDependencies"]
