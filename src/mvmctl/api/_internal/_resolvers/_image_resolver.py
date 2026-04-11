"""Image resolution helpers."""

from __future__ import annotations

import hashlib
from pathlib import Path

__all__ = [
    "resolve_image_hash",
    "resolve_image_multi_strategy",
]


def resolve_image_hash(image_path: Path, image_hash: str | None) -> str | None:
    """Resolve image hash from path.

    Args:
        image_path: Path to image file
        image_hash: Optional pre-computed hash

    Returns:
        Image hash or None if file doesn't exist
    """
    if image_hash:
        return image_hash

    if image_path and image_path.exists():
        return hashlib.sha256(image_path.read_bytes()).hexdigest()

    return None


def resolve_image_multi_strategy(value: str) -> Path:
    """Resolve image value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/' or ends with .ext4/.btrfs)
    2. YAML image name lookup (via os_slug)
    3. Short-ID resolution against metadata.json
    """
    from mvmctl.core.metadata import list_image_entries
    from mvmctl.utils.fs import get_cache_dir, get_images_dir

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
