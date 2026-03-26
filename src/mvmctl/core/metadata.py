"""Unified metadata storage for kernels, images, and binaries.

All metadata is stored in a single JSON file at {cache_dir}/metadata.json.
This module provides functions to read, write, and migrate metadata.
"""

from __future__ import annotations

import fcntl
import json
import logging
import threading
import time
from collections import OrderedDict
from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import IO, Any

from mvmctl.constants import CONST_FILE_PERMS_METADATA

logger = logging.getLogger(__name__)

_METADATA_FILENAME = "metadata.json"
_METADATA_LOCK_FILENAME = "metadata.json.lock"

# Default TTL for metadata cache in seconds
DEFAULT_CACHE_TTL = 5.0
DEFAULT_CACHE_MAX_ENTRIES = 32


@dataclass
class _CacheEntry:
    """Internal cache entry with data, mtime, and timestamp."""

    data: dict[str, Any]
    mtime: float
    timestamp: float = field(default_factory=time.time)


class MetadataCache:
    """Thread-safe LRU cache for metadata reads with TTL and mtime-based invalidation.

    The cache stores metadata entries keyed by cache directory path. Each entry
    tracks the file's modification time (mtime) to invalidate stale data.

    Attributes:
        ttl: Time-to-live in seconds for cached entries (default: 5.0)
    """

    def __init__(
        self,
        ttl: float = DEFAULT_CACHE_TTL,
        max_entries: int = DEFAULT_CACHE_MAX_ENTRIES,
    ) -> None:
        """Initialize the cache with specified TTL.

        Args:
            ttl: Time-to-live in seconds for cached entries
        """
        self._ttl = ttl
        self._max_entries = max_entries
        self._cache: OrderedDict[Path, _CacheEntry] = OrderedDict()
        self._lock = threading.RLock()

    def get(self, cache_dir: Path) -> dict[str, Any] | None:
        """Get cached metadata if valid (not expired and mtime matches).

        Args:
            cache_dir: Directory containing metadata.json

        Returns:
            Cached data dict if valid, None otherwise
        """
        with self._lock:
            entry = self._cache.get(cache_dir)
            if entry is None:
                return None

            meta_path = _metadata_path(cache_dir)
            try:
                current_mtime = meta_path.stat().st_mtime
            except OSError:
                del self._cache[cache_dir]
                return None

            if current_mtime != entry.mtime:
                del self._cache[cache_dir]
                return None

            now = time.time()
            if now - entry.timestamp <= self._ttl:
                self._cache.move_to_end(cache_dir)
                return entry.data

            entry.timestamp = now
            self._cache.move_to_end(cache_dir)
            return entry.data

    def set(self, cache_dir: Path, data: dict[str, Any]) -> None:
        """Cache metadata with current file mtime.

        Args:
            cache_dir: Directory containing metadata.json
            data: Metadata dict to cache
        """
        with self._lock:
            meta_path = _metadata_path(cache_dir)
            try:
                mtime = meta_path.stat().st_mtime
            except OSError:
                mtime = time.time()

            self._cache[cache_dir] = _CacheEntry(data=data, mtime=mtime)
            self._cache.move_to_end(cache_dir)
            while len(self._cache) > self._max_entries:
                self._cache.popitem(last=False)

    def invalidate(self, cache_dir: Path | None = None) -> None:
        """Invalidate cache entries.

        Args:
            cache_dir: Specific cache directory to invalidate, or None to clear all
        """
        with self._lock:
            if cache_dir is None:
                self._cache.clear()
            else:
                self._cache.pop(cache_dir, None)


# Global cache instance
_metadata_cache = MetadataCache()


def _metadata_path(cache_dir: Path) -> Path:
    """Return path to metadata.json in cache_dir."""
    return cache_dir / _METADATA_FILENAME


def _lock_path(cache_dir: Path) -> Path:
    """Return path to metadata.json.lock in cache_dir."""
    return cache_dir / _METADATA_LOCK_FILENAME


@contextmanager
def _locked_metadata(cache_dir: Path, exclusive: bool = True) -> Generator[None, None, None]:
    """Context manager for file locking metadata operations.

    Uses a separate .lock file (not metadata.json itself) to avoid
    interfering with atomic writes.

    Args:
        cache_dir: Directory containing metadata.json
        exclusive: If True, use LOCK_EX for writes; if False, use LOCK_SH for reads
    """
    lock_file_path = _lock_path(cache_dir)
    lock_file_path.parent.mkdir(parents=True, exist_ok=True)
    f: IO[str] = open(lock_file_path, "a+")
    try:
        fcntl.flock(f, fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)
        yield None
    finally:
        fcntl.flock(f, fcntl.LOCK_UN)
        f.close()


def read_metadata(cache_dir: Path) -> dict[str, Any]:
    """Read metadata.json; return {} if not found or invalid JSON.

    Uses shared file locking (LOCK_SH) for concurrent read safety.
    Results are cached with TTL to reduce I/O for repeated reads.
    """
    cached = _metadata_cache.get(cache_dir)
    if cached is not None:
        return cached

    with _locked_metadata(cache_dir, exclusive=False):
        path = _metadata_path(cache_dir)
        if not path.exists():
            return {}
        try:
            data: dict[str, Any] = json.loads(path.read_text())
            if not isinstance(data, dict):
                return {}
        except (json.JSONDecodeError, OSError):
            logger.warning("Corrupt metadata at %s — returning empty state", path)
            return {}

    _metadata_cache.set(cache_dir, data)
    return data


def write_metadata(cache_dir: Path, data: dict[str, Any]) -> None:
    """Write metadata.json atomically (chmod 0o600).

    Uses exclusive file locking (LOCK_EX) to prevent concurrent write corruption.
    Invalidates the read cache after writing.
    """
    with _locked_metadata(cache_dir, exclusive=True):
        path = _metadata_path(cache_dir)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data, indent=2))
        path.chmod(CONST_FILE_PERMS_METADATA)
        # Invalidate cache since file has been modified
        _metadata_cache.invalidate(cache_dir)


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


def list_kernel_entries(
    cache_dir: Path, kernels_dir: Path | None = None
) -> dict[str, dict[str, Any]]:
    """Return all kernel entries dict keyed by filename.

    Validates that entries correspond to actual files and removes orphaned entries.

    Args:
        cache_dir: Directory containing metadata.json
        kernels_dir: Optional directory to validate kernel files exist
    """
    data = read_metadata(cache_dir)
    kernels = data.get("kernels", {})
    if not isinstance(kernels, dict):
        return {}

    # Validate entries against actual files
    if kernels_dir is not None and kernels_dir.exists():
        valid_kernels: dict[str, dict[str, Any]] = {}
        orphaned: list[str] = []

        for kernel_id, kernel_data in kernels.items():
            if not isinstance(kernel_data, dict):
                orphaned.append(kernel_id)
                continue
            filename = kernel_data.get("filename", kernel_id)
            kernel_path = kernels_dir / str(filename)
            if kernel_path.exists():
                valid_kernels[kernel_id] = dict(kernel_data)
            else:
                orphaned.append(kernel_id)

        if orphaned:
            logger.debug("Removing %d orphaned kernel entries: %s", len(orphaned), orphaned)
            for kernel_id in orphaned:
                del data["kernels"][kernel_id]
            write_metadata(cache_dir, data)

        return valid_kernels

    return {k: dict(v) for k, v in kernels.items() if isinstance(v, dict)}


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


def list_image_entries(
    cache_dir: Path, images_dir: Path | None = None
) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID.

    Validates that entries correspond to actual files and removes orphaned entries.

    Args:
        cache_dir: Directory containing metadata.json
        images_dir: Optional directory to validate image files exist
    """
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if not isinstance(images, dict):
        return {}

    # Validate entries against actual files
    if images_dir is not None and images_dir.exists():
        valid_images: dict[str, dict[str, Any]] = {}
        orphaned: list[str] = []

        for image_id, image_data in images.items():
            if isinstance(image_data, dict):
                filename = image_data.get("filename", f"{image_id}.ext4")
                image_path = images_dir / filename
                if image_path.exists():
                    valid_images[image_id] = dict(image_data)
                else:
                    orphaned.append(image_id)

        # Remove orphaned entries
        if orphaned:
            logger.debug("Removing %d orphaned image entries: %s", len(orphaned), orphaned)
            for image_id in orphaned:
                del data["images"][image_id]
            write_metadata(cache_dir, data)

        return valid_images

    return {k: dict(v) for k, v in images.items() if isinstance(v, dict)}


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
                            from mvmctl.core.config_state import set_defaults_value

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
