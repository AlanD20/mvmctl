"""Firecracker/jailer binary version management."""

from __future__ import annotations

import hashlib
import json
import logging
import os
import stat
import tarfile
from dataclasses import dataclass
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

from fcm.constants import HTTP_USER_AGENT
from fcm.exceptions import AssetNotFoundError, BinaryError
from fcm.utils.fs import get_bin_dir, get_cache_dir

logger = logging.getLogger(__name__)

GITHUB_RELEASES_URL = "https://api.github.com/repos/firecracker-microvm/firecracker/releases"
GITHUB_DOWNLOAD_URL = "https://github.com/firecracker-microvm/firecracker/releases/download"


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


def _active_target(symlink: Path) -> str | None:
    if symlink.is_symlink():
        target = os.readlink(symlink)
        return str(target)
    return None


def list_local_versions(bin_dir: Path | None = None) -> list[BinaryVersion]:
    """Scan bin_dir for firecracker-vX.X.X and jailer-vX.X.X files."""
    d = _resolve_bin_dir(bin_dir)

    fc_symlink = d / "firecracker"
    active_fc_target = _active_target(fc_symlink)

    versions: dict[str, tuple[Path | None, Path | None]] = {}
    for path in d.iterdir():
        if path.is_symlink() or path.is_dir():
            continue
        name = path.name
        if name.startswith("firecracker-v"):
            ver = name.removeprefix("firecracker-v")
            fc, jl = versions.get(ver, (None, None))
            versions[ver] = (path, jl)
        elif name.startswith("jailer-v"):
            ver = name.removeprefix("jailer-v")
            fc, jl = versions.get(ver, (None, None))
            versions[ver] = (fc, path)

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


def list_remote_versions(limit: int = 10) -> list[str]:
    """Fetch recent Firecracker release versions from GitHub."""
    url = f"{GITHUB_RELEASES_URL}?per_page={limit}"
    req = Request(url, headers={"User-Agent": HTTP_USER_AGENT, "Accept": "application/json"})

    try:
        with urlopen(req, timeout=30) as response:
            data: list[dict[str, object]] = json.loads(response.read().decode())
    except (URLError, OSError) as exc:
        raise BinaryError(f"Failed to fetch releases from GitHub: {exc}") from exc

    versions: list[str] = []
    for release in data:
        tag = release.get("tag_name")
        if isinstance(tag, str):
            versions.append(_normalize_version(tag))
    return versions


def _verify_sha256(version: str, tgz_path: Path, actual_hex: str) -> None:
    """Verify downloaded tarball against GitHub SHA-256 sidecar file."""
    sha_url = f"{GITHUB_DOWNLOAD_URL}/v{version}/firecracker-v{version}-x86_64.tgz.sha256.txt"
    try:
        req = Request(sha_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=30) as resp:
            content = resp.read().decode().strip()
        expected = content.split()[0].lower()
        if actual_hex.lower() != expected:
            tgz_path.unlink(missing_ok=True)
            raise BinaryError(
                f"SHA-256 mismatch for Firecracker v{version}: "
                f"expected {expected}, got {actual_hex}"
            )
        logger.info("SHA-256 verified for Firecracker v%s", version)
    except URLError:
        logger.warning("Could not fetch SHA-256 sidecar for v%s — skipping verification", version)


def fetch_binary(version: str, bin_dir: Path | None = None) -> BinaryVersion:
    """Download Firecracker and jailer binaries for *version*."""
    version = _normalize_version(version)
    d = _resolve_bin_dir(bin_dir)

    fc_dest = d / f"firecracker-v{version}"
    jl_dest = d / f"jailer-v{version}"

    if fc_dest.exists() and jl_dest.exists():
        active = _active_target(d / "firecracker") == fc_dest.name
        return BinaryVersion(
            version=version,
            firecracker_path=fc_dest,
            jailer_path=jl_dest,
            is_active=active,
        )

    tgz_url = f"{GITHUB_DOWNLOAD_URL}/v{version}/firecracker-v{version}-x86_64.tgz"
    req = Request(tgz_url, headers={"User-Agent": HTTP_USER_AGENT})

    tgz_path = d / f"firecracker-v{version}-x86_64.tgz"
    try:
        logger.info("Downloading %s", tgz_url)
        sha256_hash = hashlib.sha256()
        with urlopen(req, timeout=300) as resp, open(tgz_path, "wb") as f:
            while True:
                chunk = resp.read(8192)
                if not chunk:
                    break
                f.write(chunk)
                sha256_hash.update(chunk)
    except (URLError, OSError) as exc:
        tgz_path.unlink(missing_ok=True)
        raise BinaryError(f"Failed to download Firecracker v{version}: {exc}") from exc

    _verify_sha256(version, tgz_path, sha256_hash.hexdigest())

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

    active = _active_target(d / "firecracker") == fc_dest.name
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
            chunk = reader.read(8192)
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
            f"Version {version} not downloaded — run 'fcm asset bin fetch {version}' first"
        )

    for link_name, target in [("firecracker", fc_src.name), ("jailer", jl_src.name)]:
        link = d / link_name
        link.unlink(missing_ok=True)
        link.symlink_to(target)


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
