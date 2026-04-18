"""Data models for MicroVM Manager."""

from mvmctl.models.binary import BinaryItem
from mvmctl.models.bulk import BulkResult, BulkResultItem
from mvmctl.models.cache import PruneAllResult
from mvmctl.models.cloud_init import (
    CloudInitConfig,
    CloudInitMode,
    CloudInitStatus,
    CloudInitWriteConfig,
)
from mvmctl.models.firecracker import InstanceDescription, InstanceInfo
from mvmctl.models.host import HostStateChangeItem, HostStateItem
from mvmctl.models.image import ImageItem, ImageSpec
from mvmctl.models.kernel import KernelItem, KernelSpec
from mvmctl.models.key import SSHKeyItem
from mvmctl.models.network import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRuleItem,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
    LeaseEntry,
    NetworkEntry,
    NetworkInspectInfo,
    NetworkItem,
    NetworkLeaseItem,
)
from mvmctl.models.vm import (
    ConsoleInfo,
    ConsoleState,
    VMConfig,
    VMCreateInput,
    VMInspectInfo,
    VMInstanceItem,
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
    "BulkResult",
    "BulkResultItem",
    "CloudInitConfig",
    "CloudInitMode",
    "CloudInitStatus",
    "CloudInitWriteConfig",
    "ConsoleInfo",
    "ConsoleState",
    "HostStateChangeItem",
    "HostStateItem",
    "IPTablesChain",
    "IPTablesPort",
    "IPTablesProtocol",
    "IPTablesRuleItem",
    "IPTablesRuleType",
    "IPTablesTable",
    "IPTablesTarget",
    "IPTablesWildcard",
    "ImageItem",
    "ImageSpec",
    "InstanceDescription",
    "InstanceInfo",
    "KernelItem",
    "KernelSpec",
    "LeaseEntry",
    "NetworkEntry",
    "NetworkInspectInfo",
    "NetworkItem",
    "NetworkLeaseItem",
    "PruneAllResult",
    "SSHKeyItem",
    "VMConfig",
    "VMCreateInput",
    "VMInstanceItem",
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
