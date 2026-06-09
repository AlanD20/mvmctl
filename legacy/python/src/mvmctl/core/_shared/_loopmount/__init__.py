"""Loop-mount manager — binary lifecycle for the mvm-provision subprocess."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core._shared._loopmount._manager import LoopMountManager
    from mvmctl.core._shared._loopmount._provisioner import LoopMountProvisioner
    from mvmctl.exceptions import (
        LoopMountBinaryNotFoundError,
        LoopMountError,
        LoopMountTimeoutError,
    )

__all__ = [
    "LoopMountBinaryNotFoundError",
    "LoopMountError",
    "LoopMountManager",
    "LoopMountProvisioner",
    "LoopMountTimeoutError",
]

_LAZY_MAP = {
    "LoopMountManager": (
        "mvmctl.core._shared._loopmount._manager",
        "LoopMountManager",
    ),
    "LoopMountProvisioner": (
        "mvmctl.core._shared._loopmount._provisioner",
        "LoopMountProvisioner",
    ),
    "LoopMountBinaryNotFoundError": (
        "mvmctl.exceptions",
        "LoopMountBinaryNotFoundError",
    ),
    "LoopMountError": ("mvmctl.exceptions", "LoopMountError"),
    "LoopMountTimeoutError": ("mvmctl.exceptions", "LoopMountTimeoutError"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
