"""Data models for MicroVM Manager."""

from mvmctl.models.cache import PruneAllResult
from mvmctl.models.cloud_init import (
    CloudInitConfig,
    CloudInitMode,
    CloudInitStatus,
    CloudInitWriteConfig,
)
from mvmctl.models.firecracker import InstanceDescription, InstanceInfo
from mvmctl.models.image import ImageSpec
from mvmctl.models.key import KeyCreateInput
from mvmctl.models.kernel import KernelFetchInput, KernelSpec
from mvmctl.models.network import LeaseEntry, NetworkEntry, NetworkInspectInfo
from mvmctl.models.vm import (
    ConsoleInfo,
    ConsoleState,
    VMConfig,
    VMInstance,
    VMInspectInfo,
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
    "CloudInitConfig",
    "CloudInitMode",
    "CloudInitStatus",
    "CloudInitWriteConfig",
    "ConsoleInfo",
    "ConsoleState",
    "ImageSpec",
    "InstanceDescription",
    "InstanceInfo",
    "KeyCreateInput",
    "KernelFetchInput",
    "KernelSpec",
    "LeaseEntry",
    "NetworkEntry",
    "NetworkInspectInfo",
    "PruneAllResult",
    "VMConfig",
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
