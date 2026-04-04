"""Firecracker/jailer binary version management."""

from __future__ import annotations

import json
import logging
import os
import stat
import tarfile
from dataclasses import dataclass
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

from mvmctl.constants import (
    CONST_BUFFER_SIZE_BYTES,
    CONST_HTTP_TIMEOUT_SECONDS,
    CONST_MIN_BINARY_SIZE_BYTES,
    DEFAULT_REMOTE_VERSION_LIMIT,
    FIRECRACKER_GITHUB_DOWNLOAD_URL,
    FIRECRACKER_GITHUB_RELEASES_API_URL,
    HTTP_TIMEOUT_SHA256_FETCH_S,
    HTTP_USER_AGENT,
)
from mvmctl.core.metadata import set_default_binary_entry, update_binary_entry
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary
from mvmctl.exceptions import AssetNotFoundError, BinaryError, MVMError
from mvmctl.utils.fs import get_bin_dir, get_cache_dir
from mvmctl.utils.progress import download_with_progress

logger = logging.getLogger(__name__)

_CHUNK_SIZE = CONST_MIN_BINARY_SIZE_BYTES * CONST_BUFFER_SIZE_BYTES

GITHUB_RELEASES_URL = FIRECRACKER_GITHUB_RELEASES_API_URL
GITHUB_DOWNLOAD_URL = FIRECRACKER_GITHUB_DOWNLOAD_URL


@dataclass
class BinaryVersion:
    """A locally cached Firecracker/jailer binary pair."""

    version: str
    firecracker_path: Path
    jailer_path: Path
    is_active: bool


def _resolve_bin_dir(bin_dir: Path | None) -> Path:
    d = bin_dir if bin_dir is not None else get_bin_dir()
    d.mkdir(parents=True, exist_ok=True)
    return d


def _normalize_version(version: str) -> str:
    return version.removeprefix("v")


def _parse_version_string(name: str, prefix: str) -> str | None:
    """Extract the version string from a binary filename.

    Given a filename like ``firecracker-v1.2.3`` and the prefix
    ``firecracker-v``, returns ``"1.2.3"``.  Returns *None* when
    *name* does not start with *prefix*.
    """
    if not name.startswith(prefix):
        return None
    return name[len(prefix) :]


def _active_target(symlink: Path) -> str | None:
    if symlink.is_symlink():
        target = os.readlink(symlink)
        return str(target)
    return None


def list_local_versions(bin_dir: Path | None = None) -> list[BinaryVersion]:
    """List locally cached Firecracker/jailer binary pairs.

    **Canonical path (production):** Called without ``bin_dir``. Queries
    SQLite (``MVMDatabase.list_binaries_by_name``) and derives ``is_active``
    from the ``is_default`` column.  This is the only correct path for CLI
    and API callers.

    **Filesystem-discovery path (non-production):** When ``bin_dir`` is
    explicitly provided, falls back to a filesystem scan
    (``_list_local_versions_from_fs``).  Use only for one-time registration
    of manually placed binaries.  Never pass ``bin_dir`` from CLI or API
    code.
    """
    if bin_dir is not None:
        return _list_local_versions_from_fs(bin_dir)

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


def _list_local_versions_from_fs(bin_dir: Path) -> list[BinaryVersion]:
    """Filesystem-scan implementation used for custom bin_dir / tests."""
    d = _resolve_bin_dir(bin_dir)

    fc_symlink = d / "firecracker"
    active_fc_target = _active_target(fc_symlink)

    versions: dict[str, tuple[Path | None, Path | None]] = {}
    for path in d.iterdir():
        if path.is_symlink() or path.is_dir():
            continue
        name = path.name
        fc_ver = _parse_version_string(name, "firecracker-v")
        jl_ver = _parse_version_string(name, "jailer-v")
        if fc_ver is not None:
            fc, jl = versions.get(fc_ver, (None, None))
            versions[fc_ver] = (path, jl)
        elif jl_ver is not None:
            fc, jl = versions.get(jl_ver, (None, None))
            versions[jl_ver] = (fc, path)

    result: list[BinaryVersion] = []
    for ver in sorted(versions, reverse=True):
        fc_path, jl_path = versions[ver]
        if fc_path is None or jl_path is None:
            continue
        is_active = active_fc_target == fc_path.name
        result.append(
            BinaryVersion(
                version=ver,
                firecracker_path=fc_path,
                jailer_path=jl_path,
                is_active=is_active,
            )
        )
    return result


def list_remote_versions(limit: int | None = None) -> list[str]:
    """Fetch recent Firecracker release versions from GitHub."""
    effective_limit = limit if limit is not None else DEFAULT_REMOTE_VERSION_LIMIT
    url = f"{GITHUB_RELEASES_URL}?per_page={effective_limit}"
    req = Request(url, headers={"User-Agent": HTTP_USER_AGENT, "Accept": "application/json"})

    try:
        with urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as response:
            data: list[dict[str, object]] = json.loads(response.read().decode())
    except (URLError, OSError) as exc:
        raise BinaryError(f"Failed to fetch releases from GitHub: {exc}") from exc

    versions: list[str] = []
    for release in data:
        tag = release.get("tag_name")
        if isinstance(tag, str):
            versions.append(_normalize_version(tag))

    def _semver_key(v: str) -> tuple[int, ...]:
        try:
            return tuple(int(x) for x in v.split("."))
        except ValueError:
            return (0,)

    versions.sort(key=_semver_key, reverse=True)
    return versions


def fetch_binary(version: str, bin_dir: Path | None = None) -> BinaryVersion:
    """Download Firecracker and jailer binaries for *version*."""
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_dest = d / f"firecracker-v{version}"
    jl_dest = d / f"jailer-v{version}"

    if fc_dest.exists() and jl_dest.exists():
        _db = MVMDatabase()
        _default = _db.get_default_binary("firecracker")
        active = _default is not None and _normalize_version(_default.version or "") == version
        return BinaryVersion(
            version=version,
            firecracker_path=fc_dest,
            jailer_path=jl_dest,
            is_active=active,
        )

    tgz_url = f"{GITHUB_DOWNLOAD_URL}/v{version}/firecracker-v{version}-x86_64.tgz"
    sha256_url = f"{tgz_url}.sha256.txt"

    expected_sha256: str | None = None
    try:
        req = Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            content = resp.read().decode().strip()
        parts = content.split()
        if parts:
            expected_sha256 = parts[0].lower()
            logger.info("Fetched checksum for Firecracker v%s: %s", version, expected_sha256)
    except (URLError, OSError):
        logger.debug("Could not fetch SHA-256 sidecar for v%s", version)

    if expected_sha256 is None:
        raise BinaryError(f"Checksum required for Firecracker v{version} download")

    tgz_path = d / f"firecracker-v{version}-x86_64.tgz"
    try:
        download_with_progress(
            tgz_url,
            tgz_path,
            title=f"Downloading Firecracker v{version}",
            expected_sha256=expected_sha256,
            timeout=CONST_HTTP_TIMEOUT_SECONDS,
        )
    except MVMError as exc:
        tgz_path.unlink(missing_ok=True)
        raise BinaryError(f"Failed to download Firecracker v{version}: {exc}") from exc

    try:
        with tarfile.open(tgz_path, "r:gz") as tar:
            fc_found = False
            jl_found = False
            for member in tar.getmembers():
                basename = Path(member.name).name
                if basename == f"firecracker-v{version}-x86_64":
                    _extract_member(tar, member, fc_dest)
                    fc_found = True
                elif basename == f"jailer-v{version}-x86_64":
                    _extract_member(tar, member, jl_dest)
                    jl_found = True

            if not fc_found or not jl_found:
                raise BinaryError(f"Archive for v{version} missing expected binaries")
    except tarfile.TarError as exc:
        fc_dest.unlink(missing_ok=True)
        jl_dest.unlink(missing_ok=True)
        raise BinaryError(f"Failed to extract archive: {exc}") from exc
    finally:
        tgz_path.unlink(missing_ok=True)

    db = MVMDatabase()
    no_default = db.get_default_binary("firecracker") is None
    cache_dir = get_cache_dir()
    update_binary_entry(
        cache_dir,
        version,
        full_version=f"v{version}",
        ci_version=f"v{version.split('.')[0]}.{version.split('.')[1]}"
        if len(version.split(".")) >= 2
        else f"v{version}",
        firecracker_path=str(fc_dest),
        jailer_path=str(jl_dest),
        is_default=1 if no_default else 0,
    )

    active = no_default
    if no_default:
        set_active_version(version, d)

    return BinaryVersion(
        version=version,
        firecracker_path=fc_dest,
        jailer_path=jl_dest,
        is_active=active,
    )


def _extract_member(tar: tarfile.TarFile, member: tarfile.TarInfo, dest: Path) -> None:
    reader = tar.extractfile(member)
    if reader is None:
        raise BinaryError(f"Cannot read {member.name} from archive")
    with open(dest, "wb") as out:
        while True:
            chunk = reader.read(_CHUNK_SIZE)
            if not chunk:
                break
            out.write(chunk)
    dest.chmod(dest.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def set_active_version(version: str, bin_dir: Path | None = None) -> None:
    """Create/update symlinks for the active Firecracker version."""
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_src = d / f"firecracker-v{version}"
    jl_src = d / f"jailer-v{version}"

    if not fc_src.exists() or not jl_src.exists():
        raise AssetNotFoundError(
            f"Version {version} not downloaded — run 'mvm bin fetch {version}' first"
        )

    parts = version.split(".")
    ci_version = f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{version}"
    full_version = f"v{version}"
    cache_dir = get_cache_dir()
    update_binary_entry(
        cache_dir,
        version,
        full_version=full_version,
        ci_version=ci_version,
        firecracker_path=str(fc_src),
        jailer_path=str(jl_src),
        is_default=1,
    )
    set_default_binary_entry(cache_dir, version)


def ensure_default_binary(bin_dir: Path | None = None) -> str | None:
    """Set a default binary if none is recorded; return active version or None."""
    db = MVMDatabase()
    existing_default = db.get_default_binary("firecracker")
    if existing_default is not None and existing_default.path:
        return _normalize_version(existing_default.version or "")

    local = list_local_versions(bin_dir)
    if not local:
        return None

    best = local[0]
    set_active_version(best.version, bin_dir)
    return best.version


def get_binary_path(name: str, version: str | None = None) -> str:
    """Return the filesystem path for the named binary.

    Args:
        name:    Binary name, e.g. "firecracker" or "jailer".
        version: Specific version string, e.g. "1.15.0". If None, the binary
                 marked is_default=1 for this name is used.

    Returns:
        Absolute path string to the binary file.

    Raises:
        AssetNotFoundError: If ``version`` is specified but not found locally.
        AssetNotFoundError: If ``version`` is None and no binary for ``name``
                            is marked as default.
        AssetNotFoundError: If the resolved path does not exist on disk
                            (stale/deleted entry).
    """
    db = MVMDatabase()

    if version is not None:
        normalized = _normalize_version(version)
        binaries = db.list_binaries_by_name(name)
        for b in binaries:
            if (
                b.version == normalized
                or b.version == version
                or (b.full_version and b.full_version.removeprefix("v") == normalized)
            ):
                if b.path:
                    if not Path(b.path).exists():
                        raise AssetNotFoundError(
                            f"Binary '{name}' version '{version}' is registered but the file "
                            f"is missing: {b.path} — run 'mvm bin fetch {version}' to re-download it."
                        )
                    return b.path
        raise AssetNotFoundError(
            f"Binary '{name}' version '{version}' not found locally. "
            f"Run 'mvm bin fetch {version}' to download it."
        )

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


def remove_version(version: str, bin_dir: Path | None = None) -> None:
    """Delete a locally cached binary version."""
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_path = d / f"firecracker-v{version}"
    jl_path = d / f"jailer-v{version}"

    if not fc_path.exists() and not jl_path.exists():
        raise AssetNotFoundError(f"Version {version} not found locally")

    fc_link = d / "firecracker"
    jl_link = d / "jailer"

    if fc_link.is_symlink() and os.readlink(fc_link) == fc_path.name:
        fc_link.unlink()
    if jl_link.is_symlink() and os.readlink(jl_link) == jl_path.name:
        jl_link.unlink()

    fc_path.unlink(missing_ok=True)
    jl_path.unlink(missing_ok=True)

    db = MVMDatabase()
    db.delete_binary_by_name_and_version("firecracker", version)
    db.delete_binary_by_name_and_version("jailer", version)
