"""Kernel domain - Kernel management and resolution."""

from __future__ import annotations

from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver, KernelResolveResult

__all__ = [
    "KernelRepository",
    "KernelResolver",
    "KernelResolveResult",
]
