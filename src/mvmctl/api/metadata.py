"""Metadata API — thin wrappers for core.metadata functions.

This module provides thin API wrappers around core.metadata functions,
exposing them to the CLI layer while maintaining the architecture rule:
CLI → API → Core.

Note: find_kernels_by_short_id does not exist in core.metadata; kernels are
looked up by their full name, not by short ID prefix.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from mvmctl.core.metadata import find_images_by_short_id as _find_images_by_short_id
from mvmctl.core.metadata import get_image_entry as _get_image_entry
from mvmctl.core.metadata import list_image_entries as _list_image_entries
from mvmctl.core.metadata import list_kernel_entries as _list_kernel_entries
from mvmctl.core.metadata import remove_image_entry as _remove_image_entry
from mvmctl.core.metadata import update_image_entry as _update_image_entry

__all__ = [
    "list_image_entries",
    "get_image_entry",
    "find_images_by_short_id",
    "find_kernels_by_short_id",
    "remove_image_entry",
    "update_image_entry",
]


def list_image_entries(
    cache_dir: Path, images_dir: Path | None = None
) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID.

    Validates that entries correspond to actual files and removes orphaned entries.

    Args:
        cache_dir: Directory containing metadata.json
        images_dir: Optional directory to validate image files exist

    Returns:
        Dictionary mapping image IDs to their metadata
    """
    return _list_image_entries(cache_dir, images_dir)


def get_image_entry(cache_dir: Path, image_id: str) -> dict[str, Any]:
    """Return image metadata entry or {} if not found.

    Args:
        cache_dir: Directory containing metadata.json
        image_id: The full image ID (64-char hash)

    Returns:
        Image metadata dictionary or empty dict if not found
    """
    return _get_image_entry(cache_dir, image_id)


def find_images_by_short_id(cache_dir: Path, short_id: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all image entries whose key starts with short_id.

    Args:
        cache_dir: Directory containing metadata.json
        short_id: Short ID prefix to search for

    Returns:
        List of (full_image_id, metadata) tuples matching the prefix
    """
    return _find_images_by_short_id(cache_dir, short_id)


def find_kernels_by_short_id(cache_dir: Path, short_id: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all kernel entries whose key starts with short_id.

    Note: This is a placeholder implementation. Kernels in mvmctl are stored
    by their full filename, not by hash-based short IDs like images.
    This function searches kernel names by prefix for API consistency.

    Args:
        cache_dir: Directory containing metadata.json
        short_id: Short ID prefix to search for

    Returns:
        List of (kernel_name, metadata) tuples matching the prefix
    """
    all_kernels = _list_kernel_entries(cache_dir)
    return [(name, data) for name, data in all_kernels.items() if name.startswith(short_id)]


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
