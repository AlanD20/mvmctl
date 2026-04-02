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
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary, Image, Kernel, Network

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
        path.write_text(json.dumps(data))
        path.chmod(CONST_FILE_PERMS_METADATA)
        # Invalidate cache since file has been modified
        _metadata_cache.invalidate(cache_dir)


def _now_utc() -> str:
    """Return current UTC timestamp as ISO format string."""
    return datetime.now(tz=timezone.utc).isoformat()


# =============================================================================
# Conversion helpers for SQLite migration (dual-write pattern)
# =============================================================================


def _dict_to_db_image(image_id: str, entry: dict[str, Any]) -> Image:
    """Convert JSON image entry to DB Image model."""
    return Image(
        id=image_id,
        os_slug=entry.get("internal_id", ""),
        path=entry.get("filename", ""),
        os_name=entry.get("os_name"),
        fs_type=entry.get("fs_type"),
        fs_uuid=entry.get("fs_uuid"),
        compressed_size=entry.get("compressed_size"),
        original_size=entry.get("original_size"),
        compression_ratio=entry.get("compression_ratio"),
        compressed_format=entry.get("compressed_format"),
        pulled_at=entry.get("pulled_at"),
        is_default=entry.get("is_default", 0) == 1,
        created_at=entry.get("created_at"),
        updated_at=entry.get("updated_at"),
    )


def _db_image_to_dict(image: Image) -> dict[str, Any]:
    """Convert DB Image model to JSON dict format."""
    return {
        "internal_id": image.os_slug,
        "filename": image.path,
        "os_name": image.os_name,
        "fs_type": image.fs_type,
        "fs_uuid": image.fs_uuid,
        "compressed_size": image.compressed_size,
        "original_size": image.original_size,
        "compression_ratio": image.compression_ratio,
        "compressed_format": image.compressed_format,
        "pulled_at": image.pulled_at,
        "is_default": 1 if image.is_default else 0,
        "created_at": image.created_at,
        "updated_at": image.updated_at,
    }


def _dict_to_db_kernel(kernel_id: str, entry: dict[str, Any]) -> Kernel:
    """Convert JSON kernel entry to DB Kernel model."""
    filename = entry.get("filename", "")
    return Kernel(
        id=kernel_id,
        name=entry.get("name", filename),
        version=entry.get("version", ""),
        arch=entry.get("arch", "x86_64"),
        path=filename,
        base_name=entry.get("base_name"),
        type=entry.get("type"),
        is_default=entry.get("is_default", 0) == 1,
        created_at=entry.get("created_at"),
        updated_at=entry.get("last_modified"),
    )


def _db_kernel_to_dict(kernel: Kernel) -> dict[str, Any]:
    """Convert DB Kernel model to JSON dict format."""
    return {
        "name": kernel.name,
        "filename": kernel.path,
        "version": kernel.version,
        "arch": kernel.arch,
        "base_name": kernel.base_name,
        "type": kernel.type,
        "is_default": 1 if kernel.is_default else 0,
        "created_at": kernel.created_at,
        "last_modified": kernel.updated_at,
    }


def _dict_to_db_binary(name: str, entry: dict[str, Any]) -> Binary | None:
    """Convert JSON binary entry to DB Binary model."""
    binary_id = entry.get("binary_id") or entry.get("id")
    if not binary_id:
        return None
    return Binary(
        id=binary_id,
        name=name,
        version=entry.get("package_version", ""),
        path=entry.get("binary_path", ""),
        full_version=entry.get("full_version"),
        ci_version=entry.get("ci_version"),
        created_at=entry.get("created_at"),
        updated_at=entry.get("updated_at"),
    )


def _db_binary_to_dict(binary: Binary) -> dict[str, Any]:
    """Convert DB Binary model to JSON dict format."""
    return {
        "binary_id": binary.id,
        "binary_name": binary.name,
        "package_version": binary.version,
        "binary_path": binary.path,
        "full_version": binary.full_version,
        "ci_version": binary.ci_version,
        "created_at": binary.created_at,
        "updated_at": binary.updated_at,
    }


def _dict_to_db_network(network_name: str, entry: dict[str, Any]) -> Network:
    """Convert JSON network entry to DB Network model."""
    return Network(
        id=entry.get("network_id", ""),
        name=network_name,
        subnet=entry.get("cidr", ""),
        bridge=entry.get("bridge", ""),
        ipv4_gateway=entry.get("gateway", ""),
        bridge_active=entry.get("bridge_active", False),
        nat_gateways=",".join(entry.get("nat_gateways", [])) if entry.get("nat_gateways") else None,
        nat_enabled=entry.get("nat_enabled", True),
        is_default=entry.get("is_default", 0) == 1,
        created_at=entry.get("created_at"),
        updated_at=entry.get("updated_at"),
    )


def _db_network_to_dict(network: Network) -> dict[str, Any]:
    """Convert DB Network model to JSON dict format."""
    return {
        "network_id": network.id,
        "cidr": network.subnet,
        "bridge": network.bridge,
        "gateway": network.ipv4_gateway,
        "bridge_active": network.bridge_active,
        "nat_gateways": network.nat_gateways.split(",") if network.nat_gateways else [],
        "nat_enabled": network.nat_enabled,
        "is_default": 1 if network.is_default else 0,
        "created_at": network.created_at,
        "updated_at": network.updated_at,
    }


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

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        db_kernel = _dict_to_db_kernel(kernel_name, kernel_data)
        db.upsert_kernel(db_kernel)
    except Exception:
        pass


def _flag_as_default(value: Any) -> int:
    if isinstance(value, bool):
        return 1 if value else 0
    if isinstance(value, int):
        return 1 if value == 1 else 0
    if isinstance(value, str):
        lowered = value.strip().lower()
        return 1 if lowered in {"1", "true", "yes"} else 0
    return 0


def _set_default_entry(cache_dir: Path, section: str, target_key: str) -> None:
    data = read_metadata(cache_dir)
    section_data = data.get(section, {})
    if not isinstance(section_data, dict):
        raise KeyError(f"Metadata section '{section}' not found")
    if target_key not in section_data or not isinstance(section_data[target_key], dict):
        raise KeyError(f"Entry '{target_key}' not found in metadata section '{section}'")

    for key, entry in section_data.items():
        if not isinstance(entry, dict):
            continue
        entry["is_default"] = 1 if key == target_key else 0

    write_metadata(cache_dir, data)


def _find_default_entry(cache_dir: Path, section: str) -> tuple[str, dict[str, Any]] | None:
    data = read_metadata(cache_dir)
    section_data = data.get(section, {})
    if not isinstance(section_data, dict):
        return None
    for key, entry in section_data.items():
        if isinstance(entry, dict) and _flag_as_default(entry.get("is_default")) == 1:
            return key, dict(entry)
    return None


def set_default_kernel_entry(cache_dir: Path, kernel_id: str) -> None:
    _set_default_entry(cache_dir, "kernels", kernel_id)

    # NEW: Also update SQLite
    try:
        db = MVMDatabase()
        db.set_default_kernel(kernel_id)
    except Exception:
        pass


def set_default_kernel_by_filename(cache_dir: Path, filename: str) -> None:
    kernels = list_kernel_entries(cache_dir)
    for kernel_id, entry in kernels.items():
        if str(entry.get("filename", kernel_id)) == filename:
            set_default_kernel_entry(cache_dir, kernel_id)
            return
    raise KeyError(f"Kernel filename '{filename}' not found in metadata")


def get_default_kernel_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    # Try SQLite first
    try:
        db = MVMDatabase()
        kernels = db.list_kernels()
        for kernel in kernels:
            if kernel.is_default:
                return kernel.id, _db_kernel_to_dict(kernel)
    except Exception:
        pass

    # Fall back to JSON
    return _find_default_entry(cache_dir, "kernels")


def get_kernel_entry(cache_dir: Path, kernel_name: str) -> dict[str, Any]:
    """Return kernel metadata entry or {} if not found."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        kernel = db.get_kernel(kernel_name)
        if kernel:
            return _db_kernel_to_dict(kernel)
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    kernels = data.get("kernels", {})
    if isinstance(kernels, dict):
        return dict(kernels.get(kernel_name, {}))
    return {}


def list_kernel_entries(
    cache_dir: Path, kernels_dir: Path | None = None, include_missing: bool = False
) -> dict[str, dict[str, Any]]:
    """Return all kernel entries dict keyed by filename.

    Validates that entries correspond to actual files and removes orphaned entries
    unless include_missing is True.

    Args:
        cache_dir: Directory containing metadata.json
        kernels_dir: Optional directory to validate kernel files exist
        include_missing: If True, include entries even if file is missing (for X mark display)
    """
    # Try SQLite first
    try:
        db = MVMDatabase()
        kernels = db.list_kernels()
        if kernels:
            result: dict[str, dict[str, Any]] = {}
            for kernel in kernels:
                if kernels_dir is not None and kernels_dir.exists():
                    kernel_path = kernels_dir / kernel.path
                    if kernel_path.exists() or include_missing:
                        result[kernel.id] = _db_kernel_to_dict(kernel)
                else:
                    result[kernel.id] = _db_kernel_to_dict(kernel)
            if result:
                return result
    except Exception:
        pass

    # Fall back to JSON
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
            elif include_missing:
                # Include missing files for X mark display in CLI
                valid_kernels[kernel_id] = dict(kernel_data)
            else:
                orphaned.append(kernel_id)

        # Only clean up orphaned entries if we're not including missing
        if orphaned and not include_missing:
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

    # NEW: Also delete from SQLite
    try:
        db = MVMDatabase()
        db.delete_kernel(kernel_name)
    except Exception:
        pass


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

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        db_image = _dict_to_db_image(image_id, image_data)
        db.upsert_image(db_image)
    except Exception:
        pass


def get_image_entry(cache_dir: Path, image_id: str) -> dict[str, Any]:
    """Return image metadata entry or {} if not found."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        image = db.get_image(image_id)
        if image:
            return _db_image_to_dict(image)
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if isinstance(images, dict):
        return dict(images.get(image_id, {}))
    return {}


def list_image_entries(
    cache_dir: Path, images_dir: Path | None = None, include_missing: bool = False
) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID.

    Validates that entries correspond to actual files and removes orphaned entries
    unless include_missing is True.

    Args:
        cache_dir: Directory containing metadata.json
        images_dir: Optional directory to validate image files exist
        include_missing: If True, include entries even if file is missing (for X mark display)
    """
    # Try SQLite first
    try:
        db = MVMDatabase()
        images = db.list_images()
        if images:
            result: dict[str, dict[str, Any]] = {}
            for image in images:
                if images_dir is not None and images_dir.exists():
                    filename = image.path
                    image_path = images_dir / filename
                    if image_path.exists() or include_missing:
                        result[image.id] = _db_image_to_dict(image)
                else:
                    result[image.id] = _db_image_to_dict(image)
            if result:
                return result
    except Exception:
        pass

    # Fall back to JSON
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
                elif include_missing:
                    # Include missing files for X mark display in CLI
                    valid_images[image_id] = dict(image_data)
                else:
                    orphaned.append(image_id)

        # Only clean up orphaned entries if we're not including missing
        if orphaned and not include_missing:
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

    # NEW: Also delete from SQLite
    try:
        db = MVMDatabase()
        db.delete_image(image_id)
    except Exception:
        pass


def set_default_image_entry(cache_dir: Path, image_id: str) -> None:
    _set_default_entry(cache_dir, "images", image_id)

    # NEW: Also update SQLite
    try:
        db = MVMDatabase()
        db.set_default_image(image_id)
    except Exception:
        pass


def set_default_image_by_internal_id(cache_dir: Path, internal_id: str) -> None:
    images = list_image_entries(cache_dir)
    for image_id, entry in images.items():
        if str(entry.get("internal_id", "")) == internal_id:
            set_default_image_entry(cache_dir, image_id)
            return
    raise KeyError(f"Image internal_id '{internal_id}' not found in metadata")


def get_default_image_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    # Try SQLite first
    try:
        db = MVMDatabase()
        images = db.list_images()
        for image in images:
            if image.is_default:
                return image.id, _db_image_to_dict(image)
    except Exception:
        pass

    # Fall back to JSON
    return _find_default_entry(cache_dir, "images")


def find_image_by_id_prefix(cache_dir: Path, prefix: str) -> tuple[str, dict[str, Any]] | None:
    """Find an image entry whose key starts with prefix. Returns (full_key, meta) or None."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        images = db.find_images_by_prefix(prefix)
        if len(images) == 1:
            return images[0].id, _db_image_to_dict(images[0])
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if not isinstance(images, dict):
        return None
    matches = [(k, v) for k, v in images.items() if k.startswith(prefix) and isinstance(v, dict)]
    if len(matches) == 1:
        return matches[0]
    return None


def find_images_by_id_prefix(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all image entries whose key starts with prefix."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        images = db.find_images_by_prefix(prefix)
        if images:
            return [(img.id, _db_image_to_dict(img)) for img in images]
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    images = data.get("images", {})
    if not isinstance(images, dict):
        return []
    return [(k, v) for k, v in images.items() if k.startswith(prefix) and isinstance(v, dict)]


# =============================================================================
# Binary metadata
# =============================================================================


_BINARY_METADATA_NAMES = ("firecracker", "jailer")


def _normalized_package_version(value: str) -> str:
    return value.removeprefix("v")


def _derive_ci_version(version: str) -> str:
    parts = version.split(".")
    return f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{version}"


def _binary_entry_version(entry: dict[str, Any]) -> str:
    package_version = entry.get("package_version")
    if isinstance(package_version, str) and package_version:
        return _normalized_package_version(package_version)
    full_version = entry.get("full_version")
    if isinstance(full_version, str) and full_version:
        return _normalized_package_version(full_version)
    return ""


def _binary_matches_version(entry: dict[str, Any], version: str) -> bool:
    normalized = _normalized_package_version(version)
    if not normalized:
        return False
    entry_version = _binary_entry_version(entry)
    return entry_version == normalized


def _derive_peer_binary_path(path_value: str, target_binary_name: str) -> str:
    path = Path(path_value)
    if path.name in {"firecracker", "jailer"}:
        return str(path.with_name(target_binary_name))
    if path.name.startswith("firecracker") and target_binary_name == "jailer":
        return str(path.with_name(path.name.replace("firecracker", "jailer", 1)))
    if path.name.startswith("jailer") and target_binary_name == "firecracker":
        return str(path.with_name(path.name.replace("jailer", "firecracker", 1)))
    return str(path.with_name(target_binary_name))


def update_binary_entry(cache_dir: Path, version: str, **fields: Any) -> None:
    """Update binary entry in metadata with new structure.

    New structure:
    - binaries>defaults>firecracker: {binary_path, full_version}
    - binaries>defaults>jailer: {binary_path, full_version}
    - binaries>firecracker: {binary_name, binary_path, full_version, ci_version, is_default}
    - binaries>jailer: {binary_name, binary_path, full_version, ci_version, is_default}

    Removed fields from individual entries:
    - jailer_path (was never actually stored in firecracker entry)
    - active_binary_path
    - default_binary_path
    """
    normalized_version = _normalized_package_version(version)
    data = read_metadata(cache_dir)
    if "binaries" not in data or not isinstance(data.get("binaries"), dict):
        data["binaries"] = {}

    payload = dict(fields)
    payload["package_version"] = normalized_version
    payload["full_version"] = str(payload.get("full_version") or f"v{normalized_version}")
    payload["ci_version"] = str(payload.get("ci_version") or _derive_ci_version(normalized_version))

    # Extract paths for individual binary entries
    firecracker_path = payload.get("firecracker_path")
    jailer_path = payload.get("jailer_path")

    # Extract paths for defaults section
    default_binary_path = payload.get("default_binary_path")
    default_jailer_path = payload.get("default_jailer_path")

    # Derive jailer default path from binary default path if not provided
    if not isinstance(default_jailer_path, str) and isinstance(default_binary_path, str):
        default_jailer_path = _derive_peer_binary_path(default_binary_path, "jailer")

    # Build shared payload excluding path-related and defaults fields
    shared_payload = {
        k: v
        for k, v in payload.items()
        if k
        not in {
            "firecracker_path",
            "jailer_path",
            "default_binary_path",
            "default_jailer_path",
            "binary_name",
            "binary_path",
        }
    }

    # Build per-binary payloads (individual entries - no default/active paths)
    per_binary_payloads: dict[str, dict[str, Any]] = {
        "firecracker": {
            **shared_payload,
            "binary_name": "firecracker",
            "binary_path": firecracker_path,
        },
        "jailer": {
            **shared_payload,
            "binary_name": "jailer",
            "binary_path": jailer_path,
        },
    }

    # Update individual binary entries
    for binary_name, binary_payload in per_binary_payloads.items():
        existing = data["binaries"].get(binary_name, {})
        binary_data = dict(existing) if isinstance(existing, dict) else {}
        binary_data.update({k: v for k, v in binary_payload.items() if v is not None})
        data["binaries"][binary_name] = binary_data

    # Update defaults section with default paths and versions
    full_version = payload.get("full_version")
    if isinstance(default_binary_path, str) or isinstance(default_jailer_path, str):
        if "defaults" not in data["binaries"] or not isinstance(
            data["binaries"].get("defaults"), dict
        ):
            data["binaries"]["defaults"] = {}

        defaults = data["binaries"]["defaults"]

        # Firecracker default entry
        if isinstance(default_binary_path, str):
            if "firecracker" not in defaults or not isinstance(defaults.get("firecracker"), dict):
                defaults["firecracker"] = {}
            defaults["firecracker"]["binary_path"] = default_binary_path
            if isinstance(full_version, str):
                defaults["firecracker"]["full_version"] = full_version

        # Jailer default entry
        if isinstance(default_jailer_path, str):
            if "jailer" not in defaults or not isinstance(defaults.get("jailer"), dict):
                defaults["jailer"] = {}
            defaults["jailer"]["binary_path"] = default_jailer_path
            if isinstance(full_version, str):
                defaults["jailer"]["full_version"] = full_version

    write_metadata(cache_dir, data)

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        for binary_name in _BINARY_METADATA_NAMES:
            entry = data["binaries"].get(binary_name, {})
            if isinstance(entry, dict):
                db_binary = _dict_to_db_binary(binary_name, entry)
                if db_binary:
                    db.upsert_binary(db_binary)
    except Exception:
        pass


def get_binary_entry(cache_dir: Path, version: str) -> dict[str, Any]:
    # Try SQLite first
    try:
        db = MVMDatabase()
        for binary_name in _BINARY_METADATA_NAMES:
            binary = db.get_binary(binary_name)
            if binary and _binary_matches_version(_db_binary_to_dict(binary), version):
                return _db_binary_to_dict(binary)
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    if not isinstance(binaries, dict):
        return {}

    if version in _BINARY_METADATA_NAMES:
        named = binaries.get(version, {})
        return dict(named) if isinstance(named, dict) else {}

    for binary_name in _BINARY_METADATA_NAMES:
        candidate = binaries.get(binary_name, {})
        if isinstance(candidate, dict) and _binary_matches_version(candidate, version):
            return dict(candidate)

    return {}


def list_binary_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    # Try SQLite first
    try:
        db = MVMDatabase()
        binaries = db.list_binaries()
        if binaries:
            return {binary.name: _db_binary_to_dict(binary) for binary in binaries}
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    if isinstance(binaries, dict):
        return {
            name: dict(entry)
            for name in _BINARY_METADATA_NAMES
            if isinstance((entry := binaries.get(name)), dict)
        }
    return {}


def set_default_binary_entry(cache_dir: Path, version: str) -> None:
    data = read_metadata(cache_dir)
    binaries = data.get("binaries", {})
    if not isinstance(binaries, dict):
        raise KeyError("Metadata section 'binaries' not found")

    has_match = False
    for binary_name in _BINARY_METADATA_NAMES:
        entry = binaries.get(binary_name)
        if isinstance(entry, dict) and _binary_matches_version(entry, version):
            has_match = True
            break

    if not has_match:
        raise KeyError(f"Binary version '{version}' not found in metadata")

    changed = False
    for binary_name in _BINARY_METADATA_NAMES:
        entry = binaries.get(binary_name)
        if not isinstance(entry, dict):
            continue
        entry["is_default"] = 1
        changed = True

    if not changed:
        raise KeyError("No binary entries found in metadata")

    write_metadata(cache_dir, data)

    # NEW: Also update SQLite
    try:
        db = MVMDatabase()
        for binary_name in _BINARY_METADATA_NAMES:
            entry = binaries.get(binary_name, {})
            if isinstance(entry, dict):
                binary_path = entry.get("binary_path", "")
                full_version = entry.get("full_version", version)
                db.set_default_binary(binary_name, full_version, binary_path)
    except Exception:
        pass


def get_default_binary_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    # Try SQLite first
    try:
        db = MVMDatabase()
        db_binaries = db.list_binaries()
        for binary in db_binaries:
            # Check if this is the default via binary_defaults table
            default = db.get_binary_default(binary.name)
            if default and default.version == binary.version:
                return binary.version, _db_binary_to_dict(binary)
    except Exception:
        pass

    # Fall back to JSON
    json_binaries = list_binary_entries(cache_dir)

    firecracker = json_binaries.get("firecracker")
    if isinstance(firecracker, dict) and _flag_as_default(firecracker.get("is_default")) == 1:
        version = _binary_entry_version(firecracker)
        return version or "firecracker", firecracker

    jailer = json_binaries.get("jailer")
    if isinstance(jailer, dict) and _flag_as_default(jailer.get("is_default")) == 1:
        version = _binary_entry_version(jailer)
        return version or "jailer", jailer

    return None


# =============================================================================
# Network metadata
# =============================================================================


def update_network_entry(cache_dir: Path, network_name: str, **fields: Any) -> None:
    """Upsert network entry in metadata.json networks section."""
    data = read_metadata(cache_dir)
    if "networks" not in data or not isinstance(data.get("networks"), dict):
        data["networks"] = {}

    network_data: dict[str, Any] = data["networks"].get(network_name, {})
    network_data.update(fields)
    data["networks"][network_name] = network_data
    write_metadata(cache_dir, data)

    # NEW: Also write to SQLite
    try:
        db = MVMDatabase()
        db_network = _dict_to_db_network(network_name, network_data)
        db.upsert_network(db_network)
    except Exception:
        pass


def get_network_entry(cache_dir: Path, network_name: str) -> dict[str, Any]:
    """Return network metadata entry or {} if not found."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(network_name)
        if network:
            return _db_network_to_dict(network)
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    networks = data.get("networks", {})
    if isinstance(networks, dict):
        return dict(networks.get(network_name, {}))
    return {}


def list_network_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all network entries dict keyed by network name."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        networks = db.list_networks()
        if networks:
            return {network.name: _db_network_to_dict(network) for network in networks}
    except Exception:
        pass

    # Fall back to JSON
    data = read_metadata(cache_dir)
    networks = data.get("networks", {})
    if isinstance(networks, dict):
        return {k: dict(v) for k, v in networks.items() if isinstance(v, dict)}
    return {}


def remove_network_entry(cache_dir: Path, network_name: str) -> None:
    """Remove a network entry from metadata.json."""
    data = read_metadata(cache_dir)
    if "networks" in data and isinstance(data["networks"], dict):
        if network_name in data["networks"]:
            del data["networks"][network_name]
            write_metadata(cache_dir, data)

    # NEW: Also delete from SQLite
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(network_name)
        if network:
            db.delete_network(network.id)
    except Exception:
        pass


def set_default_network_entry(cache_dir: Path, network_name: str) -> None:
    """Set a network as the default, clearing is_default from all others."""
    _set_default_entry(cache_dir, "networks", network_name)

    # NEW: Also update SQLite
    try:
        db = MVMDatabase()
        network = db.get_network_by_name(network_name)
        if network:
            db.set_default_network(network.id)
    except Exception:
        pass


def get_default_network_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Return the default network entry as (name, metadata) or None if not set."""
    # Try SQLite first
    try:
        db = MVMDatabase()
        networks = db.list_networks()
        for network in networks:
            if network.is_default:
                return network.name, _db_network_to_dict(network)
    except Exception:
        pass

    # Fall back to JSON
    return _find_default_entry(cache_dir, "networks")
