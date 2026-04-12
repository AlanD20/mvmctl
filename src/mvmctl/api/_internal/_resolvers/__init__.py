"""Shared DB-backed resolvers for API modules."""

from __future__ import annotations

from mvmctl.api._internal._resolvers._binary_resolver import BinaryResolver, BinaryResolveResult
from mvmctl.api._internal._resolvers._image_resolver import ImageResolver, ImageResolveResult
from mvmctl.api._internal._resolvers._kernel_resolver import KernelResolver, KernelResolveResult
from mvmctl.api._internal._resolvers._key_resolver import KeyResolveResult, KeyResolver
from mvmctl.api._internal._resolvers._network_resolver import NetworkResolver, NetworkResolveResult
from mvmctl.api._internal._resolvers._vm_resolver import VMResolver, VMResolveResult

__all__ = [
    "BinaryResolver",
    "BinaryResolveResult",
    "ImageResolver",
    "ImageResolveResult",
    "KernelResolver",
    "KernelResolveResult",
    "KeyResolver",
    "KeyResolveResult",
    "NetworkResolver",
    "NetworkResolveResult",
    "VMResolver",
    "VMResolveResult",
]
