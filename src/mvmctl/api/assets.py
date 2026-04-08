from __future__ import annotations

import logging
from pathlib import Path
from typing import TYPE_CHECKING, Any, Literal, TypedDict

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary
from mvmctl.exceptions import AssetNotFoundError
from mvmctl.models.image import ImageSpec
from mvmctl.utils.fs import get_cache_dir

if TYPE_CHECKING:
    from mvmctl.core.binary_manager import BinaryVersion
    from mvmctl.core.image import ImageImportResult
    from mvmctl.models.image import ImageImportSpec

from mvmctl.core.binary_manager import BinaryVersion
from mvmctl.models.image import ImageImportSpec

logger = logging.getLogger(__name__)

__all__ = [
    "AssetInfo",
    "BinaryVersion",
    "ImageImportResult",
    "ImageImportSpec",
    "ensure_default_binary",
    "fetch_binary",
    "get_binary_path",
    "list_local_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "resolve_image_path",
    "resolve_image_fs_uuid",
    "resolve_image_fs_type",
    "resolve_image_id_path",
    "list_remote_versions",
    "load_images_config",
    "import_image",
]


def _normalize_version(version: str) -> str:
    return version.removeprefix("v")


def ensure_default_binary(bin_dir: Path | None = None) -> str | None:
    """Set a default binary if none is recorded; return active version or None.

    This function queries the database to check if a default binary exists.
    If not, it selects the best available local version and sets it as default.

    Args:
        bin_dir: Optional directory containing binaries. Uses default if None.

    Returns:
        The version string of the default binary, or None if no binary available.
    """
    db = MVMDatabase()
    existing_default = db.get_default_binary("firecracker")
    if existing_default is not None and existing_default.path:
        return _normalize_version(existing_default.version or "")

    local = list_local_versions()
    if not local:
        return None

    best = local[0]
    set_active_version(best.version)
    return best.version


def fetch_binary(version: str, bin_dir: Path | None = None) -> BinaryVersion:
    """Download Firecracker and jailer binaries for *version*.

    If no default binary is currently set, the downloaded version will be
    automatically set as the default.

    Args:
        version: The Firecracker version to fetch (e.g., "1.15.0").
        bin_dir: Optional directory to store binaries. Uses default if None.

    Returns:
        BinaryVersion with paths and active status.
    """
    db = MVMDatabase()
    no_default = db.get_default_binary("firecracker") is None

    from mvmctl.core.binary_manager import fetch_binary as _core_fetch_binary
    from mvmctl.core.metadata import update_binary_entry

    result = _core_fetch_binary(version, bin_dir, set_as_default=no_default)

    # Persist metadata to database
    cache_dir = get_cache_dir()
    normalized_version = _normalize_version(result.version)
    update_binary_entry(
        cache_dir,
        normalized_version,
        full_version=f"v{normalized_version}",
        ci_version=f"v{normalized_version.split('.')[0]}.{normalized_version.split('.')[1]}"
        if len(normalized_version.split(".")) >= 2
        else f"v{normalized_version}",
        firecracker_path=str(result.firecracker_path),
        jailer_path=str(result.jailer_path),
        is_default=1 if no_default else 0,
    )

    if no_default:
        from mvmctl.core.binary_manager import (
            fetch_binary as _core_fetch_binary,
        )
        from mvmctl.core.binary_manager import (
            set_active_version as _core_set_active_version,
        )
        from mvmctl.core.metadata import set_default_binary_entry

        _core_set_active_version(result.version, bin_dir)
        set_default_binary_entry(cache_dir, normalized_version)

    return result


def get_binary_path(name: str, version: str | None = None) -> str:
    """Return the filesystem path for the named binary.

    Args:
        name: Binary name, e.g. "firecracker" or "jailer".
        version: Specific version string, e.g. "1.15.0". If None, the
                 default binary for this name is looked up from the database.

    Returns:
        Absolute path string to the binary file.

    Raises:
        AssetNotFoundError: If version is specified but not found locally.
        AssetNotFoundError: If version is None and no default is set.
        AssetNotFoundError: If the resolved path does not exist on disk.
    """
    from mvmctl.core.binary_manager import get_binary_path as _core_get_binary_path

    if version is not None:
        return _core_get_binary_path(name, version)

    db = MVMDatabase()
    default = db.get_default_binary(name)
    if default is None:
        raise AssetNotFoundError(
            f"No active binary for '{name}' found — run 'mvm bin fetch <version>' to download "
            f"one, or 'mvm bin set-default <version>' if you already have a local version."
        )
    if not default.path:
        raise AssetNotFoundError(
            f"No active binary for '{name}' found — run 'mvm bin fetch <version>' to download "
            f"one, or 'mvm bin set-default <version>' if you already have a local version."
        )
    if not Path(default.path).exists():
        raise AssetNotFoundError(
            f"Default binary for '{name}' is registered at '{default.path}' but the file is "
            f"missing — run 'mvm bin fetch <version>' to re-download it, or "
            f"'mvm bin set-default <version>' to point to an existing local version."
        )
    return default.path


def list_local_versions(bin_dir: Path | None = None) -> list[BinaryVersion]:
    """List locally cached Firecracker/jailer binary pairs.

    Queries SQLite (MVMDatabase.list_binaries_by_name) and derives is_active
    from the is_default column. This is the canonical path for CLI and API callers.

    Args:
        bin_dir: Optional directory to scan. Uses default if None.

    Returns:
        List of BinaryVersion objects sorted by version (newest first).
    """
    from mvmctl.core.binary_manager import list_local_versions as _core_list_local_versions

    if bin_dir is not None:
        return _core_list_local_versions(bin_dir)

    db = MVMDatabase()
    fc_binaries = db.list_binaries_by_name("firecracker")
    jl_binaries = db.list_binaries_by_name("jailer")

    jl_by_version: dict[str, Binary] = {}
    for jl_bin in jl_binaries:
        jl_by_version[_normalize_version(jl_bin.version)] = jl_bin

    result: list[BinaryVersion] = []
    for fc in sorted(fc_binaries, key=lambda b: b.version, reverse=True):
        normalized = _normalize_version(fc.version)
        jl_bin_match = jl_by_version.get(normalized)
        if jl_bin_match is None:
            continue

        fc_path = Path(fc.path)
        jl_path = Path(jl_bin_match.path)
        if not fc_path.exists() or not jl_path.exists():
            continue

        result.append(
            BinaryVersion(
                version=normalized,
                firecracker_path=fc_path,
                jailer_path=jl_path,
                is_active=bool(fc.is_default),
            )
        )

    return result


def set_active_version(version: str, bin_dir: Path | None = None) -> None:
    """Create/update symlinks and database entry for the active Firecracker version.

    Args:
        version: The version to set as active (e.g., "1.15.0").
        bin_dir: Optional directory containing binaries. Uses default if None.
    """
    from mvmctl.core.binary_manager import set_active_version as _core_set_active_version
    from mvmctl.core.metadata import set_default_binary_entry, update_binary_entry

    _core_set_active_version(version, bin_dir)

    # Persist metadata to database
    cache_dir = get_cache_dir()
    normalized_version = _normalize_version(version)

    # Get the binary paths from the filesystem
    from mvmctl.utils.fs import get_bin_dir

    bin_directory = bin_dir if bin_dir is not None else get_bin_dir()
    fc_src = bin_directory / f"firecracker-v{normalized_version}"
    jl_src = bin_directory / f"jailer-v{normalized_version}"

    parts = normalized_version.split(".")
    ci_version = f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{normalized_version}"
    full_version = f"v{normalized_version}"

    update_binary_entry(
        cache_dir,
        normalized_version,
        full_version=full_version,
        ci_version=ci_version,
        firecracker_path=str(fc_src),
        jailer_path=str(jl_src),
        is_default=1,
    )
    set_default_binary_entry(cache_dir, normalized_version)


def remove_version(version: str, bin_dir: Path | None = None) -> None:
    """Delete a locally cached binary version and update database.

    Args:
        version: The version to remove (e.g., "1.15.0").
        bin_dir: Optional directory containing binaries. Uses default if None.

    Raises:
        AssetNotFoundError: If the version is not found locally.
    """
    from mvmctl.core.binary_manager import remove_version as _core_remove_version

    _core_remove_version(version, bin_dir)

    db = MVMDatabase()
    normalized = _normalize_version(version)
    db.delete_binary_by_name_and_version("firecracker", normalized)
    db.delete_binary_by_name_and_version("jailer", normalized)


def fetch_image(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
    skip_optimization: bool = False,
) -> "ImageImportResult":
    """Fetch and convert an image.

    Args:
        spec: Image specification
        output_dir: Directory to store images
        force: Re-download even if exists
        partition: Specific partition number to extract (1-indexed), or None for auto-detect
        skip_optimization: Skip shrink and compression, keep plain ext4

    Returns:
        Path to final image
    """
    from mvmctl.api.metadata import get_default_binary_entry
    from mvmctl.core.image import fetch_image as _core_fetch_image

    # Fetch CI version from default binary for template resolution
    ci_version = ""
    try:
        default_binary = get_default_binary_entry()
        if default_binary is not None:
            _version, binary_meta = default_binary
            raw_ci_version = binary_meta.get("ci_version")
            if isinstance(raw_ci_version, str):
                ci_version = raw_ci_version
    except Exception:
        pass

    return _core_fetch_image(
        spec,
        output_dir,
        force=force,
        partition=partition,
        skip_optimization=skip_optimization,
        ci_version=ci_version,
    )


class AssetInfo(TypedDict):
    type: Literal["binary", "kernel", "image"]
    name: str
    active: bool | None
    size_mib: float | None
    details: str | None


def resolve_image_path(image: str) -> Path:
    """Resolve an image identifier to a filesystem path.

    Args:
        image: Image identifier (name, path, or ID prefix).

    Returns:
        Path to the image file.

    Raises:
        AssetNotFoundError: If the image cannot be found.
    """
    from mvmctl.constants import SUPPORTED_IMAGE_EXTENSIONS
    from mvmctl.core.metadata import find_images_by_id_prefix as _find_images_by_id_prefix
    from mvmctl.utils.fs import get_images_dir

    images_dir = get_images_dir()
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        compressed = images_dir / f"{image}{ext}.zst"
        if compressed.exists():
            return compressed
        candidate = images_dir / f"{image}{ext}"
        if candidate.exists():
            return candidate

    direct = Path(image)
    if direct.is_absolute() and direct.exists():
        return direct

    matches = _find_images_by_id_prefix(get_cache_dir(), image)
    if len(matches) == 1:
        full_key, meta = matches[0]
        path = str(meta.get("path", ""))
        if path:
            compressed = images_dir / f"{path}.zst"
            if compressed.exists():
                return compressed
            candidate = images_dir / path
            if candidate.exists():
                return candidate
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            compressed = images_dir / f"{full_key}{ext}.zst"
            if compressed.exists():
                return compressed
            candidate = images_dir / f"{full_key}{ext}"
            if candidate.exists():
                return candidate

    if direct.exists():
        return direct

    raise AssetNotFoundError(f"Image not found: {image!r}")


def resolve_image_fs_uuid(image: str) -> str | None:
    """Resolve the filesystem UUID for an image.

    Args:
        image: Image identifier (name, path, or ID prefix).

    Returns:
        Filesystem UUID string, or None if not found.
    """
    from mvmctl.core.metadata import (
        find_images_by_id_prefix as _find_images_by_id_prefix,
    )
    from mvmctl.core.metadata import (
        list_image_entries as _list_image_entries,
    )

    cache_dir = get_cache_dir()
    for _full_key, meta in _list_image_entries(cache_dir).items():
        if image not in {str(meta.get("os_slug", "")), str(meta.get("path", ""))}:
            continue
        fs_uuid = meta.get("fs_uuid")
        if isinstance(fs_uuid, str) and fs_uuid.strip():
            return fs_uuid.strip()

    matches = _find_images_by_id_prefix(cache_dir, image)
    if len(matches) == 1:
        _, meta = matches[0]
        fs_uuid = meta.get("fs_uuid")
        if isinstance(fs_uuid, str) and fs_uuid.strip():
            return fs_uuid.strip()
    return None


def resolve_image_fs_type(image: str) -> str | None:
    """Resolve the filesystem type for an image.

    Args:
        image: Image identifier (name, path, or ID prefix).

    Returns:
        Filesystem type string (e.g., "ext4", "btrfs"), or None if not found.
    """
    from mvmctl.core.metadata import (
        find_images_by_id_prefix as _find_images_by_id_prefix,
    )
    from mvmctl.core.metadata import (
        list_image_entries as _list_image_entries,
    )

    cache_dir = get_cache_dir()
    for _full_key, meta in _list_image_entries(cache_dir).items():
        if image not in {str(meta.get("os_slug", "")), str(meta.get("path", ""))}:
            continue
        fs_type = meta.get("fs_type")
        if isinstance(fs_type, str) and fs_type.strip():
            return fs_type.strip()

    matches = _find_images_by_id_prefix(cache_dir, image)
    if len(matches) == 1:
        _, meta = matches[0]
        fs_type = meta.get("fs_type")
        if isinstance(fs_type, str) and fs_type.strip():
            return fs_type.strip()
    return None


def resolve_image_id_path(image: str) -> Path:
    """Resolve an image ID prefix to a filesystem path.

    Args:
        image: Image ID prefix to resolve.

    Returns:
        Path to the image file.

    Raises:
        AssetNotFoundError: If the image ID is not found or is ambiguous.
    """
    from mvmctl.constants import SUPPORTED_IMAGE_EXTENSIONS
    from mvmctl.core.metadata import find_images_by_id_prefix as _find_images_by_id_prefix
    from mvmctl.utils.fs import get_images_dir
    from mvmctl.utils.id_lookup import resolve_single_by_id_prefix

    images_dir = get_images_dir()
    match = resolve_single_by_id_prefix(image, _find_images_by_id_prefix, get_cache_dir())
    if match is None:
        raise AssetNotFoundError(f"Image ID not found or ambiguous: {image!r}")

    full_key, meta = match
    filename = str(meta.get("path", ""))
    if filename:
        compressed = images_dir / f"{filename}.zst"
        if compressed.exists():
            return compressed
        candidate = images_dir / filename
        if candidate.exists():
            return candidate
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        compressed = images_dir / f"{full_key}{ext}.zst"
        if compressed.exists():
            return compressed
        candidate = images_dir / f"{full_key}{ext}"
        if candidate.exists():
            return candidate

    raise AssetNotFoundError(f"Image not found: {image!r}")


def list_remote_versions(limit: int = 10) -> list[str]:
    from mvmctl.core.binary_manager import list_remote_versions as _list_remote_versions

    return _list_remote_versions(limit)


def load_images_config(path: Path) -> list[Any]:
    from mvmctl.core.image import load_images_config as _load_images_config

    return _load_images_config(path)


def import_image(spec: Any, output_dir: Path) -> Any:
    from mvmctl.core.image import import_image as _import_image

    return _import_image(spec, output_dir)
