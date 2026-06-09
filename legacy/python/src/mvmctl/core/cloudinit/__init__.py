"""
Cloud-init internal API module.

This module provides OOP-based cloud-init configuration management
and provisioning for VM creation.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from ._manager import CloudInitManager
    from ._provisioner import (
        CloudInitProvisionConfig,
        CloudInitProvisioner,
        CloudInitProvisionResult,
    )

__all__ = [
    "CloudInitManager",
    "CloudInitProvisionConfig",
    "CloudInitProvisioner",
    "CloudInitProvisionResult",
]

_LAZY_MAP = {
    "CloudInitManager": ("mvmctl.core.cloudinit._manager", "CloudInitManager"),
    "CloudInitProvisionConfig": (
        "mvmctl.core.cloudinit._provisioner",
        "CloudInitProvisionConfig",
    ),
    "CloudInitProvisioner": (
        "mvmctl.core.cloudinit._provisioner",
        "CloudInitProvisioner",
    ),
    "CloudInitProvisionResult": (
        "mvmctl.core.cloudinit._provisioner",
        "CloudInitProvisionResult",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
