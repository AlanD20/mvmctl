"""
mvmctl public API — stable, curated interface for automation.

All public types and operations are re-exported here. Import from this
module for stability guarantees::

    from mvmctl.api import VMOperation, VMCreateInput

    VMOperation.create(VMCreateInput(name="my-vm", ...))
"""

from __future__ import annotations

# Operation classes — primary actions (create, remove, list, start, stop, etc.)
from mvmctl.api.binary_operations import BinaryOperation  # noqa: F401
from mvmctl.api.cache_operations import CacheOperation  # noqa: F401
from mvmctl.api.config_operations import ConfigOperation  # noqa: F401
from mvmctl.api.console_operations import (  # noqa: F401
    ConsoleConnectionInfo,
    ConsoleOperation,
)
from mvmctl.api.host_operations import HostOperation  # noqa: F401
from mvmctl.api.image_operations import ImageOperation  # noqa: F401
from mvmctl.api.init_operations import (  # noqa: F401
    InitOperation,
    InitResult,
    InitStepResult,
)

# Input classes — request contracts (Input → Request → Resolved pipeline)
from mvmctl.api.inputs import (  # noqa: F401
    BinaryFetchInput,
    BinaryInput,
    ConsoleInput,
    ConsoleRequest,
    ImageFetchInput,
    ImageImportInput,
    ImageInput,
    KernelFetchInput,
    KernelInput,
    KeyCreateInput,
    KeyInput,
    LogInput,
    NetworkCreateInput,
    NetworkInput,
    SSHInput,
    VMCreateInput,
    VMInput,
)
from mvmctl.api.kernel_operations import KernelOperation  # noqa: F401
from mvmctl.api.key_operations import KeyOperation  # noqa: F401
from mvmctl.api.logs_operations import LogOperation  # noqa: F401
from mvmctl.api.network_operations import NetworkOperation  # noqa: F401
from mvmctl.api.ssh_operations import SSHOperation  # noqa: F401
from mvmctl.api.vm_operations import VMOperation  # noqa: F401

__all__ = [
    # Operation classes
    "BinaryOperation",
    "CacheOperation",
    "ConfigOperation",
    "ConsoleConnectionInfo",
    "ConsoleOperation",
    "HostOperation",
    "ImageOperation",
    "InitOperation",
    "InitResult",
    "InitStepResult",
    "KernelOperation",
    "KeyOperation",
    "LogOperation",
    "NetworkOperation",
    "SSHOperation",
    "VMOperation",
    # Input classes
    "BinaryFetchInput",
    "BinaryInput",
    "ImageFetchInput",
    "ImageImportInput",
    "ImageInput",
    "KernelFetchInput",
    "KernelInput",
    "KeyCreateInput",
    "KeyInput",
    "NetworkCreateInput",
    "NetworkInput",
    "LogInput",
    "SSHInput",
    "VMCreateInput",
    "VMInput",
]
