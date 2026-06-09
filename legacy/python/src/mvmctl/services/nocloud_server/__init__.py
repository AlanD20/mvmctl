"""
NoCloud HTTP server package for cloud-init datasource.

This package provides subprocess-based NoCloud server functionality
for serving cloud-init files to VMs via the nocloud-net datasource.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

__all__ = ["NoCloudNetServerManager"]

_LAZY_MAP = {
    "NoCloudNetServerManager": (
        "mvmctl.services.nocloud_server.manager",
        "NoCloudNetServerManager",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
