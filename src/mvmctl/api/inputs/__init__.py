"""
Input resolution layer for API boundary.

This module provides Input/Request/Config classes that represent
API boundary contracts — raw CLI arguments, resolved requests,
and portable export configurations.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
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
    from mvmctl.api.inputs._volume_create_input import (
        ResolvedVolumeCreateInput,
        VolumeCreateInput,
        VolumeCreateRequest,
    )
    from mvmctl.api.inputs._volume_input import (
        ResolvedVolumeInput,
        VolumeInput,
        VolumeRequest,
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
    "SSHInput",
    "SSHRequest",
    "ResolvedVolumeCreateInput",
    "ResolvedVolumeInput",
    "VolumeCreateInput",
    "VolumeCreateRequest",
    "VolumeInput",
    "VolumeRequest",
    "ResolvedVMCreateInput",
    "ResolvedVMInput",
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

_LAZY_MAP = {
    "BinaryInput": ("mvmctl.api.inputs._binary_input", "BinaryInput"),
    "BinaryRequest": ("mvmctl.api.inputs._binary_input", "BinaryRequest"),
    "ResolvedBinaryInput": (
        "mvmctl.api.inputs._binary_input",
        "ResolvedBinaryInput",
    ),
    "BinaryPullInput": (
        "mvmctl.api.inputs._binary_pull_input",
        "BinaryPullInput",
    ),
    "BinaryPullRequest": (
        "mvmctl.api.inputs._binary_pull_input",
        "BinaryPullRequest",
    ),
    "ResolvedBinaryPullInput": (
        "mvmctl.api.inputs._binary_pull_input",
        "ResolvedBinaryPullInput",
    ),
    "ConfigInput": ("mvmctl.api.inputs._config_input", "ConfigInput"),
    "ConfigRequest": ("mvmctl.api.inputs._config_input", "ConfigRequest"),
    "ResolvedConfigInput": (
        "mvmctl.api.inputs._config_input",
        "ResolvedConfigInput",
    ),
    "ConsoleInput": ("mvmctl.api.inputs._console_input", "ConsoleInput"),
    "ConsoleRequest": ("mvmctl.api.inputs._console_input", "ConsoleRequest"),
    "ResolvedConsoleInput": (
        "mvmctl.api.inputs._console_input",
        "ResolvedConsoleInput",
    ),
    "ImageImportInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImageImportInput",
    ),
    "ImagePullInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImagePullInput",
    ),
    "ImageInput": ("mvmctl.api.inputs._image_input", "ImageInput"),
    "ImageRequest": ("mvmctl.api.inputs._image_input", "ImageRequest"),
    "ResolvedImageInput": (
        "mvmctl.api.inputs._image_input",
        "ResolvedImageInput",
    ),
    "KernelInput": ("mvmctl.api.inputs._kernel_input", "KernelInput"),
    "KernelRequest": ("mvmctl.api.inputs._kernel_input", "KernelRequest"),
    "ResolvedKernelInput": (
        "mvmctl.api.inputs._kernel_input",
        "ResolvedKernelInput",
    ),
    "KernelPullInput": (
        "mvmctl.api.inputs._kernel_pull_input",
        "KernelPullInput",
    ),
    "KernelPullRequest": (
        "mvmctl.api.inputs._kernel_pull_input",
        "KernelPullRequest",
    ),
    "ResolvedKernelPullRequest": (
        "mvmctl.api.inputs._kernel_pull_input",
        "ResolvedKernelPullRequest",
    ),
    "KeyCreateInput": ("mvmctl.api.inputs._key_create_input", "KeyCreateInput"),
    "KeyCreateRequest": (
        "mvmctl.api.inputs._key_create_input",
        "KeyCreateRequest",
    ),
    "ResolvedKeyCreateInput": (
        "mvmctl.api.inputs._key_create_input",
        "ResolvedKeyCreateInput",
    ),
    "KeyInput": ("mvmctl.api.inputs._key_input", "KeyInput"),
    "KeyRequest": ("mvmctl.api.inputs._key_input", "KeyRequest"),
    "ResolvedKeyInput": ("mvmctl.api.inputs._key_input", "ResolvedKeyInput"),
    "LogInput": ("mvmctl.api.inputs._logs_input", "LogInput"),
    "LogRequest": ("mvmctl.api.inputs._logs_input", "LogRequest"),
    "ResolvedLogInput": ("mvmctl.api.inputs._logs_input", "ResolvedLogInput"),
    "NetworkCreateInput": (
        "mvmctl.api.inputs._network_create_input",
        "NetworkCreateInput",
    ),
    "NetworkCreateRequest": (
        "mvmctl.api.inputs._network_create_input",
        "NetworkCreateRequest",
    ),
    "ResolvedNetworkCreateRequest": (
        "mvmctl.api.inputs._network_create_input",
        "ResolvedNetworkCreateRequest",
    ),
    "NetworkInput": ("mvmctl.api.inputs._network_input", "NetworkInput"),
    "NetworkRequest": ("mvmctl.api.inputs._network_input", "NetworkRequest"),
    "ResolvedNetworkInput": (
        "mvmctl.api.inputs._network_input",
        "ResolvedNetworkInput",
    ),
    "ResolvedSSHInput": ("mvmctl.api.inputs._ssh_input", "ResolvedSSHInput"),
    "SSHInput": ("mvmctl.api.inputs._ssh_input", "SSHInput"),
    "SSHRequest": ("mvmctl.api.inputs._ssh_input", "SSHRequest"),
    "CloudInitModeResolved": (
        "mvmctl.api.inputs._vm_create_input",
        "CloudInitModeResolved",
    ),
    "ResolvedVMCreateInput": (
        "mvmctl.api.inputs._vm_create_input",
        "ResolvedVMCreateInput",
    ),
    "VMCreateInput": ("mvmctl.api.inputs._vm_create_input", "VMCreateInput"),
    "VMCreateRequest": (
        "mvmctl.api.inputs._vm_create_input",
        "VMCreateRequest",
    ),
    "VMExportBinaryConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportBinaryConfig",
    ),
    "VMExportBootConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportBootConfig",
    ),
    "VMExportCloudInitConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportCloudInitConfig",
    ),
    "VMExportComputeConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportComputeConfig",
    ),
    "VMExportConfig": ("mvmctl.api.inputs._vm_export_config", "VMExportConfig"),
    "VMExportFirecrackerConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportFirecrackerConfig",
    ),
    "VMExportImageConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportImageConfig",
    ),
    "VMExportKernelConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportKernelConfig",
    ),
    "VMExportNetworkConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportNetworkConfig",
    ),
    "VMImportInput": ("mvmctl.api.inputs._vm_import_input", "VMImportInput"),
    "VMImportRequest": (
        "mvmctl.api.inputs._vm_import_input",
        "VMImportRequest",
    ),
    "ResolvedVMInput": ("mvmctl.api.inputs._vm_input", "ResolvedVMInput"),
    "VMInput": ("mvmctl.api.inputs._vm_input", "VMInput"),
    "VMRequest": ("mvmctl.api.inputs._vm_input", "VMRequest"),
    "ResolvedVolumeCreateInput": (
        "mvmctl.api.inputs._volume_create_input",
        "ResolvedVolumeCreateInput",
    ),
    "VolumeCreateInput": (
        "mvmctl.api.inputs._volume_create_input",
        "VolumeCreateInput",
    ),
    "VolumeCreateRequest": (
        "mvmctl.api.inputs._volume_create_input",
        "VolumeCreateRequest",
    ),
    "ResolvedVolumeInput": (
        "mvmctl.api.inputs._volume_input",
        "ResolvedVolumeInput",
    ),
    "VolumeInput": ("mvmctl.api.inputs._volume_input", "VolumeInput"),
    "VolumeRequest": ("mvmctl.api.inputs._volume_input", "VolumeRequest"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
