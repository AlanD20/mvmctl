"""
mvmctl public API — stable, curated interface for automation.

All public types and operations are re-exported here. Import from this
module for stability guarantees::

    from mvmctl.api import VMOperation, VMCreateInput

    VMOperation.create(VMCreateInput(name="my-vm", ...))

**Lazy imports:** Submodules are imported on first access via
``__getattr__`` (PEP 562). ``import mvmctl.api`` is cheap — the
~230 ms import cost for the full core layer is deferred until the
first operation class is actually accessed.
"""

from __future__ import annotations

from mvmctl.utils._lazy_import import resolve_lazy

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
    "VolumeOperation",
    "VMOperation",
    # Input classes
    "BinaryPullInput",
    "BinaryInput",
    "ConsoleInput",
    "ConsoleRequest",
    "ImagePullInput",
    "ImageImportInput",
    "ImageInput",
    "KernelImportInput",
    "KernelImportRequest",
    "KernelPullInput",
    "ResolvedKernelImportInput",
    "KernelInput",
    "KeyCreateInput",
    "KeyInput",
    "LogInput",
    "NetworkCreateInput",
    "NetworkInput",
    "SSHInput",
    "VolumeCreateInput",
    "VolumeInput",
    "VMCreateInput",
    "VMImportInput",
    "VMImportRequest",
    "VMInput",
    # Export/import config models
    "VMExportComputeConfig",
    "VMExportImageConfig",
    "VMExportKernelConfig",
    "VMExportBinaryConfig",
    "VMExportNetworkConfig",
    "VMExportBootConfig",
    "VMExportFirecrackerConfig",
    "VMExportCloudInitConfig",
    "VMExportConfig",
]

_LAZY_MAP: dict[str, tuple[str, str]] = {
    # ── Operations ──────────────────────────────────────────────────
    "BinaryOperation": ("mvmctl.api.binary_operations", "BinaryOperation"),
    "CacheOperation": ("mvmctl.api.cache_operations", "CacheOperation"),
    "ConfigOperation": ("mvmctl.api.config_operations", "ConfigOperation"),
    "ConsoleConnectionInfo": (
        "mvmctl.api.console_operations",
        "ConsoleConnectionInfo",
    ),
    "ConsoleOperation": ("mvmctl.api.console_operations", "ConsoleOperation"),
    "HostOperation": ("mvmctl.api.host_operations", "HostOperation"),
    "ImageOperation": ("mvmctl.api.image_operations", "ImageOperation"),
    "InitOperation": ("mvmctl.api.init_operations", "InitOperation"),
    "InitResult": ("mvmctl.api.init_operations", "InitResult"),
    "InitStepResult": ("mvmctl.api.init_operations", "InitStepResult"),
    "KernelOperation": ("mvmctl.api.kernel_operations", "KernelOperation"),
    "KeyOperation": ("mvmctl.api.key_operations", "KeyOperation"),
    "LogOperation": ("mvmctl.api.logs_operations", "LogOperation"),
    "NetworkOperation": ("mvmctl.api.network_operations", "NetworkOperation"),
    "SSHOperation": ("mvmctl.api.ssh_operations", "SSHOperation"),
    "VolumeOperation": (
        "mvmctl.api.volume_operations",
        "VolumeOperation",
    ),
    "VMOperation": ("mvmctl.api.vm_operations", "VMOperation"),
    # ── Input classes ───────────────────────────────────────────────
    "BinaryPullInput": (
        "mvmctl.api.inputs._binary_pull_input",
        "BinaryPullInput",
    ),
    "BinaryInput": ("mvmctl.api.inputs._binary_input", "BinaryInput"),
    "ConsoleInput": ("mvmctl.api.inputs._console_input", "ConsoleInput"),
    "ConsoleRequest": ("mvmctl.api.inputs._console_input", "ConsoleRequest"),
    "ImagePullInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImagePullInput",
    ),
    "ImageImportInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImageImportInput",
    ),
    "ImageInput": ("mvmctl.api.inputs._image_input", "ImageInput"),
    "KernelImportInput": (
        "mvmctl.api.inputs._kernel_import_input",
        "KernelImportInput",
    ),
    "KernelImportRequest": (
        "mvmctl.api.inputs._kernel_import_input",
        "KernelImportRequest",
    ),
    "KernelPullInput": (
        "mvmctl.api.inputs._kernel_pull_input",
        "KernelPullInput",
    ),
    "KernelInput": ("mvmctl.api.inputs._kernel_input", "KernelInput"),
    "ResolvedKernelImportInput": (
        "mvmctl.api.inputs._kernel_import_input",
        "ResolvedKernelImportInput",
    ),
    "KeyCreateInput": (
        "mvmctl.api.inputs._key_create_input",
        "KeyCreateInput",
    ),
    "KeyInput": ("mvmctl.api.inputs._key_input", "KeyInput"),
    "LogInput": ("mvmctl.api.inputs._logs_input", "LogInput"),
    "NetworkCreateInput": (
        "mvmctl.api.inputs._network_create_input",
        "NetworkCreateInput",
    ),
    "NetworkInput": ("mvmctl.api.inputs._network_input", "NetworkInput"),
    "SSHInput": ("mvmctl.api.inputs._ssh_input", "SSHInput"),
    "VolumeCreateInput": (
        "mvmctl.api.inputs._volume_create_input",
        "VolumeCreateInput",
    ),
    "VolumeInput": (
        "mvmctl.api.inputs._volume_input",
        "VolumeInput",
    ),
    "VMCreateInput": (
        "mvmctl.api.inputs._vm_create_input",
        "VMCreateInput",
    ),
    "VMImportInput": (
        "mvmctl.api.inputs._vm_import_input",
        "VMImportInput",
    ),
    "VMImportRequest": (
        "mvmctl.api.inputs._vm_import_input",
        "VMImportRequest",
    ),
    "VMInput": ("mvmctl.api.inputs._vm_input", "VMInput"),
    # ── Export/import config models ────────────────────────────
    "VMExportComputeConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportComputeConfig",
    ),
    "VMExportImageConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportImageConfig",
    ),
    "VMExportKernelConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportKernelConfig",
    ),
    "VMExportBinaryConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportBinaryConfig",
    ),
    "VMExportNetworkConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportNetworkConfig",
    ),
    "VMExportBootConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportBootConfig",
    ),
    "VMExportFirecrackerConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportFirecrackerConfig",
    ),
    "VMExportCloudInitConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportCloudInitConfig",
    ),
    "VMExportConfig": (
        "mvmctl.api.inputs._vm_export_config",
        "VMExportConfig",
    ),
}


def __getattr__(name: str) -> object:
    """Deferred import of submodules on first attribute access."""
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    """Expose public API for tab-completion and introspection."""
    return __all__
