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
from mvmctl.exceptions import AssetNotFoundError, BinaryError, MVMError
from mvmctl.utils.fs import get_bin_dir
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
    """List locally cached Firecracker/jailer binary pairs from filesystem.

    This function scans the filesystem for binary pairs and returns them.
    The ``is_active`` flag is determined by checking the ``firecracker``
    symlink in the bin directory.

    Args:
        bin_dir: Optional directory to scan. Uses default if None.

    Returns:
        List of BinaryVersion objects sorted by version (newest first).
    """
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


def fetch_binary(
    version: str, bin_dir: Path | None = None, *, set_as_default: bool = False
) -> BinaryVersion:
    """Download Firecracker and jailer binaries for *version*.

    Args:
        version: The Firecracker version to fetch (e.g., "1.15.0").
        bin_dir: Optional directory to store binaries. Uses default if None.
        set_as_default: If True, mark this version as the default binary after download.

    Returns:
        BinaryVersion with paths and active status.
    """
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_dest = d / f"firecracker-v{version}"
    jl_dest = d / f"jailer-v{version}"

    if fc_dest.exists() and jl_dest.exists():
        # If binaries exist, return with active status based on set_as_default
        # or if they're already the active version (detected via symlink)
        fc_symlink = d / "firecracker"
        is_active = set_as_default or (
            fc_symlink.is_symlink() and os.readlink(fc_symlink) == fc_dest.name
        )
        return BinaryVersion(
            version=version,
            firecracker_path=fc_dest,
            jailer_path=jl_dest,
            is_active=is_active,
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

    if set_as_default:
        set_active_version(version, d)

    return BinaryVersion(
        version=version,
        firecracker_path=fc_dest,
        jailer_path=jl_dest,
        is_active=set_as_default,
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
    """Mark a binary version as active in the database.

    Note: Symlink creation has been removed. SQLite is now the canonical
    source of truth for binary defaults (is_default=1 in binaries table).
    """
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_src = d / f"firecracker-v{version}"
    jl_src = d / f"jailer-v{version}"

    if not fc_src.exists() or not jl_src.exists():
        raise AssetNotFoundError(
            f"Version {version} not downloaded — run 'mvm bin fetch {version}' first"
        )


def get_binary_path(name: str, version: str) -> str:
    """Return the filesystem path for the named binary with specific version.

    Args:
        name: Binary name, e.g. "firecracker" or "jailer".
        version: Specific version string, e.g. "1.15.0".

    Returns:
        Absolute path string to the binary file.

    Raises:
        AssetNotFoundError: If the version is not found locally.
        AssetNotFoundError: If the resolved path does not exist on disk.
    """
    normalized = _normalize_version(version)
    d = _resolve_bin_dir(None)

    if name == "firecracker":
        path = d / f"firecracker-v{normalized}"
    elif name == "jailer":
        path = d / f"jailer-v{normalized}"
    else:
        raise AssetNotFoundError(f"Unknown binary name: {name}")

    if not path.exists():
        raise AssetNotFoundError(
            f"Binary '{name}' version '{version}' not found locally. "
            f"Run 'mvm bin fetch {version}' to download it."
        )

    return str(path)


def remove_version(version: str, bin_dir: Path | None = None) -> None:
    """Delete a locally cached binary version.

    This function removes the binary files.
    Note: Symlink removal has been removed. The caller is responsible for
    updating the database state.

    Args:
        version: The version to remove (e.g., "1.15.0").
        bin_dir: Optional directory containing binaries. Uses default if None.

    Raises:
        AssetNotFoundError: If the version is not found locally.
    """
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_path = d / f"firecracker-v{version}"
    jl_path = d / f"jailer-v{version}"

    if not fc_path.exists() and not jl_path.exists():
        raise AssetNotFoundError(f"Version {version} not found locally")

    fc_path.unlink(missing_ok=True)
    jl_path.unlink(missing_ok=True)
