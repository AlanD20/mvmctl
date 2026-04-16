"""Input resolution layer for VM operations.

This module provides Request classes that resolve CLI input into
explicit domain objects for orchestration.
"""

from __future__ import annotations

from mvmctl.api.input.vm_create_request import (
    ResolvedVMCreateRequest,
    VMCreateRequest,
)
from mvmctl.api.input.vm_request import ResolvedVMRequest, VMRequest

__all__ = [
    "VMCreateRequest",
    "ResolvedVMCreateRequest",
    "VMRequest",
    "ResolvedVMRequest",
]
