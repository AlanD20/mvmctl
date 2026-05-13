"""Kernel domain - Kernel management and resolution."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.kernel._controller import KernelController
    from mvmctl.core.kernel._repository import KernelRepository
    from mvmctl.core.kernel._resolver import KernelResolver, KernelResolveResult
    from mvmctl.core.kernel._service import (
        KernelBuildResult,
        KernelConfigResult,
        KernelPipelineResult,
        KernelService,
        ParsedKernelFilename,
    )

__all__ = [
    "KernelBuildResult",
    "KernelConfigResult",
    "KernelController",
    "KernelPipelineResult",
    "KernelRepository",
    "KernelResolveResult",
    "KernelResolver",
    "KernelService",
    "ParsedKernelFilename",
]

_LAZY_MAP = {
    "KernelBuildResult": ("mvmctl.core.kernel._service", "KernelBuildResult"),
    "KernelConfigResult": ("mvmctl.core.kernel._service", "KernelConfigResult"),
    "KernelController": ("mvmctl.core.kernel._controller", "KernelController"),
    "KernelPipelineResult": (
        "mvmctl.core.kernel._service",
        "KernelPipelineResult",
    ),
    "KernelRepository": ("mvmctl.core.kernel._repository", "KernelRepository"),
    "KernelResolveResult": (
        "mvmctl.core.kernel._resolver",
        "KernelResolveResult",
    ),
    "KernelResolver": ("mvmctl.core.kernel._resolver", "KernelResolver"),
    "KernelService": ("mvmctl.core.kernel._service", "KernelService"),
    "ParsedKernelFilename": (
        "mvmctl.core.kernel._service",
        "ParsedKernelFilename",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
