"""Unified metadata storage for kernels, images, and binaries.

All metadata is stored in a single JSON file at {cache_dir}/metadata.json.
This module provides functions to read, write, and migrate metadata.
"""

from __future__ import annotations

import json
import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

_METADATA_FILENAME = "metadata.json"


def _metadata_path(cache_dir: Path) -> Path:
    """Return path to metadata.json in cache_dir."""
    return cache_dir / _METADATA_FILENAME


def read_metadata(cache_dir: Path) -> dict[str, Any]:
    """Read metadata.json; return {} if not found or invalid JSON."""
    path = _metadata_path(cache_dir)
    if not path.exists():
        return {}
    try:
        data: dict[str, Any] = json.loads(path.read_text())
        if not isinstance(data, dict):
            return {}
        return data
    except (json.JSONDecodeError, OSError):
        logger.warning("Corrupt metadata at %s — returning empty state", path)
        return {}


def write_metadata(cache_dir: Path, data: dict[str, Any]) -> None:
    """Write metadata.json atomically (chmod 0o600)."""
    path = _metadata_path(cache_dir)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2))
    path.chmod(0o600)


def _now_utc() -> str:
    """Return current UTC timestamp as ISO format string."""
    return datetime.now(tz=timezone.utc).isoformat()


# =============================================================================
# Kernel metadata
# =============================================================================


def update_kernel_entry(cache_dir: Path, kernel_name: str, **fields: Any) -> None:
    """Upsert kernel entry in metadata.json kernels section."""
    data = read_metadata(cache_dir)
    if "kernels" not in data or not isinstance(data.get("kernels"), dict):
        data["kernels"] = {}

    kernel_data: dict[str, Any] = data["kernels"].get(kernel_name, {})
    kernel_data.update(fields)
    data["kernels"][kernel_name] = kernel_data
    write_metadata(cache_dir, data)


def get_kernel_entry(cache_dir: Path, kernel_name: str) -> dict[str, Any]:
    """Return kernel metadata entry or {} if not found."""
    data = read_metadata(cache_dir)
    kernels = data.get("kernels", {})
    if isinstance(kernels, dict):
        return dict(kernels.get(kernel_name, {}))
    return {}


def list_kernel_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all kernel entries dict keyed by filename."""
    data = read_metadata(cache_dir)
    kernels = data.get("kernels", {})
    if isinstance(kernels, dict):
        return {k: dict(v) for k, v in kernels.items() if isinstance(v, dict)}
    return {}


def remove_kernel_entry(cache_dir: Path, kernel_name: str) -> None:
    """Remove a kernel entry from metadata.json."""
    data = read_metadata(cache_dir)
    if "kernels" in data and isinstance(data["kernels"], dict):
        if kernel_name in data["kernels"]:
            del data["kernels"][kernel_name]
            write_metadata(cache_dir, data)


# =============================================================================
# Image metadata
# =============================================================================


def update_image_entry(cache_dir: Path, image_id: str, **fields: Any) -> None:
    """Upsert image entry in metadata.json images section."""
    data = read_metadata(cache_dir)
    if "images" not in data or not isinstance(data.get("images"), dict):
        data["images"] = {}

    image_data: dict[str, Any] = data["images"].get(image_id, {})
    image_data.update(fields)
    data["images"][image_id] = image_data
    write_metadata(cache_dir, data)


def get_image_entry(cache_dir: Path, image_id: str) -> dict[str, Any]:
    """Return image metadata entry or {} if not found."""
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if isinstance(images, dict):
        return dict(images.get(image_id, {}))
    return {}


def list_image_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID."""
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if isinstance(images, dict):
        return {k: dict(v) for k, v in images.items() if isinstance(v, dict)}
    return {}


def remove_image_entry(cache_dir: Path, image_id: str) -> None:
    """Remove an image entry from metadata.json."""
    data = read_metadata(cache_dir)
    if "images" in data and isinstance(data["images"], dict):
        if image_id in data["images"]:
            del data["images"][image_id]
            write_metadata(cache_dir, data)


def find_image_by_short_id(cache_dir: Path, short_id: str) -> tuple[str, dict[str, Any]] | None:
    """Find an image entry whose key starts with short_id. Returns (full_key, meta) or None."""
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if not isinstance(images, dict):
        return None
    matches = [(k, v) for k, v in images.items() if k.startswith(short_id) and isinstance(v, dict)]
    if len(matches) == 1:
        return matches[0]
    return None


def find_images_by_short_id(cache_dir: Path, short_id: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all image entries whose key starts with short_id."""
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if not isinstance(images, dict):
        return []
    return [(k, v) for k, v in images.items() if k.startswith(short_id) and isinstance(v, dict)]


# =============================================================================
# Binary metadata
# =============================================================================


def update_binary_entry(cache_dir: Path, version: str, **fields: Any) -> None:
    """Upsert binary entry in metadata.json binaries section."""
    data = read_metadata(cache_dir)
    if "binaries" not in data or not isinstance(data.get("binaries"), dict):
        data["binaries"] = {}

    binary_data: dict[str, Any] = data["binaries"].get(version, {})
    binary_data.update(fields)
    data["binaries"][version] = binary_data
    write_metadata(cache_dir, data)


def get_binary_entry(cache_dir: Path, version: str) -> dict[str, Any]:
    """Return binary metadata entry or {} if not found."""
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    if isinstance(binaries, dict):
        return dict(binaries.get(version, {}))
    return {}


def list_binary_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all binary entries dict keyed by version."""
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    if isinstance(binaries, dict):
        return {k: dict(v) for k, v in binaries.items() if isinstance(v, dict)}
    return {}


# =============================================================================
# Migration from legacy per-file JSON
# =============================================================================


def migrate_legacy_metadata(cache_dir: Path, kernels_dir: Path, images_dir: Path) -> None:
    """One-time migration: read existing per-file JSON into metadata.json.

    Reads:
    - kernels_dir/{kernel_name}.json files -> kernels section
    - images_dir/{image_id}*.json files -> images section
    After migrating, removes the individual .json sidecar files.
    Skips if metadata.json already has data in kernels or images sections.
    """
    data = read_metadata(cache_dir)

    # Skip if already has kernel or image data
    existing_kernels = data.get("kernels", {})
    existing_images = data.get("images", {})
    if (isinstance(existing_kernels, dict) and existing_kernels) or (
        isinstance(existing_images, dict) and existing_images
    ):
        logger.debug("Metadata already populated, skipping migration")
        return

    changed = False

    # Migrate kernel metadata files
    if kernels_dir.exists():
        for meta_path in kernels_dir.glob("*.json"):
            if meta_path.name == "default.json":
                try:
                    legacy_data: dict[str, Any] = json.loads(meta_path.read_text())
                    legacy_name = legacy_data.get("name")
                    if legacy_name:
                        try:
                            from fcm.core.config_state import set_defaults_value

                            set_defaults_value("kernel", legacy_name)
                            logger.info("Migrated default kernel: %s", legacy_name)
                        except Exception:
                            pass
                    meta_path.unlink()
                except (json.JSONDecodeError, OSError):
                    pass
                continue
            try:
                kernel_data: dict[str, Any] = json.loads(meta_path.read_text())
                if isinstance(kernel_data, dict) and "name" in kernel_data:
                    kernel_name = kernel_data["name"]
                    if "kernels" not in data:
                        data["kernels"] = {}
                    data["kernels"][kernel_name] = kernel_data
                    changed = True
                    # Remove the legacy file after migration
                    meta_path.unlink()
                    logger.debug("Migrated kernel metadata: %s", kernel_name)
            except (json.JSONDecodeError, OSError) as e:
                logger.warning("Failed to migrate %s: %s", meta_path, e)

    # Migrate image metadata files
    if images_dir.exists():
        for meta_path in images_dir.glob("*.json"):
            try:
                image_data: dict[str, Any] = json.loads(meta_path.read_text())
                if isinstance(image_data, dict):
                    # Determine image ID from filename (remove .ext4.json, etc.)
                    image_id = meta_path.stem
                    # Strip known extensions to get base image ID
                    for ext in (".ext4", ".btrfs", ".img", ".raw"):
                        if image_id.endswith(ext):
                            image_id = image_id[: -len(ext)]
                            break
                    if "images" not in data:
                        data["images"] = {}
                    data["images"][image_id] = image_data
                    changed = True
                    # Remove the legacy file after migration
                    meta_path.unlink()
                    logger.debug("Migrated image metadata: %s", image_id)
            except (json.JSONDecodeError, OSError) as e:
                logger.warning("Failed to migrate %s: %s", meta_path, e)

    if changed:
        write_metadata(cache_dir, data)
        logger.info("Migrated legacy metadata to %s", _metadata_path(cache_dir))
