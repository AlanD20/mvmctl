"""Image resolution helpers."""

from __future__ import annotations

import hashlib
from pathlib import Path

__all__ = [
    "resolve_image_hash",
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
