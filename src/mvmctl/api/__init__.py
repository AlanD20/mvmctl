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
    "VMOperation",
    # Input classes
    "BinaryFetchInput",
    "BinaryInput",
    "ConsoleInput",
    "ConsoleRequest",
    "ImageFetchInput",
    "ImageImportInput",
    "ImageInput",
    "KernelFetchInput",
    "KernelInput",
    "KeyCreateInput",
    "KeyInput",
    "LogInput",
    "NetworkCreateInput",
    "NetworkInput",
    "SSHInput",
    "VMCreateInput",
    "VMInput",
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
    "VMOperation": ("mvmctl.api.vm_operations", "VMOperation"),
    # ── Input classes ───────────────────────────────────────────────
    "BinaryFetchInput": (
        "mvmctl.api.inputs._binary_fetch_input",
        "BinaryFetchInput",
    ),
    "BinaryInput": ("mvmctl.api.inputs._binary_input", "BinaryInput"),
    "ConsoleInput": ("mvmctl.api.inputs._console_input", "ConsoleInput"),
    "ConsoleRequest": ("mvmctl.api.inputs._console_input", "ConsoleRequest"),
    "ImageFetchInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImageFetchInput",
    ),
    "ImageImportInput": (
        "mvmctl.api.inputs._image_acquire_input",
        "ImageImportInput",
    ),
    "ImageInput": ("mvmctl.api.inputs._image_input", "ImageInput"),
    "KernelFetchInput": (
        "mvmctl.api.inputs._kernel_fetch_input",
        "KernelFetchInput",
    ),
    "KernelInput": ("mvmctl.api.inputs._kernel_input", "KernelInput"),
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
    "VMCreateInput": (
        "mvmctl.api.inputs._vm_create_input",
        "VMCreateInput",
    ),
    "VMInput": ("mvmctl.api.inputs._vm_input", "VMInput"),
}


def __getattr__(name: str) -> object:
    """Deferred import of submodules on first attribute access."""
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    """Expose public API for tab-completion and introspection."""
    return __all__
