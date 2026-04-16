"""Metadata API — thin wrappers for core.metadata functions.

This module provides thin API wrappers around core.metadata functions,
exposing them to the CLI layer while maintaining the architecture rule:
CLI → API → Core.

Note: find_kernels_by_id_prefix does not exist in core.metadata; kernels are
looked up by their full name, not by ID prefix.
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING, Any

from mvmctl.core.metadata import find_images_by_id_prefix as _find_images_by_id_prefix
from mvmctl.core.metadata import get_image_entry as _get_image_entry
from mvmctl.core.metadata import list_binary_entries as _list_binary_entries
from mvmctl.core.metadata import list_image_entries as _list_image_entries
from mvmctl.core.metadata import list_kernel_entries as _list_kernel_entries
from mvmctl.core.metadata import remove_image_entry as _remove_image_entry
from mvmctl.core.metadata import remove_kernel_entry as _remove_kernel_entry
from mvmctl.core.metadata import set_default_binary_entry as _set_default_binary_entry
from mvmctl.core.metadata import set_default_image_by_os_slug as _set_default_image_by_os_slug
from mvmctl.core.metadata import set_default_image_entry as _set_default_image_entry
from mvmctl.core.metadata import update_image_entry as _update_image_entry
from mvmctl.models.binary import BinaryItem
from mvmctl.models.image import ImageItem
from mvmctl.models.kernel import KernelItem
from mvmctl.models.network import NetworkItem

if TYPE_CHECKING:
    from mvmctl.core.mvm_db import MVMDatabase


def _create_db() -> "MVMDatabase":
    """Create fresh MVMDatabase instance to respect environment changes."""
    from mvmctl.core.mvm_db import MVMDatabase

    return MVMDatabase()


__all__ = [
    "list_image_entries",
    "list_binary_entries",
    "get_image_entry",
    "find_images_by_id_prefix",
    "find_kernels_by_id_prefix",
    "get_default_binary_entry",
    "get_default_image_entry",
    "get_default_kernel_entry",
    "get_default_network_entry",
    "remove_image_entry",
    "remove_kernel_entry",
    "set_default_binary_entry",
    "set_default_image_entry",
    "set_default_image_by_os_slug",
    "update_image_entry",
]


def list_image_entries(
    cache_dir: Path, images_dir: Path | None = None, include_missing: bool = False
) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID.

    Validates that entries correspond to actual files and removes orphaned entries.

    Args:
        cache_dir: Directory containing metadata.json
        images_dir: Optional directory to validate image files exist
        include_missing: If True, include entries even if file is missing (for X mark display)

    Returns:
        Dictionary mapping image IDs to their metadata
    """
    return _list_image_entries(cache_dir, images_dir, include_missing=include_missing)


def list_binary_entries(cache_dir: Path) -> dict[str, list[dict[str, Any]]]:
    """Return all binary entries dict keyed by binary name.

    Args:
        cache_dir: Directory containing metadata.json

    Returns:
        Dictionary mapping binary names to their metadata
    """
    return _list_binary_entries(cache_dir)


def get_image_entry(cache_dir: Path, image_id: str) -> dict[str, Any]:
    """Return image metadata entry or {} if not found.

    Args:
        cache_dir: Directory containing metadata.json
        image_id: The full image ID (64-char hash)

    Returns:
        Image metadata dictionary or empty dict if not found
    """
    return _get_image_entry(cache_dir, image_id)


def find_images_by_id_prefix(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all image entries whose key starts with prefix."""
    return _find_images_by_id_prefix(cache_dir, prefix)


def find_kernels_by_id_prefix(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all kernel entries whose full_id starts with prefix."""
    all_kernels = _list_kernel_entries(cache_dir)
    return [(full_id, data) for full_id, data in all_kernels.items() if full_id.startswith(prefix)]


def get_default_image_entry() -> ImageItem | None:
    """Get the default image entry from the database.

    Returns:
        ImageItem if a default image is set, None otherwise.
    """
    db = _create_db()
    image = db.get_default_image()
    if image is None:
        return None
    return ImageItem.from_db(image)


def get_default_kernel_entry(cache_dir: Path) -> KernelItem | None:
    """Get the default kernel entry from the database.

    Args:
        cache_dir: Directory containing metadata.json (unused, kept for API compatibility).

    Returns:
        KernelItem if a default kernel is set, None otherwise.
    """
    db = _create_db()
    kernel = db.get_default_kernel()
    if kernel is None:
        return None
    return KernelItem.from_db(kernel)


def remove_kernel_entry(cache_dir: Path, kernel_id: str) -> None:
    """Remove a kernel entry from metadata.json by its full hash ID."""
    return _remove_kernel_entry(cache_dir, kernel_id)


def remove_image_entry(cache_dir: Path, image_id: str) -> None:
    """Remove an image entry from metadata.json.

    Args:
        cache_dir: Directory containing metadata.json
        image_id: The full image ID to remove
    """
    return _remove_image_entry(cache_dir, image_id)


def update_image_entry(cache_dir: Path, image_id: str, **fields: Any) -> None:
    """Upsert image entry in metadata.json images section.

    Args:
        cache_dir: Directory containing metadata.json
        image_id: The image ID to update or create
        **fields: Metadata fields to set for the image
    """
    return _update_image_entry(cache_dir, image_id, **fields)


def set_default_image_entry(cache_dir: Path, image_id: str) -> None:
    _set_default_image_entry(cache_dir, image_id)


def set_default_image_by_os_slug(cache_dir: Path, os_slug: str) -> None:
    _set_default_image_by_os_slug(cache_dir, os_slug)


def get_default_binary_entry() -> BinaryItem | None:
    """Get the default binary entry from the database.

    Returns:
        BinaryItem if a default binary is set, None otherwise.
    """
    db = _create_db()
    binary = db.get_default_binary("firecracker")
    if binary is None:
        return None
    return BinaryItem.from_db(binary)


def set_default_binary_entry(cache_dir: Path, version: str) -> None:
    _set_default_binary_entry(cache_dir, version)


def get_default_network_entry(cache_dir: Path) -> NetworkItem | None:
    """Get the default network entry from the database.

    Args:
        cache_dir: Directory containing metadata.json (unused, kept for API compatibility).

    Returns:
        NetworkItem if a default network is set, None otherwise.
    """
    db = _create_db()
    network = db.get_default_network()
    if network is None:
        return None
    return NetworkItem.from_db(network)
