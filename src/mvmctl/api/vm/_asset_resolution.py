"""Resolution wrappers for VM operations.

This module contains functions for resolving image/kernel paths and VM selectors.
"""

from __future__ import annotations

from pathlib import Path

from mvmctl.api._internal._resolvers._image_resolver import ImageResolver
from mvmctl.api._internal._resolvers._kernel_resolver import KernelResolver
from mvmctl.api._internal._resolvers._vm_resolver import VMResolver

__all__ = [
    "resolve_image_path",
    "resolve_kernel_path",
    "resolve_image_id_path",
    "resolve_kernel_id_path",
    "resolve_image_multi_strategy",
    "resolve_kernel_multi_strategy",
    "resolve_vm_selector",
]


def resolve_image_path(image: str) -> Path:
    """Resolve an image identifier to a filesystem path.

    Args:
        image: Image identifier (os_slug, ID prefix, or path)

    Returns:
        Resolved path to the image file
    """
    from mvmctl.api.assets import resolve_image_path as _api_resolve_image_path

    return _api_resolve_image_path(image)


def resolve_kernel_path(kernel: str) -> Path:
    """Resolve a kernel identifier to a filesystem path.

    Args:
        kernel: Kernel identifier (version, ID prefix, or path)

    Returns:
        Resolved path to the kernel file
    """
    from mvmctl.api.kernel import resolve_kernel_path as _api_resolve_kernel_path

    return _api_resolve_kernel_path(kernel)


def resolve_image_id_path(image: str) -> Path:
    """Resolve an image ID prefix to a filesystem path.

    Args:
        image: Image ID prefix

    Returns:
        Resolved path to the image file
    """
    from mvmctl.api.assets import resolve_image_id_path as _api_resolve_image_id_path

    return _api_resolve_image_id_path(image)


def resolve_kernel_id_path(kernel: str) -> Path:
    """Resolve a kernel ID prefix to a filesystem path.

    Args:
        kernel: Kernel ID prefix

    Returns:
        Resolved path to the kernel file
    """
    from mvmctl.api.kernel import resolve_kernel_id_path as _api_resolve_kernel_id_path

    return _api_resolve_kernel_id_path(kernel)


def resolve_kernel_multi_strategy(value: str) -> Path:
    """Resolve kernel value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/')
    2. Short-ID resolution against database via KernelResolver

    Args:
        value: Kernel identifier (path, version, or ID prefix)

    Returns:
        Resolved path to the kernel file

    Raises:
        AssetNotFoundError: If kernel cannot be resolved
    """
    resolver = KernelResolver()
    kernel_item = resolver.resolve(value)
    return Path(kernel_item.path)


def resolve_image_multi_strategy(value: str) -> Path:
    """Resolve image value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/' or ends with .ext4/.btrfs)
    2. YAML image name lookup (via os_slug)
    3. Short-ID resolution against database via ImageResolver

    Args:
        value: Image identifier (path, os_slug, or ID prefix)

    Returns:
        Resolved path to the image file

    Raises:
        AssetNotFoundError: If image cannot be resolved
    """
    resolver = ImageResolver()
    image_item = resolver.resolve(value)
    return Path(image_item.path)


def resolve_vm_selector(selector: str) -> str:
    """Resolve a VM selector (name or ID prefix) to full VM name.

    Args:
        selector: VM name, ID prefix, or regex pattern

    Returns:
        Full VM name

    Raises:
        VMNotFoundError: If no matching VM found
    """
    import re

    from mvmctl.core.vm_manager import get_vm_manager
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()

    # Check if it's a regex pattern
    if "/" in selector:
        pattern = re.compile(selector)
        all_vms = manager.list_all()
        matches = [vm.name for vm in all_vms if pattern.match(vm.name)]
        if not matches:
            raise VMNotFoundError(f"No VMs match pattern: {selector}")
        if len(matches) > 1:
            raise VMNotFoundError(f"Pattern {selector} matches multiple VMs: {matches}")
        return matches[0]

    # Try VMResolver first (DB-based resolution)
    resolver = VMResolver()
    try:
        vm = resolver.resolve(selector)
        return vm.name
    except Exception:
        pass

    # Fallback to manager lookup
    vm_from_manager = manager.get(selector)
    if vm_from_manager is not None:
        return vm_from_manager.name

    raise VMNotFoundError(f"VM not found: {selector}")
