"""
Input resolution layer for API boundary.

This module provides Input/Request/Config classes that represent
API boundary contracts — raw CLI arguments, resolved requests,
and portable export configurations.
"""

from __future__ import annotations

from mvmctl.api.inputs._binary_input import (
    BinaryInput,
    BinaryRequest,
    ResolvedBinaryInput,
)
from mvmctl.api.inputs._binary_pull_input import (
    BinaryPullInput,
    BinaryPullRequest,
    ResolvedBinaryPullInput,
)
from mvmctl.api.inputs._config_input import (
    ConfigInput,
    ConfigRequest,
    ResolvedConfigInput,
)
from mvmctl.api.inputs._console_input import (
    ConsoleInput,
    ConsoleRequest,
    ResolvedConsoleInput,
)
from mvmctl.api.inputs._image_acquire_input import (
    ImageImportInput,
    ImagePullInput,
)
from mvmctl.api.inputs._image_input import (
    ImageInput,
    ImageRequest,
    ResolvedImageInput,
)
from mvmctl.api.inputs._kernel_input import (
    KernelInput,
    KernelRequest,
    ResolvedKernelInput,
)
from mvmctl.api.inputs._kernel_pull_input import (
    KernelPullInput,
    KernelPullRequest,
    ResolvedKernelPullRequest,
)
from mvmctl.api.inputs._key_create_input import (
    KeyCreateInput,
    KeyCreateRequest,
    ResolvedKeyCreateInput,
)
from mvmctl.api.inputs._key_input import (
    KeyInput,
    KeyRequest,
    ResolvedKeyInput,
)
from mvmctl.api.inputs._logs_input import (
    LogInput,
    LogRequest,
    ResolvedLogInput,
)
from mvmctl.api.inputs._network_create_input import (
    NetworkCreateInput,
    NetworkCreateRequest,
    ResolvedNetworkCreateRequest,
)
from mvmctl.api.inputs._network_input import (
    NetworkInput,
    NetworkRequest,
    ResolvedNetworkInput,
)
from mvmctl.api.inputs._ssh_input import (
    ResolvedSSHInput,
    SSHInput,
    SSHRequest,
)
from mvmctl.api.inputs._vm_create_input import (
    CloudInitModeResolved,
    ResolvedVMCreateInput,
    VMCreateInput,
    VMCreateRequest,
)
from mvmctl.api.inputs._vm_export_config import (
    VMExportBinaryConfig,
    VMExportBootConfig,
    VMExportCloudInitConfig,
    VMExportComputeConfig,
    VMExportConfig,
    VMExportFirecrackerConfig,
    VMExportImageConfig,
    VMExportKernelConfig,
    VMExportNetworkConfig,
)
from mvmctl.api.inputs._vm_import_input import (
    VMImportInput,
    VMImportRequest,
)
from mvmctl.api.inputs._vm_input import (
    ResolvedVMInput,
    VMInput,
    VMRequest,
)

__all__ = [
    "ConsoleInput",
    "ConsoleRequest",
    "ResolvedConsoleInput",
    "LogInput",
    "LogRequest",
    "ResolvedLogInput",
    "BinaryPullInput",
    "BinaryPullRequest",
    "BinaryInput",
    "BinaryRequest",
    "CloudInitModeResolved",
    "ConfigInput",
    "ConfigRequest",
    "ResolvedConfigInput",
    "ImagePullInput",
    "ImageImportInput",
    "ImageInput",
    "ImageRequest",
    "ResolvedImageInput",
    "KernelPullInput",
    "KernelPullRequest",
    "KernelInput",
    "KernelRequest",
    "KeyCreateInput",
    "KeyCreateRequest",
    "KeyInput",
    "KeyRequest",
    "NetworkCreateInput",
    "NetworkCreateRequest",
    "NetworkInput",
    "NetworkRequest",
    "ResolvedBinaryPullInput",
    "ResolvedBinaryInput",
    "ResolvedKernelPullRequest",
    "ResolvedKernelInput",
    "ResolvedKeyCreateInput",
    "ResolvedKeyInput",
    "ResolvedNetworkCreateRequest",
    "ResolvedNetworkInput",
    "ResolvedSSHInput",
    "ResolvedVMCreateInput",
    "ResolvedVMInput",
    "SSHInput",
    "SSHRequest",
    "VMCreateInput",
    "VMCreateRequest",
    "VMExportBinaryConfig",
    "VMExportBootConfig",
    "VMExportCloudInitConfig",
    "VMExportComputeConfig",
    "VMExportConfig",
    "VMExportFirecrackerConfig",
    "VMExportImageConfig",
    "VMExportKernelConfig",
    "VMExportNetworkConfig",
    "VMImportInput",
    "VMImportRequest",
    "VMInput",
    "VMRequest",
]
