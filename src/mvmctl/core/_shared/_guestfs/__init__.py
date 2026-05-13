"""Guestfs utilities for image processing."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from ._base import OptimizedGuestfs
    from ._kernel_detector import KernelDetector
    from ._provisioner import GuestfsProvisioner
    from ._service import GuestfsService

__all__ = [
    "OptimizedGuestfs",
    "GuestfsProvisioner",
    "KernelDetector",
    "GuestfsService",
]

_LAZY_MAP = {
    "OptimizedGuestfs": (
        "mvmctl.core._shared._guestfs._base",
        "OptimizedGuestfs",
    ),
    "KernelDetector": (
        "mvmctl.core._shared._guestfs._kernel_detector",
        "KernelDetector",
    ),
    "GuestfsProvisioner": (
        "mvmctl.core._shared._guestfs._provisioner",
        "GuestfsProvisioner",
    ),
    "GuestfsService": (
        "mvmctl.core._shared._guestfs._service",
        "GuestfsService",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
