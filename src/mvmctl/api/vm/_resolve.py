"""Resolution wrappers for VM operations.

This module contains functions for resolving image/kernel paths and VM selectors.
"""

from __future__ import annotations

from pathlib import Path

from mvmctl.core.metadata import list_image_entries
from mvmctl.core.vm_manager import get_vm_manager
from mvmctl.exceptions import MVMError
from mvmctl.utils.fs import get_cache_dir, get_images_dir, get_kernels_dir

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


def resolve_image_multi_strategy(value: str) -> Path:
    """Resolve image value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/' or ends with .ext4/.btrfs)
    2. YAML image name lookup (via os_slug)
    3. Short-ID resolution against metadata.json

    Args:
        value: Image identifier (path, os_slug, or ID prefix)

    Returns:
        Resolved path to the image file

    Raises:
        MVMError: If image cannot be resolved
    """
    images_dir = get_images_dir()
    cache_dir = get_cache_dir()

    # Direct path check
    if "/" in value or value.endswith((".ext4", ".btrfs")):
        path = Path(value)
        if path.exists():
            return path

    # YAML image name lookup (check os_slug in metadata)
    all_entries = list_image_entries(cache_dir)
    for full_key, meta in all_entries.items():
        os_slug = str(meta.get("os_slug", ""))
        if os_slug == value:
            path_str = str(meta.get("path", ""))
            if path_str:
                candidate = images_dir / path_str
                if candidate.exists():
                    return candidate
            # Try full_key with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{full_key}{ext}"
                if candidate.exists():
                    return candidate
            # Try just the value name with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{value}{ext}"
                if candidate.exists():
                    return candidate

    # ID prefix resolution
    from mvmctl.api.assets import resolve_image_id_path as _api_resolve_image_id_path

    return _api_resolve_image_id_path(value)


def resolve_kernel_multi_strategy(value: str) -> Path:
    """Resolve kernel value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/')
    2. Short-ID resolution against metadata.json

    Args:
        value: Kernel identifier (path, version, or ID prefix)

    Returns:
        Resolved path to the kernel file

    Raises:
        MVMError: If kernel cannot be resolved
    """
    from mvmctl.api.kernel import resolve_kernel_id_path as _api_resolve_kernel_id_path

    kernels_dir = get_kernels_dir()

    # Direct path check
    if "/" in value:
        path = Path(value)
        if path.exists():
            return path

    # Check if it's a direct filename in kernels dir
    candidate = kernels_dir / value
    if candidate.exists():
        return candidate

    # ID prefix resolution
    return _api_resolve_kernel_id_path(value)


def resolve_vm_selector(selector: str) -> str:
    """Resolve a VM selector (name or ID prefix) to a VM name.

    Tries ID-prefix lookup first, falls back to treating selector as name.
    Raises MVMError if the prefix is ambiguous (matches multiple VMs).

    Args:
        selector: VM name or ID prefix

    Returns:
        Resolved VM name

    Raises:
        MVMError: If ID prefix is ambiguous (matches multiple VMs)
    """
    manager = get_vm_manager()
    matches = manager.find_by_id_prefix(selector)
    if len(matches) == 1:
        return matches[0].name
    if len(matches) > 1:
        names = ", ".join(m.name for m in matches)
        raise MVMError(f"Ambiguous ID prefix '{selector}' matches {len(matches)} VMs: {names}")
    # No ID match — treat as name
    return selector
