from __future__ import annotations

import logging
from pathlib import Path
from typing import Literal, TypedDict

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
from mvmctl.core.image import fetch_image, get_filesystem_uuid, import_image, load_images_config
from mvmctl.core.kernel import (
    build_kernel_pipeline,
    download_firecracker_kernel,
    get_default_kernel_path,
    list_kernels,
    resolve_kernel_spec,
    set_default_kernel,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary
from mvmctl.exceptions import AssetNotFoundError
from mvmctl.models.image import ImageImportSpec

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

    if no_default:
        _core_set_active_version(result.version, bin_dir)

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


class AssetInfo(TypedDict):
    type: Literal["binary", "kernel", "image"]
    name: str
    active: bool | None
    size_mib: float | None
    details: str | None
