"""Data models for MicroVM Manager."""

from mvmctl.models.binary import BinaryItem
from mvmctl.models.cache import PruneAllResult
from mvmctl.models.cloud_init import (
    CloudInitConfig,
    CloudInitMode,
    CloudInitStatus,
    CloudInitWriteConfig,
)
from mvmctl.models.firecracker import InstanceDescription, InstanceInfo
from mvmctl.models.image import ImageFetchInput, ImageItem, ImageSpec
from mvmctl.models.kernel import KernelFetchInput, KernelItem, KernelSpec
from mvmctl.models.key import KeyCreateInput
from mvmctl.models.network import (
    LeaseEntry,
    NetworkConfig,
    NetworkEntry,
    NetworkInspectInfo,
    NetworkItem,
    NetworkLease,
)
from mvmctl.models.vm import (
    ConsoleInfo,
    ConsoleState,
    VMConfig,
    VMCreateInput,
    VMInspectInfo,
    VMInstance,
    VMStatus,
)
from mvmctl.models.vm_config_file import (
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

__all__ = [
    "BinaryItem",
    "CloudInitConfig",
    "CloudInitMode",
    "CloudInitStatus",
    "CloudInitWriteConfig",
    "ConsoleInfo",
    "ConsoleState",
    "ImageFetchInput",
    "ImageItem",
    "ImageSpec",
    "InstanceDescription",
    "InstanceInfo",
    "KeyCreateInput",
    "KernelFetchInput",
    "KernelItem",
    "KernelSpec",
    "LeaseEntry",
    "NetworkConfig",
    "NetworkEntry",
    "NetworkInspectInfo",
    "NetworkItem",
    "NetworkLease",
    "PruneAllResult",
    "VMConfig",
    "VMCreateInput",
    "VMInstance",
    "VMInspectInfo",
    "VMStatus",
    "VMExportConfig",
    "VMExportComputeConfig",
    "VMExportImageConfig",
    "VMExportKernelConfig",
    "VMExportBinaryConfig",
    "VMExportNetworkConfig",
    "VMExportBootConfig",
    "VMExportFirecrackerConfig",
    "VMExportCloudInitConfig",
]
