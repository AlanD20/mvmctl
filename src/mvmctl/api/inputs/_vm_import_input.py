"""
VM import resolution — Input → Request → ResolvedVMCreateInput.

Reads a portable VMExportConfig JSON file, resolves semantic references
(type, version, name) to actual DB records, and produces a
ResolvedVMCreateInput ready for VM creation.
"""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._vm_create_input import (
    ResolvedVMCreateInput,
    VMCreateInput,
    VMCreateRequest,
)
from mvmctl.api.inputs._vm_export_config import (
    VMExportBinaryConfig,
    VMExportConfig,
    VMExportImageConfig,
    VMExportKernelConfig,
    VMExportNetworkConfig,
)
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.exceptions import (
    BinaryNotFoundError,
    ImageNotFoundError,
    KernelNotFoundError,
    NetworkNotFoundError,
)
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.crypto import HashGenerator

logger = logging.getLogger(__name__)


@dataclass
class VMImportInput:
    """Raw import parameters from CLI."""

    config_path: Path
    name_override: str | None = None


class VMImportRequest:
    """
    Resolve VMImportInput to ResolvedVMCreateInput.

    1. Read VMExportConfig from JSON file
    2. Resolve semantic references to DB records
    3. Build VMCreateInput with resolved values
    4. Delegate to VMCreateRequest for full resolution
    """

    def __init__(self, inputs: VMImportInput, db: Database) -> None:
        self._inputs = inputs
        self._db = db

    def resolve(self) -> ResolvedVMCreateInput:
        """Resolve import config to fully resolved VM creation parameters."""
        export_config = VMExportConfig.from_json_file(self._inputs.config_path)

        # Resolve all assets from semantic references
        image_slug = self._resolve_image(export_config.image)
        kernel_id = self._resolve_kernel(export_config.kernel)
        binary_id = self._resolve_binary(export_config.binary)
        network_name = self._resolve_network(export_config.network)

        # Parse cpu_config from export JSON string (if present)
        cpu_config: dict[str, Any] | None = None
        if export_config.firecracker.cpu_config is not None:
            try:
                cpu_config = json.loads(export_config.firecracker.cpu_config)
            except json.JSONDecodeError:
                logger.warning(
                    "Failed to parse cpu_config from import file: %s",
                    export_config.firecracker.cpu_config,
                )

        # Build VMCreateInput with resolved values
        create_input = VMCreateInput(
            name=self._inputs.name_override or export_config.name,
            ssh_keys=[],
            vcpu_count=export_config.compute.vcpus,
            mem_size_mib=str(export_config.compute.mem),
            disk_size=export_config.image.disk_size,
            image=image_slug,
            kernel_id=kernel_id,
            binary_id=binary_id,
            network_name=network_name,
            requested_guest_ip=export_config.network.ip,
            requested_guest_mac=export_config.network.mac,
            pci_enabled=export_config.firecracker.pci_enabled,
            enable_console=export_config.boot.enable_console,
            lsm_flags=export_config.firecracker.lsm_flags,
            boot_args=export_config.boot.args,
            cloud_init_mode=export_config.cloud_init.mode,
            nocloud_net_port=export_config.cloud_init.nocloud_net_port,
            user=export_config.cloud_init.user,
            nested_virt=export_config.firecracker.nested_virt,
            cpu_config=cpu_config,
        )

        # Delegate to VMCreateRequest for full resolution
        vm_id = HashGenerator.vm(create_input.name, datetime.now().isoformat())
        vm_dir = CacheUtils.get_vm_dir(vm_id)

        return VMCreateRequest(
            vm_id=vm_id, vm_dir=vm_dir, inputs=create_input, db=self._db
        ).resolve()

    def _resolve_image(self, image_config: VMExportImageConfig) -> str | None:
        if not image_config.type:
            return None
        resolver = ImageResolver(ImageRepository(self._db))
        try:
            image = resolver.by_type(image_config.type)
            return image.type
        except ImageNotFoundError as exc:
            raise ImageNotFoundError(
                f"Image '{image_config.type}' not found. "
                f"Fetch it first: mvm image pull {image_config.type}"
            ) from exc

    def _resolve_kernel(
        self, kernel_config: VMExportKernelConfig
    ) -> str | None:
        if not kernel_config.version or not kernel_config.type:
            return None
        resolver = KernelResolver(KernelRepository(self._db))
        try:
            kernel = resolver.by_version_type(
                kernel_config.version, kernel_config.type
            )
            return kernel.id
        except KernelNotFoundError as exc:
            raise KernelNotFoundError(
                f"Kernel version={kernel_config.version!r}, "
                f"type={kernel_config.type!r} not found. "
                f"Fetch it first: mvm kernel pull --type {kernel_config.type}"
            ) from exc

    def _resolve_binary(
        self, binary_config: VMExportBinaryConfig
    ) -> str | None:
        if not binary_config.version:
            return None
        resolver = BinaryResolver(BinaryRepository(self._db))
        try:
            binary = resolver.by_name_version(
                binary_config.name, binary_config.version
            )
            return binary.id
        except BinaryNotFoundError as exc:
            raise BinaryNotFoundError(
                f"Binary {binary_config.name!r} "
                f"version={binary_config.version!r} not found. "
                f"Fetch it first: mvm bin pull {binary_config.version}"
            ) from exc

    def _resolve_network(
        self, network_config: VMExportNetworkConfig
    ) -> str | None:
        if not network_config.name:
            return None
        resolver = NetworkResolver(NetworkRepository(self._db))
        try:
            network = resolver.by_name(network_config.name)
            return network.name
        except NetworkNotFoundError as exc:
            raise NetworkNotFoundError(
                f"Network '{network_config.name}' not found. "
                f"Create it first: mvm network create {network_config.name}"
            ) from exc


__all__ = ["VMImportInput", "VMImportRequest"]
