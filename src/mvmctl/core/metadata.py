"""Unified metadata storage for kernels, images, and binaries.

All metadata is stored in SQLite via MVMDatabase. This module provides
functions to read and write metadata through the database layer.
"""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary, Image, Kernel, Network
from mvmctl.models.binary import BinaryRecord
from mvmctl.models.image import ImageRecord
from mvmctl.models.kernel import KernelRecord
from mvmctl.models.network import NetworkRecord
from mvmctl.utils.full_hash import generate_full_hash_binary

logger = logging.getLogger(__name__)


def _now_utc() -> str:
    """Return current UTC timestamp as ISO format string."""
    return datetime.now(tz=timezone.utc).isoformat()


# =============================================================================
# Kernel metadata
# =============================================================================


def update_kernel_entry(cache_dir: Path, kernel_name: str, **fields: Any) -> None:
    """Upsert kernel entry in database."""
    db = MVMDatabase()

    # Build Kernel model from fields
    kernel_id = fields.get("full_hash", kernel_name)
    path = fields.get("path", "")
    created_at = fields.get("created_at") or _now_utc()
    updated_at = fields.get("last_modified") or created_at

    kernel = Kernel(
        id=kernel_id,
        name=fields.get("name", path),
        version=fields.get("version", ""),
        arch=fields.get("arch", "x86_64"),
        path=path,
        base_name=fields.get("base_name"),
        type=fields.get("type"),
        is_default=fields.get("is_default", 0) == 1,
        created_at=created_at,
        updated_at=updated_at,
    )
    db.upsert_kernel(kernel)


def _flag_as_default(value: Any) -> int:
    if isinstance(value, bool):
        return 1 if value else 0
    if isinstance(value, int):
        return 1 if value == 1 else 0
    if isinstance(value, str):
        lowered = value.strip().lower()
        return 1 if lowered in {"1", "true", "yes"} else 0
    return 0


def set_default_kernel_entry(cache_dir: Path, kernel_id: str) -> None:
    """Set a kernel as the default."""
    db = MVMDatabase()
    db.set_default_kernel(kernel_id)


def set_default_kernel_by_filename(cache_dir: Path, filename: str) -> None:
    """Set a kernel as default by its filename."""
    db = MVMDatabase()
    kernels = db.list_kernels()
    for kernel in kernels:
        if kernel.path == filename:
            db.set_default_kernel(kernel.id)
            return
    raise KeyError(f"Kernel filename '{filename}' not found in metadata")


def get_default_kernel_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Return the default kernel entry as (id, metadata) or None."""
    db = MVMDatabase()
    kernel = db.get_default_kernel()
    if kernel is None:
        return None
    return kernel.id, KernelRecord.from_db(kernel).to_dict()


def get_kernel_entry(cache_dir: Path, kernel_name: str) -> dict[str, Any]:
    """Return kernel metadata entry or {} if not found."""
    db = MVMDatabase()

    # Try by ID first
    kernel = db.get_kernel(kernel_name)
    if kernel:
        return KernelRecord.from_db(kernel).to_dict()

    # Try by name
    kernel = db.get_kernel_by_name(kernel_name)
    if kernel:
        return KernelRecord.from_db(kernel).to_dict()

    return {}


def list_kernel_entries(
    cache_dir: Path, kernels_dir: Path | None = None, include_missing: bool = False
) -> dict[str, dict[str, Any]]:
    """Return all kernel entries dict keyed by ID.

    Validates that entries correspond to actual files and removes orphaned entries
    unless include_missing is True.
    """
    db = MVMDatabase()
    kernels = db.list_kernels()

    result: dict[str, dict[str, Any]] = {}
    orphaned: list[str] = []

    for kernel in kernels:
        kernel_path = Path(kernel.path)
        if kernels_dir is not None and kernels_dir.exists():
            full_path = kernels_dir / kernel_path.name
            if full_path.exists():
                result[kernel.id] = KernelRecord.from_db(kernel).to_dict()
            elif include_missing:
                result[kernel.id] = KernelRecord.from_db(kernel).to_dict()
            else:
                orphaned.append(kernel.id)
        else:
            result[kernel.id] = KernelRecord.from_db(kernel).to_dict()

    # Clean up orphaned entries
    for kernel_id in orphaned:
        db.delete_kernel(kernel_id)

    return result


def remove_kernel_entry(cache_dir: Path, kernel_name: str) -> None:
    """Remove a kernel entry from database by its ID."""
    db = MVMDatabase()
    # kernel_name is actually the kernel ID (full hash)
    db.delete_kernel(kernel_name)


# =============================================================================
# Image metadata
# =============================================================================


def update_image_entry(cache_dir: Path, image_id: str, **fields: Any) -> None:
    """Upsert image entry in database."""
    db = MVMDatabase()

    created_at = fields.get("created_at") or _now_utc()
    updated_at = fields.get("updated_at") or created_at

    image = Image(
        id=image_id,
        os_slug=fields.get("os_slug") or fields.get("internal_id", ""),
        path=fields.get("path", ""),
        os_name=fields.get("os_name"),
        fs_type=fields.get("fs_type"),
        fs_uuid=fields.get("fs_uuid"),
        compressed_size=fields.get("compressed_size"),
        original_size=fields.get("original_size"),
        compression_ratio=fields.get("compression_ratio"),
        compressed_format=fields.get("compressed_format"),
        pulled_at=fields.get("pulled_at"),
        is_default=fields.get("is_default", 0) == 1,
        created_at=created_at,
        updated_at=updated_at,
    )
    db.upsert_image(image)


def get_image_entry(cache_dir: Path, image_id: str) -> dict[str, Any]:
    """Return image metadata entry or {} if not found."""
    db = MVMDatabase()
    image = db.get_image(image_id)
    if image:
        return ImageRecord.from_db(image).to_dict()
    return {}


def list_image_entries(
    cache_dir: Path, images_dir: Path | None = None, include_missing: bool = False
) -> dict[str, dict[str, Any]]:
    """Return all image entries dict keyed by image ID.

    Validates that entries correspond to actual files and removes orphaned entries
    unless include_missing is True.
    """
    db = MVMDatabase()
    images = db.list_images()

    result: dict[str, dict[str, Any]] = {}
    orphaned: list[str] = []

    for image in images:
        if images_dir is not None and images_dir.exists():
            filename = image.path
            image_path = images_dir / filename
            if image_path.exists():
                result[image.id] = ImageRecord.from_db(image).to_dict()
            elif include_missing:
                result[image.id] = ImageRecord.from_db(image).to_dict()
            else:
                orphaned.append(image.id)
        else:
            result[image.id] = ImageRecord.from_db(image).to_dict()

    # Clean up orphaned entries
    for image_id in orphaned:
        db.delete_image(image_id)

    return result


def remove_image_entry(cache_dir: Path, image_id: str) -> None:
    """Remove an image entry from database."""
    db = MVMDatabase()
    db.delete_image(image_id)


def set_default_image_entry(cache_dir: Path, image_id: str) -> None:
    """Set an image as the default."""
    db = MVMDatabase()
    db.set_default_image(image_id)


def set_default_image_by_os_slug(cache_dir: Path, os_slug: str) -> None:
    db = MVMDatabase()
    image = db.get_image_by_os_slug(os_slug)
    if image:
        db.set_default_image(image.id)
        return

    for image in db.list_images():
        if image.os_slug == os_slug:
            db.set_default_image(image.id)
            return

    raise KeyError(f"Image os_slug '{os_slug}' not found in metadata")


def get_default_image_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Return the default image entry as (id, metadata) or None."""
    db = MVMDatabase()
    image = db.get_default_image()
    if image is None:
        return None
    return image.id, ImageRecord.from_db(image).to_dict()


def find_image_by_id_prefix(cache_dir: Path, prefix: str) -> tuple[str, dict[str, Any]] | None:
    """Find an image entry whose key starts with prefix. Returns (full_key, meta) or None."""
    db = MVMDatabase()
    images = db.find_images_by_prefix(prefix)
    if len(images) == 1:
        return images[0].id, ImageRecord.from_db(images[0]).to_dict()
    return None


def find_images_by_id_prefix(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, Any]]]:
    """Return all image entries whose key starts with prefix."""
    db = MVMDatabase()
    images = db.find_images_by_prefix(prefix)
    return [(img.id, ImageRecord.from_db(img).to_dict()) for img in images]


# =============================================================================
# Binary metadata
# =============================================================================

_BINARY_METADATA_NAMES = ("firecracker", "jailer")


def _normalized_package_version(value: str) -> str:
    return value.removeprefix("v")


def _derive_ci_version(version: str) -> str:
    parts = version.split(".")
    return f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{version}"


def _dict_to_db_binary(name: str, entry: dict[str, Any]) -> Binary | None:
    """Convert dict entry to DB Binary model."""
    binary_path_value = entry.get("binary_path")
    if not isinstance(binary_path_value, str) or not binary_path_value:
        return None

    version_value = entry.get("package_version")
    if not isinstance(version_value, str) or not version_value:
        return None

    binary_path = Path(binary_path_value)
    if not binary_path.exists() or not binary_path.is_file():
        return None

    binary_id_value = entry.get("binary_id") or entry.get("id")
    binary_id = (
        binary_id_value
        if isinstance(binary_id_value, str) and binary_id_value
        else generate_full_hash_binary(binary_path, name, version_value)
    )

    created_at_value = entry.get("created_at")
    created_at = (
        created_at_value if isinstance(created_at_value, str) and created_at_value else _now_utc()
    )
    updated_at_value = entry.get("updated_at")
    updated_at = (
        updated_at_value if isinstance(updated_at_value, str) and updated_at_value else created_at
    )

    full_version_value = entry.get("full_version")
    full_version = (
        full_version_value
        if isinstance(full_version_value, str) and full_version_value
        else f"v{_normalized_package_version(version_value)}"
    )
    ci_version_value = entry.get("ci_version")
    ci_version = (
        ci_version_value
        if isinstance(ci_version_value, str) and ci_version_value
        else _derive_ci_version(_normalized_package_version(version_value))
    )

    return Binary(
        id=binary_id,
        name=name,
        version=version_value,
        path=str(binary_path),
        full_version=full_version,
        ci_version=ci_version,
        is_default=entry.get("is_default", False),
        created_at=created_at,
        updated_at=updated_at,
    )


def _find_db_binary_by_name_and_version(db: MVMDatabase, version: str) -> Binary | None:
    """Find a binary by name and version, ensuring both firecracker and jailer exist."""
    normalized_version = _normalized_package_version(version)
    matches: dict[str, Binary] = {}
    for binary_name in _BINARY_METADATA_NAMES:
        for binary in db.list_binaries_by_name(binary_name):
            if _normalized_package_version(binary.version) == normalized_version:
                matches[binary_name] = binary
                break
    if len(matches) != len(_BINARY_METADATA_NAMES):
        return None
    return matches["firecracker"]


def _find_db_default_binary(db: MVMDatabase) -> Binary | None:
    """Find the default binary pair (firecracker + jailer)."""
    matches: dict[str, Binary] = {}
    for binary_name in _BINARY_METADATA_NAMES:
        default = db.get_default_binary(binary_name)
        if default is None:
            return None
        default_version = _normalized_package_version(default.version)
        for binary in db.list_binaries_by_name(binary_name):
            if _normalized_package_version(binary.version) == default_version:
                matches[binary_name] = binary
                break
        else:
            return None
    if len(matches) != len(_BINARY_METADATA_NAMES):
        return None
    return matches["firecracker"]


def update_binary_entry(cache_dir: Path, version: str, **fields: Any) -> None:
    """Update binary entry in database.

    Creates/updates both firecracker and jailer entries.
    """
    db = MVMDatabase()
    normalized_version = _normalized_package_version(version)

    full_version = str(fields.get("full_version") or f"v{normalized_version}")
    ci_version = str(fields.get("ci_version") or _derive_ci_version(normalized_version))

    # Build per-binary payloads
    per_binary_payloads: dict[str, dict[str, Any]] = {
        "firecracker": {
            "binary_name": "firecracker",
            "binary_path": fields.get("firecracker_path"),
            "package_version": normalized_version,
            "full_version": full_version,
            "ci_version": ci_version,
            "is_default": fields.get("is_default", 0),
        },
        "jailer": {
            "binary_name": "jailer",
            "binary_path": fields.get("jailer_path"),
            "package_version": normalized_version,
            "full_version": full_version,
            "ci_version": ci_version,
            "is_default": fields.get("is_default", 0),
        },
    }

    # Update individual binary entries
    for binary_name, binary_payload in per_binary_payloads.items():
        db_binary = _dict_to_db_binary(binary_name, binary_payload)
        if db_binary:
            db.upsert_binary(db_binary)


def get_binary_entry(cache_dir: Path, version: str) -> dict[str, Any]:
    """Return binary metadata entry or {} if not found."""
    db = MVMDatabase()
    binary = _find_db_binary_by_name_and_version(db, version)
    if binary is not None:
        return BinaryRecord.from_db(binary).to_dict()
    return {}


def list_binary_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all binary entries dict keyed by binary name."""
    db = MVMDatabase()
    binaries = db.list_binaries()

    # Group by name, only return if we have both firecracker and jailer
    result: dict[str, dict[str, Any]] = {}
    for binary in binaries:
        if binary.name in _BINARY_METADATA_NAMES:
            result[binary.name] = BinaryRecord.from_db(binary).to_dict()

    return result


def set_default_binary_entry(cache_dir: Path, version: str) -> None:
    """Set a binary version as default."""
    db = MVMDatabase()
    normalized_version = _normalized_package_version(version)

    # Verify the version exists
    has_match = False
    for binary_name in _BINARY_METADATA_NAMES:
        for binary in db.list_binaries_by_name(binary_name):
            if _normalized_package_version(binary.version) == normalized_version:
                has_match = True
                break
        if has_match:
            break

    if not has_match:
        raise KeyError(f"Binary version '{version}' not found in metadata")

    # Set as default for both
    for binary_name in _BINARY_METADATA_NAMES:
        for binary in db.list_binaries_by_name(binary_name):
            if _normalized_package_version(binary.version) == normalized_version:
                db.set_default_binary(binary_name, binary.version, binary.path)
                break


def get_default_binary_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Return the default binary entry as (version, metadata) or None."""
    db = MVMDatabase()
    binary = _find_db_default_binary(db)
    if binary is not None:
        return binary.version, BinaryRecord.from_db(binary).to_dict()
    return None


# =============================================================================
# Network metadata
# =============================================================================


def update_network_entry(cache_dir: Path, network_name: str, **fields: Any) -> None:
    """Upsert network entry in database."""
    from mvmctl.utils.full_hash import generate_full_hash_network

    db = MVMDatabase()

    subnet = fields.get("subnet", "")
    created_at = fields.get("created_at") or _now_utc()
    updated_at = fields.get("updated_at") or created_at

    existing = db.get_network_by_name(network_name)
    network_id = fields.get("network_id") or (
        existing.id if existing else generate_full_hash_network(network_name, subnet, created_at)
    )

    nat_gateways = fields.get("nat_gateways", [])
    nat_gateways_str = ",".join(nat_gateways) if nat_gateways else None

    network = Network(
        id=network_id,
        name=network_name,
        subnet=subnet,
        bridge=fields.get("bridge", ""),
        ipv4_gateway=fields.get("ipv4_gateway", ""),
        bridge_active=fields.get("bridge_active", False),
        nat_gateways=nat_gateways_str,
        nat_enabled=fields.get("nat_enabled", True),
        is_default=fields.get("is_default", 0) == 1,
        created_at=created_at,
        updated_at=updated_at,
    )
    db.upsert_network(network)


def get_network_entry(cache_dir: Path, network_name: str) -> dict[str, Any]:
    """Return network metadata entry or {} if not found."""
    db = MVMDatabase()
    network = db.get_network_by_name(network_name)
    if network:
        return NetworkRecord.from_db(network).to_dict()
    return {}


def list_network_entries(cache_dir: Path) -> dict[str, dict[str, Any]]:
    """Return all network entries dict keyed by network name."""
    db = MVMDatabase()
    networks = db.list_networks()
    return {network.name: NetworkRecord.from_db(network).to_dict() for network in networks}


def remove_network_entry(cache_dir: Path, network_name: str) -> None:
    """Remove a network entry from database."""
    db = MVMDatabase()
    network = db.get_network_by_name(network_name)
    if network:
        db.delete_network(network.id)


def set_default_network_entry(cache_dir: Path, network_name: str) -> None:
    """Set a network as the default."""
    db = MVMDatabase()
    network = db.get_network_by_name(network_name)
    if network:
        db.set_default_network(network.id)


def get_default_network_entry(cache_dir: Path) -> tuple[str, dict[str, Any]] | None:
    """Return the default network entry as (name, metadata) or None."""
    db = MVMDatabase()
    network = db.get_default_network()
    if network:
        return network.name, NetworkRecord.from_db(network).to_dict()
    return None
