"""
Binary service — stateless operations coordinator.

Handles download, list, remove, and path resolution for Firecracker binaries.
"""

from __future__ import annotations

import logging
import os
import shutil
import stat
import sys
import tarfile
from datetime import UTC, datetime
from pathlib import Path

from mvmctl.constants import (
    CONST_BUFFER_SIZE_BYTES,
    CONST_HTTP_TIMEOUT_SECONDS,
    CONST_MIN_BINARY_SIZE_BYTES,
    SERVICE_BINARY_NAMES,
)
from mvmctl.constants import (
    FIRECRACKER_GITHUB_DOWNLOAD_URL as _GITHUB_DOWNLOAD_URL,
)
from mvmctl.constants import (
    FIRECRACKER_GITHUB_RELEASES_API_URL as _GITHUB_RELEASES_URL,
)
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.exceptions import BinaryError
from mvmctl.models import BinaryItem
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.http import HttpDownload

logger = logging.getLogger(__name__)

_CHUNK_SIZE = CONST_MIN_BINARY_SIZE_BYTES * CONST_BUFFER_SIZE_BYTES


class BinaryService:
    """Stateless binary operations (download, list, remove, path resolution)."""

    def __init__(self, repo: BinaryRepository) -> None:
        self._repo = repo

    def list_local(self, verify: bool = True) -> list[BinaryItem]:
        """
        List all binaries, syncing is_present flag with filesystem.

        Args:
            verify: If True (default), check filesystem and update DB.
                   If False, return DB records as-is.

        """
        binaries = self._repo.list_all()
        if not verify:
            return binaries

        missing_ids: list[str] = []
        for binary in binaries:
            if not binary.resolved_path.exists():
                missing_ids.append(binary.id)

        if missing_ids:
            self._repo.update_many_is_present(missing_ids, False)
            binaries = self._repo.list_all()

        return binaries

    def get_default_firecracker(self) -> BinaryItem | None:
        """Return the default firecracker binary, or None if not set."""
        return self._repo.get_default("firecracker")

    @staticmethod
    def list_remote(limit: int) -> list[str]:
        """
        Fetch Firecracker release versions from GitHub.

        Args:
            limit: Maximum number of versions to return.

        Returns:
            List of version strings sorted by semver (newest first).

        """
        url = f"{_GITHUB_RELEASES_URL}?per_page={limit}"

        try:
            json_data = HttpDownload.read_json_content(url, use_cache=True)
        except Exception as exc:
            raise BinaryError(
                f"Failed to fetch releases from GitHub: {exc}"
            ) from exc

        if not isinstance(json_data, list):
            raise BinaryError(
                f"Unexpected response from GitHub: expected list, got {type(json_data).__name__}"
            )

        versions: list[str] = []
        for release in json_data:
            if isinstance(release, dict):
                tag = release.get("tag_name")
                if isinstance(tag, str):
                    versions.append(BinaryService._normalize_version(tag))

        versions.sort(key=BinaryService._semver_key, reverse=True)
        return versions

    @staticmethod
    def _semver_key(v: str) -> tuple[int, ...]:
        """
        Convert a semver string to a sortable tuple of integers.

        Args:
            v: Version string like "1.15.0".

        Returns:
            Tuple of integers for sorting. Falls back to (0,) on parse failure.

        """
        try:
            return tuple(int(x) for x in v.split("."))
        except ValueError:
            return (0,)

    @staticmethod
    def download_firecracker(version: str, bin_dir: Path) -> list[BinaryItem]:
        """
        Download firecracker + jailer for version, return as BinaryItem list.

        1. Normalize version (strip 'v' prefix)
        2. Resolve bin_dir
        3. Check if already exists
        4. Fetch SHA256 checksum
        5. Download .tgz with HttpDownload.download_file()
        6. Extract firecracker and jailer binaries
        7. Set executable permissions
        8. Clean up .tgz
        9. Generate IDs and create BinaryItem list (firecracker + jailer)

        Args:
            version: The Firecracker version to fetch (e.g., "1.15.0").
            bin_dir: Optional directory to store binaries. Uses default if None.

        Returns:
            list[BinaryItem] with 2 items (firecracker and jailer).

        Raises:
            BinaryError: If download or extraction fails.

        """
        normalized_version = BinaryService._normalize_version(version)
        d = CacheUtils.resolve_dir(bin_dir)

        fc_dest = d / f"firecracker-v{normalized_version}"
        jl_dest = d / f"jailer-v{normalized_version}"

        tgz_url = f"{_GITHUB_DOWNLOAD_URL}/v{normalized_version}/firecracker-v{normalized_version}-x86_64.tgz"
        sha256_url = f"{tgz_url}.sha256.txt"

        expected_sha256: str | None = None
        try:
            parts = (
                HttpDownload.read_raw_content(sha256_url, use_cache=True)
                .strip()
                .split()
            )
            if parts:
                expected_sha256 = parts[0].lower()
                logger.info(
                    "Fetched checksum for Firecracker v%s: %s",
                    normalized_version,
                    expected_sha256,
                )
        except Exception:
            logger.debug(
                "Could not fetch SHA-256 sidecar for v%s", normalized_version
            )

        if expected_sha256 is None:
            raise BinaryError(
                f"Checksum required for Firecracker v{normalized_version} download"
            )

        tgz_path = d / f"firecracker-v{normalized_version}-x86_64.tgz"
        try:
            HttpDownload.download_file(
                tgz_url,
                tgz_path,
                expected_sha256=expected_sha256,
                timeout=CONST_HTTP_TIMEOUT_SECONDS,
            )
        except Exception as exc:
            tgz_path.unlink(missing_ok=True)
            raise BinaryError(
                f"Failed to download Firecracker v{normalized_version}: {exc}"
            ) from exc

        try:
            with tarfile.open(tgz_path, "r:gz") as tar:
                fc_found = False
                jl_found = False
                for member in tar.getmembers():
                    basename = Path(member.name).name
                    if basename == f"firecracker-v{normalized_version}-x86_64":
                        BinaryService._extract_member_from_tar(
                            tar, member, fc_dest
                        )
                        fc_found = True
                    elif basename == f"jailer-v{normalized_version}-x86_64":
                        BinaryService._extract_member_from_tar(
                            tar, member, jl_dest
                        )
                        jl_found = True

                if not fc_found or not jl_found:
                    raise BinaryError(
                        f"Archive for v{normalized_version} missing expected binaries"
                    )
        except tarfile.TarError as exc:
            fc_dest.unlink(missing_ok=True)
            jl_dest.unlink(missing_ok=True)
            raise BinaryError(f"Failed to extract archive: {exc}") from exc
        finally:
            tgz_path.unlink(missing_ok=True)

        return [
            BinaryService._create_binary_item(
                "firecracker", normalized_version, fc_dest
            ),
            BinaryService._create_binary_item(
                "jailer", normalized_version, jl_dest
            ),
        ]

    def remove(self, binary: BinaryItem, *, force: bool = False) -> BinaryItem:
        """
        Remove a specific binary from disk and database.

        Hard-deletes when no VMs reference the binary.
        Soft-deletes only when VMs still reference it (to preserve history).

        Args:
            binary: The BinaryItem to remove.
            force: If True, remove even if referenced by VMs.

        Returns:
            The removed BinaryItem.

        Raises:
            BinaryError: If binary is referenced by VMs and force is False.

        """
        vms = binary.vms or []
        has_vms = bool(vms)

        if has_vms and not force:
            raise BinaryError(
                f"Binary referenced by VMs: {', '.join(v.name for v in vms)}"
            )

        # Delete file from disk
        binary_path = binary.resolved_path
        if binary_path.exists():
            binary_path.unlink()

        # Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(binary.id)
        else:
            self._repo.delete(binary.id)

        return binary

    def remove_many(
        self, binaries: list[BinaryItem], *, force: bool = False
    ) -> list[BinaryItem]:
        """
        Remove multiple binaries.

        Args:
            binaries: List of BinaryItem to remove.
            force: If True, remove even if referenced by VMs.

        Returns:
            The removed BinaryItem list.

        """
        deleted: list[BinaryItem] = []
        for binary in binaries:
            self.remove(binary, force=force)
            deleted.append(binary)
        return deleted

    @staticmethod
    def _get_embedded_path(name: str) -> Path | None:
        """Return the path to an embedded service binary, or None if not available.

        In compiled mode (Nuitka --onefile), binaries are embedded via
        --include-data-dir and are available relative to the executable.
        In development mode, pre-built binaries don't exist.

        Resolution order:
        1. ``__compiled__.containing_dir`` — Nuitka onefile temp extraction dir
           or standalone dist directory (official Nuitka API).
        2. ``sys.argv[0]`` parent dir — standalone dist layout fallback.
        3. ``NUITKA_TEMP_DIR`` env var — explicit temp directory override.
        """
        from mvmctl.constants import is_compiled_mode

        if not is_compiled_mode():
            return None

        # Strategy 1: Nuitka compiled mode — __compiled__.containing_dir
        # abstracts the temp extraction dir (onefile) or dist dir (standalone).
        try:
            containing_dir = __compiled__.containing_dir  # type: ignore[name-defined] # noqa: F821
            candidate = Path(containing_dir) / "mvmctl" / "services" / name
            if candidate.exists():
                return candidate
        except NameError:
            pass

        # Strategy 2: Relative to sys.executable parent (Nuitka onefile temp dir).
        # In Nuitka --onefile, sys.executable is the python3 in the temp dir
        # (e.g. /tmp/onefile_XXXX/python3), and embedded data is at
        # /tmp/onefile_XXXX/mvmctl/services/<name>.
        exe_parent = Path(sys.executable).parent
        candidate = exe_parent / "mvmctl" / "services" / name
        if candidate.exists():
            return candidate

        # Strategy 3: Relative to sys.argv[0] parent (standalone dist layout)
        base = Path(sys.argv[0]).parent
        candidate = base / "mvmctl" / "services" / name
        if candidate.exists():
            return candidate

        # Strategy 4: Explicit override via env var
        temp_dir = os.environ.get("NUITKA_TEMP_DIR", "")
        if temp_dir:
            candidate = Path(temp_dir) / "mvmctl" / "services" / name
            if candidate.exists():
                return candidate
        return None

    def extract_service_binaries(self) -> list[BinaryItem]:
        """Extract the combined multidist service binary and create symlinks/DB entries.

        The combined ``mvm-services`` binary contains all 3 service entry points.
        At extraction time, we copy this single binary and create symlinks for
        each service name (``mvm-console-relay``, ``mvm-nocloud-server``,
        ``mvm-provision``).

        In development mode (not frozen), this is a no-op — the managers
        fall back to ``sys.executable -m ...`` or guestfs respectively.

        Returns:
            List of BinaryItem records that were extracted.
        """
        from datetime import UTC, datetime

        from mvmctl import __version__ as mvmctl_version
        from mvmctl.constants import CONST_FILE_PERMS_EXECUTABLE
        from mvmctl.utils.common import CacheUtils
        from mvmctl.utils.crypto import HashGenerator

        bin_dir = CacheUtils.get_bin_dir()
        now = datetime.now(UTC).isoformat()
        extracted: list[BinaryItem] = []

        # Copy the combined multidist binary
        combined_name = "mvm-services"
        combined_dest = bin_dir / combined_name

        combined_src = BinaryService._get_embedded_path(combined_name)

        # Step 1: Copy the combined binary (only in compiled mode when embedded)
        if combined_src is not None:
            combined_dest.unlink(missing_ok=True)
            shutil.copy2(str(combined_src), str(combined_dest))
            combined_dest.chmod(CONST_FILE_PERMS_EXECUTABLE)
            logger.info("Extracted combined service binary: %s", combined_name)
        else:
            logger.debug("Combined service binary not embedded, skipping copy")

        # Step 2: Always create symlinks — works in both compiled and dev mode.
        # In dev mode, mvm-services may already exist from a prior build.
        for name in SERVICE_BINARY_NAMES:
            link_path = bin_dir / name
            link_path.unlink(missing_ok=True)
            link_path.symlink_to(combined_name)

        # Step 3: Create DB entries only if the combined binary is available.
        # In dev mode without a prior build, the binary may not exist at the
        # cache path — services fall back to sys.executable -m ... in that case.
        if not combined_dest.exists():
            logger.debug(
                "Combined service binary not found at %s, "
                "skipping DB upsert (dev mode without build)",
                combined_dest,
            )
            return extracted

        for name in SERVICE_BINARY_NAMES:
            sha256 = HashGenerator.binary(combined_dest, name, mvmctl_version)
            item = BinaryItem(
                id=sha256,
                name=name,
                version=mvmctl_version,
                full_version=mvmctl_version,
                ci_version=None,
                path=name,
                is_default=True,
                is_present=True,
                created_at=now,
                updated_at=now,
            )
            self._repo.upsert(item)
            extracted.append(item)
            logger.info("Registered service binary: %s", name)

        return extracted

    @staticmethod
    def _normalize_version(version: str) -> str:
        """Strip 'v' prefix from version."""
        return version.removeprefix("v")

    @staticmethod
    def _parse_version_string(name: str, prefix: str) -> str | None:
        """
        Extract the version string from a binary filename.

        Given a filename like ``firecracker-v1.2.3`` and the prefix
        ``firecracker-v``, returns ``"1.2.3"``.  Returns *None* when
        *name* does not start with *prefix*.
        """
        if not name.startswith(prefix):
            return None
        return name[len(prefix) :]

    @staticmethod
    def _extract_member_from_tar(
        tar: tarfile.TarFile, member: tarfile.TarInfo, dest: Path
    ) -> None:
        """Extract a single member from a tar archive to dest."""
        reader = tar.extractfile(member)
        if reader is None:
            raise BinaryError(f"Cannot read {member.name} from archive")
        with open(dest, "wb") as out:
            while True:
                chunk = reader.read(_CHUNK_SIZE)
                if not chunk:
                    break
                out.write(chunk)
        dest.chmod(
            dest.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH
        )

    @staticmethod
    def _ci_version(version: str) -> str:
        """Generate CI version from full version (e.g. '1.15.0' -> 'v1.15')."""
        parts = version.split(".")
        return f"v{parts[0]}.{parts[1]}" if len(parts) >= 2 else f"v{version}"

    @staticmethod
    def _create_binary_item(
        name: str,
        version: str,
        path: Path,
        *,
        resolve_ci_version: bool = True,
    ) -> BinaryItem:
        """
        Create a single BinaryItem instance.

        Args:
            name: Binary name, e.g. "firecracker" or "jailer".
            version: The version string, e.g. "1.15.0".
            path: Filesystem path to the binary.
            resolve_ci_version: If True, generate ci_version from version.
                                If False, leave ci_version empty.

        Returns:
            A BinaryItem with generated ID and metadata.

        """
        ci_ver = (
            BinaryService._ci_version(version) if resolve_ci_version else None
        )
        now = datetime.now(tz=UTC).isoformat()

        binary_id = HashGenerator.binary(path, name, version)

        return BinaryItem(
            id=binary_id,
            name=name,
            version=version,
            full_version=f"v{version}",
            ci_version=ci_ver,
            path=path.name,
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )


__all__ = ["BinaryService"]
