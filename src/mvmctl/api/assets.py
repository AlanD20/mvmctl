from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Literal, TypedDict

from mvmctl.constants import CONST_MEBIBYTE_BYTES, KERNEL_TYPE_UNKNOWN
from mvmctl.core.binary_manager import (
    BinaryVersion,
    list_remote_versions,
)
from mvmctl.core.binary_manager import (
    fetch_binary as _core_fetch_binary,
)
from mvmctl.core.binary_manager import (
    get_binary_path as _core_get_binary_path,
)
from mvmctl.core.binary_manager import (
    list_local_versions as _core_list_local_versions,
)
from mvmctl.core.binary_manager import (
    remove_version as _core_remove_version,
)
from mvmctl.core.binary_manager import (
    set_active_version as _core_set_active_version,
)
from mvmctl.core.image import fetch_image as _core_fetch_image
from mvmctl.core.image import get_filesystem_uuid, import_image, load_images_config
from mvmctl.core.kernel import (
    build_kernel_pipeline,
    download_firecracker_kernel,
    parse_kernel_filename,
    resolve_kernel_spec,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.metadata import (
    find_images_by_id_prefix as _find_images_by_id_prefix,
    list_image_entries as _list_image_entries,
    list_kernel_entries,
    set_default_binary_entry,
    set_default_kernel_by_filename,
    update_binary_entry,
    update_kernel_entry,
)
from mvmctl.db.models import Binary
from mvmctl.exceptions import AssetNotFoundError, KernelError, MVMError
from mvmctl.models.image import ImageImportSpec
from mvmctl.utils.full_hash import generate_full_hash_kernel
from mvmctl.utils.fs import get_cache_dir, get_kernels_dir

logger = logging.getLogger(__name__)

__all__ = [
    "AssetInfo",
    "BinaryVersion",
    "ImageImportSpec",
    "ensure_default_binary",
    "fetch_binary",
    "get_binary_path",
    "list_local_versions",
    "list_remote_versions",
    "set_active_version",
    "remove_version",
    "fetch_image",
    "import_image",
    "load_images_config",
    "get_filesystem_uuid",
    "build_kernel_pipeline",
    "list_kernels",
    "set_default_kernel",
    "get_default_kernel_path",
    "resolve_kernel_spec",
    "download_firecracker_kernel",
    "resolve_image_path",
    "resolve_image_fs_uuid",
    "resolve_image_fs_type",
    "resolve_image_id_path",
    "resolve_kernel_path",
    "resolve_kernel_id_path",
    "save_kernel_metadata",
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
    _core_remove_version(version, bin_dir)

    db = MVMDatabase()
    normalized = _normalize_version(version)
    db.delete_binary_by_name_and_version("firecracker", normalized)
    db.delete_binary_by_name_and_version("jailer", normalized)


def fetch_image(
    spec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
    skip_optimization: bool = False,
):
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


# =============================================================================
# Kernel resolution/lookup functions (moved from core/kernel.py to fix layer violations)
# =============================================================================


def save_kernel_metadata(
    kernels_dir: Path,
    kernel_name: str,
    version: str | None = None,
    kernel_type: str | None = None,
    arch: str | None = None,
) -> str:
    """Save kernel metadata to database.

    Args:
        kernels_dir: Directory containing kernels
        kernel_name: Name of the kernel file
        version: Kernel version string
        kernel_type: Type of kernel (firecracker, official, unknown)
        arch: Architecture (x86_64, arm64, etc.)

    Returns:
        The full hash ID of the kernel entry
    """
    kernel_path = kernels_dir / kernel_name

    parsed = parse_kernel_filename(kernel_name)

    if version is None:
        version = parsed.version
    if arch is None:
        arch = parsed.arch
    if kernel_type is None:
        kernel_type = KERNEL_TYPE_UNKNOWN

    last_modified = "-"
    if kernel_path.exists():
        mtime = kernel_path.stat().st_mtime
        last_modified = datetime.fromtimestamp(mtime, tz=timezone.utc).isoformat()

    full_id = generate_full_hash_kernel(
        kernel_path,
        version,
        arch,
    )

    cache_dir = get_cache_dir()
    update_kernel_entry(
        cache_dir,
        full_id,
        path=kernel_name,
        full_hash=full_id,
        name=kernel_name,
        base_name=parsed.base_name,
        version=version,
        arch=arch,
        type=kernel_type,
        last_modified=last_modified,
    )
    return full_id


def _load_default_kernel(kernels_dir: Path) -> str | None:
    """Load the default kernel path from database.

    Args:
        kernels_dir: Directory containing kernels

    Returns:
        Path to default kernel or None if not set
    """
    db = MVMDatabase()
    default_kernel = db.get_default_kernel()
    if default_kernel is None:
        return None
    path = default_kernel.path
    if isinstance(path, str) and path:
        return path
    return None


def set_default_kernel(kernels_dir: Path, kernel_name: str) -> None:
    """Set a kernel as the default.

    Args:
        kernels_dir: Directory containing kernels
        kernel_name: Name of the kernel file to set as default

    Raises:
        KernelError: If the kernel file does not exist
    """
    kernel_path = kernels_dir / kernel_name
    if not kernel_path.exists():
        raise KernelError(f"Kernel not found: {kernel_path}")
    set_default_kernel_by_filename(get_cache_dir(), kernel_name)
    logger.info("Default kernel set to: %s", kernel_name)


def get_default_kernel_path(kernels_dir: Path) -> Path | None:
    """Get the path to the default kernel.

    Args:
        kernels_dir: Directory containing kernels

    Returns:
        Path to default kernel or None if not set or not found
    """
    name = _load_default_kernel(kernels_dir)
    if name is None:
        return None
    path = kernels_dir / name
    return path if path.exists() else None


def list_kernels(kernels_dir: Path) -> list[dict[str, str]]:
    """List all kernels with their metadata.

    Args:
        kernels_dir: Directory containing kernels

    Returns:
        List of kernel metadata dictionaries
    """
    kernels_dir.mkdir(parents=True, exist_ok=True)

    cache_dir = get_cache_dir()

    entries = list_kernel_entries(cache_dir, kernels_dir, include_missing=True)

    results: list[dict[str, str]] = []

    for entry_id, meta in sorted(entries.items()):
        path = str(meta.get("path", entry_id))
        kernel_file_path = kernels_dir / path
        # Include entries even if file is missing - CLI will show X mark
        file_exists = kernel_file_path.is_file()

        size_mb = kernel_file_path.stat().st_size / CONST_MEBIBYTE_BYTES if file_exists else 0

        last_modified = meta.get("last_modified")
        if not last_modified:
            last_modified = meta.get("built_at", "-")

        if meta.get("base_name"):
            base_name = str(meta["base_name"])
            version = str(meta.get("version", "-"))
            arch = str(meta.get("arch", "-"))
            kernel_type = str(meta.get("type", KERNEL_TYPE_UNKNOWN))
        else:
            parsed = parse_kernel_filename(path)
            base_name = parsed.base_name
            version = parsed.version
            arch = parsed.arch
            kernel_type = KERNEL_TYPE_UNKNOWN

        is_default_val = meta.get("is_default", 0)
        is_default_flag = "true" if str(is_default_val) in ("1", "true") else "false"

        results.append(
            {
                "id": entry_id,
                "name": base_name,
                "path": path,
                "full_name": path,
                "version": version,
                "type": kernel_type,
                "arch": arch,
                "last_modified": str(last_modified) if last_modified else "-",
                "size": f"{size_mb:.1f} MiB",
                "is_default": is_default_flag,
            }
        )

    return results


def resolve_kernel_path(kernel: str) -> Path:
    """Resolve a kernel identifier to a filesystem path.

    Tries multiple strategies:
    1. Direct file path in kernels directory
    2. Absolute path
    3. Database lookup by ID prefix

    Args:
        kernel: Kernel identifier (filename, path, or ID prefix)

    Returns:
        Resolved path to the kernel file

    Raises:
        MVMError: If kernel cannot be found
    """
    kernels_dir = get_kernels_dir()
    candidate = kernels_dir / kernel
    if candidate.exists():
        return candidate

    direct = Path(kernel)
    if direct.is_absolute() and direct.exists():
        return direct

    # Try database lookup by ID prefix
    cache_dir = get_cache_dir()
    matches = [
        (k, m)
        for k, m in list_kernel_entries(cache_dir, kernels_dir).items()
        if k.startswith(kernel)
    ]
    if len(matches) == 1:
        full_key, meta = matches[0]
        path = str(meta.get("path", ""))
        if path:
            candidate = kernels_dir / path
            if candidate.exists():
                return candidate
        candidate = kernels_dir / full_key
        if candidate.exists():
            return candidate

    if direct.exists():
        return direct

    raise MVMError(f"Kernel not found: {kernel!r}")


def resolve_kernel_id_path(kernel: str) -> Path:
    """Resolve a kernel ID prefix to a filesystem path.

    Args:
        kernel: Kernel ID prefix

    Returns:
        Resolved path to the kernel file

    Raises:
        MVMError: If kernel ID is not found or ambiguous
    """
    from mvmctl.utils.id_lookup import resolve_single_by_id_prefix

    kernels_dir = get_kernels_dir()
    cache_dir = get_cache_dir()

    def _find(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, object]]]:
        return [
            (k, m)
            for k, m in list_kernel_entries(cache_dir, kernels_dir).items()
            if k.startswith(prefix)
        ]

    match = resolve_single_by_id_prefix(kernel, _find, cache_dir)
    if match is None:
        raise MVMError(f"Kernel ID not found or ambiguous: {kernel!r}")

    full_key, meta = match
    path = str(meta.get("path", ""))
    if path:
        candidate = kernels_dir / path
        if candidate.exists():
            return candidate
    candidate = kernels_dir / full_key
    if candidate.exists():
        return candidate

    raise MVMError(f"Kernel not found: {kernel!r}")
