"""Backend classes — shared between VMProvisioner and ImageProvisioner."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core._shared._provisioner._backend import (
        ProvisionerBackend,
        _GuestfsBackend,
        _LoopMountBackend,
    )

__all__ = [
    "ProvisionerBackend",
    "_GuestfsBackend",
    "_LoopMountBackend",
]

_LAZY_MAP = {
    "ProvisionerBackend": (
        "mvmctl.core._shared._provisioner._backend",
        "ProvisionerBackend",
    ),
    "_GuestfsBackend": (
        "mvmctl.core._shared._provisioner._backend",
        "_GuestfsBackend",
    ),
    "_LoopMountBackend": (
        "mvmctl.core._shared._provisioner._backend",
        "_LoopMountBackend",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
