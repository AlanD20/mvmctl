"""Input resolution layer for API boundary.

This module provides Input/Request/Config classes that represent
API boundary contracts — raw CLI arguments, resolved requests,
and portable export configurations.
"""

from __future__ import annotations

from mvmctl.api.inputs._image_input import (
    ImageFetchInput,
    ImageImportInput,
)
from mvmctl.api.inputs._kernel_input import KernelFetchInput
from mvmctl.api.inputs._key_input import KeyCreateInput
from mvmctl.api.inputs._network_create_input import (
    NetworkCreateInput,
    NetworkCreateRequest,
    ResolvedNetworkCreateRequest,
)
from mvmctl.api.inputs._network_input import (
    NetworkInput,
    NetworkRequest,
    ResolvedNetworkInput,
)
from mvmctl.api.inputs._vm_create_input import (
    CloudInitModeResolved,
    ResolvedVMCreateInput,
    VMCreateInput,
    VMCreateRequest,
)
from mvmctl.api.inputs._vm_export_config import (
    VMExportBinaryConfig,
    VMExportBootConfig,
    VMExportCloudInitConfig,
    VMExportComputeConfig,
    VMExportConfig,
    VMExportFirecrackerConfig,
    VMExportImageConfig,
    VMExportKernelConfig,
    VMExportNetworkConfig,
)
from mvmctl.api.inputs._vm_input import (
    ResolvedVMInput,
    VMInput,
    VMRequest,
)

__all__ = [
    "CloudInitModeResolved",
    "ImageFetchInput",
    "ImageImportInput",
    "KernelFetchInput",
    "KeyCreateInput",
    "NetworkCreateInput",
    "NetworkCreateRequest",
    "NetworkInput",
    "NetworkRequest",
    "ResolvedNetworkCreateRequest",
    "ResolvedNetworkInput",
    "ResolvedVMCreateInput",
    "ResolvedVMInput",
    "VMCreateInput",
    "VMCreateRequest",
    "VMExportBinaryConfig",
    "VMExportBootConfig",
    "VMExportCloudInitConfig",
    "VMExportComputeConfig",
    "VMExportConfig",
    "VMExportFirecrackerConfig",
    "VMExportImageConfig",
    "VMExportKernelConfig",
    "VMExportNetworkConfig",
    "VMInput",
    "VMRequest",
]
