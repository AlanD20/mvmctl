"""Input resolution layer for VM operations.

This module provides Request classes that resolve CLI input into
explicit domain objects for orchestration.
"""

from __future__ import annotations

from mvmctl.api.inputs._vm_create_request import (
    CloudInitModeResolved,
    ResolvedVMCreateRequest,
    VMCreateInput,
    VMCreateRequest,
)
from mvmctl.api.inputs._vm_request import (
    ResolvedVMRequest,
    VMInput,
    VMRequest,
)

__all__ = [
    "CloudInitModeResolved",
    "ResolvedVMCreateRequest",
    "ResolvedVMRequest",
    "VMCreateInput",
    "VMCreateRequest",
    "VMInput",
    "VMRequest",
]
