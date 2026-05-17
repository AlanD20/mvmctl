"""
mvmctl data models — stable, curated interface for typed domain data.

All public model types are re-exported here. Import from this module
for stability guarantees::

    from mvmctl.models import VMInstanceItem, NetworkItem

    vm: VMInstanceItem = ...
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.models.binary import BinaryItem
    from mvmctl.models.bulk import BulkResult, BulkResultItem
    from mvmctl.models.cache import CleanResult, PruneAllResult
    from mvmctl.models.cloudinit import (
        CloudInitMode,
        CloudInitStatus,
    )
    from mvmctl.models.firecracker import DriveConfig, FirecrackerConfig
    from mvmctl.models.host import (
        HostHardware,
        HostLimits,
        HostResources,
        HostStateChangeItem,
        HostStateItem,
    )
    from mvmctl.models.image import ImageItem, ImageSpec, ImageVersion
    from mvmctl.models.kernel import KernelItem, KernelPullResult, KernelSpec
    from mvmctl.models.key import SSHKeyItem
    from mvmctl.models.network import (
        FirewallBackendType,
        FirewallChain,
        FirewallPort,
        FirewallProtocol,
        FirewallRule,
        FirewallRuleType,
        FirewallTable,
        FirewallTarget,
        FirewallWildcard,
        NetworkItem,
        NetworkLeaseItem,
    )
    from mvmctl.models.provisioner import ProvisionerType
    from mvmctl.models.vm import (
        ConsoleInfo,
        ConsoleState,
        VMInspectInfo,
        VMInstanceItem,
        VMStatus,
    )
    from mvmctl.models.volume import VolumeItem, VolumeStatus

__all__ = [
    "BinaryItem",
    "BulkResult",
    "BulkResultItem",
    "CloudInitMode",
    "CloudInitStatus",
    "ConsoleInfo",
    "ConsoleState",
    "DriveConfig",
    "FirecrackerConfig",
    "HostHardware",
    "HostLimits",
    "HostResources",
    "HostStateChangeItem",
    "HostStateItem",
    "FirewallBackendType",
    "FirewallChain",
    "FirewallPort",
    "FirewallProtocol",
    "FirewallRule",
    "FirewallRuleType",
    "FirewallTable",
    "FirewallTarget",
    "FirewallWildcard",
    "ImageItem",
    "ImageSpec",
    "ImageVersion",
    "KernelPullResult",
    "KernelItem",
    "KernelSpec",
    "NetworkItem",
    "NetworkLeaseItem",
    "ProvisionerType",
    "CleanResult",
    "PruneAllResult",
    "SSHKeyItem",
    "VolumeItem",
    "VolumeStatus",
    "VMInspectInfo",
    "VMInstanceItem",
    "VMStatus",
    "VolumeStatus",
]

_LAZY_MAP = {
    "BinaryItem": ("mvmctl.models.binary", "BinaryItem"),
    "BulkResult": ("mvmctl.models.bulk", "BulkResult"),
    "BulkResultItem": ("mvmctl.models.bulk", "BulkResultItem"),
    "CleanResult": ("mvmctl.models.cache", "CleanResult"),
    "PruneAllResult": ("mvmctl.models.cache", "PruneAllResult"),
    "CloudInitMode": ("mvmctl.models.cloudinit", "CloudInitMode"),
    "CloudInitStatus": ("mvmctl.models.cloudinit", "CloudInitStatus"),
    "DriveConfig": ("mvmctl.models.firecracker", "DriveConfig"),
    "FirecrackerConfig": ("mvmctl.models.firecracker", "FirecrackerConfig"),
    "HostHardware": ("mvmctl.models.host", "HostHardware"),
    "HostLimits": ("mvmctl.models.host", "HostLimits"),
    "HostResources": ("mvmctl.models.host", "HostResources"),
    "HostStateChangeItem": ("mvmctl.models.host", "HostStateChangeItem"),
    "HostStateItem": ("mvmctl.models.host", "HostStateItem"),
    "ImageItem": ("mvmctl.models.image", "ImageItem"),
    "ImageSpec": ("mvmctl.models.image", "ImageSpec"),
    "ImageVersion": ("mvmctl.models.image", "ImageVersion"),
    "KernelItem": ("mvmctl.models.kernel", "KernelItem"),
    "KernelPullResult": ("mvmctl.models.kernel", "KernelPullResult"),
    "KernelSpec": ("mvmctl.models.kernel", "KernelSpec"),
    "SSHKeyItem": ("mvmctl.models.key", "SSHKeyItem"),
    "FirewallBackendType": ("mvmctl.models.network", "FirewallBackendType"),
    "FirewallChain": ("mvmctl.models.network", "FirewallChain"),
    "FirewallPort": ("mvmctl.models.network", "FirewallPort"),
    "FirewallProtocol": ("mvmctl.models.network", "FirewallProtocol"),
    "FirewallRule": ("mvmctl.models.network", "FirewallRule"),
    "FirewallRuleType": ("mvmctl.models.network", "FirewallRuleType"),
    "FirewallRuleResult": (
        "mvmctl.models.network",
        "FirewallRuleResult",
    ),
    "FirewallTable": ("mvmctl.models.network", "FirewallTable"),
    "FirewallTarget": ("mvmctl.models.network", "FirewallTarget"),
    "FirewallWildcard": ("mvmctl.models.network", "FirewallWildcard"),
    "NetworkItem": ("mvmctl.models.network", "NetworkItem"),
    "NetworkLeaseItem": ("mvmctl.models.network", "NetworkLeaseItem"),
    "ProvisionerType": ("mvmctl.models.provisioner", "ProvisionerType"),
    "ConsoleInfo": ("mvmctl.models.vm", "ConsoleInfo"),
    "ConsoleState": ("mvmctl.models.vm", "ConsoleState"),
    "VMInspectInfo": ("mvmctl.models.vm", "VMInspectInfo"),
    "VMInstanceItem": ("mvmctl.models.vm", "VMInstanceItem"),
    "VMStatus": ("mvmctl.models.vm", "VMStatus"),
    "VolumeItem": ("mvmctl.models.volume", "VolumeItem"),
    "VolumeStatus": ("mvmctl.models.volume", "VolumeStatus"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
