"""Fail-closed release binary identity qualification for the system-test runner."""

from __future__ import annotations

import hashlib
import os
import re
import stat
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path

_CANONICAL_CONTROLLER = Path("/usr/local/bin/mvm")
_SEMVER_CORE_NUMBER = r"(?:0|[1-9][0-9]*)"
_SEMVER_PRERELEASE_ID = (
    r"(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
)
_SEMVER_BUILD_ID = r"[0-9A-Za-z-]+"
_STRICT_SEMVER_PATTERN = re.compile(
    rf"^{_SEMVER_CORE_NUMBER}\.{_SEMVER_CORE_NUMBER}\."
    rf"{_SEMVER_CORE_NUMBER}"
    rf"(?:-{_SEMVER_PRERELEASE_ID}(?:\.{_SEMVER_PRERELEASE_ID})*)?"
    rf"(?:\+{_SEMVER_BUILD_ID}(?:\.{_SEMVER_BUILD_ID})*)?$"
)


@dataclass(frozen=True)
class _PinnedBinary:
    path: Path
    descriptor: int
    metadata: os.stat_result
    controller: bool


@dataclass(frozen=True)
class _BinaryEvidence:
    path: Path
    device: int
    inode: int
    sha256: str
    version: str


def _controller_path(controller_command: str) -> Path:
    if controller_command != str(_CANONICAL_CONTROLLER):
        raise RuntimeError(
            "release qualification requires MVM_BINARY to be exactly "
            f"{_CANONICAL_CONTROLLER}"
        )
    return _CANONICAL_CONTROLLER


def _candidate_path(configured_candidate: Path, candidate: Path) -> Path:
    try:
        configured = configured_candidate.lstat()
    except FileNotFoundError:
        configured = None
    except OSError as exc:
        raise RuntimeError(
            "cannot inspect configured release candidate path "
            f"{configured_candidate}: {exc}"
        ) from exc
    if configured is not None and stat.S_ISLNK(configured.st_mode):
        raise RuntimeError(
            "release candidate path must not be a symlink: "
            f"{configured_candidate}"
        )
    if not candidate.is_absolute() or candidate != candidate.resolve():
        raise RuntimeError(f"release candidate path is not canonical: {candidate}")
    return candidate


def validate_release_build_paths(
    *,
    controller_command: str,
    configured_candidate: Path,
    candidate: Path,
) -> None:
    """Reject controller commands and candidate path aliases before rebuilding."""
    controller = _controller_path(controller_command)
    resolved_candidate = _candidate_path(configured_candidate, candidate)
    if resolved_candidate == controller:
        raise RuntimeError(
            "release candidate and controller have the same canonical path"
        )
    try:
        controller_metadata = controller.lstat()
    except OSError as exc:
        raise RuntimeError(
            f"cannot inspect release controller {controller}: {exc}"
        ) from exc
    _validate_binary_metadata(controller, controller_metadata, controller=True)
    _validate_controller_ancestors(controller)
    try:
        candidate_metadata = resolved_candidate.lstat()
    except FileNotFoundError:
        return
    except OSError as exc:
        raise RuntimeError(
            f"cannot inspect existing release candidate {resolved_candidate}: {exc}"
        ) from exc
    if (candidate_metadata.st_dev, candidate_metadata.st_ino) == (
        controller_metadata.st_dev,
        controller_metadata.st_ino,
    ):
        raise RuntimeError("release candidate and controller share one inode")


def _validate_binary_metadata(
    path: Path,
    metadata: os.stat_result,
    *,
    controller: bool,
) -> None:
    if stat.S_ISLNK(metadata.st_mode):
        raise RuntimeError(f"release binary must not be a symlink: {path}")
    if not stat.S_ISREG(metadata.st_mode):
        raise RuntimeError(f"release binary is not a regular file: {path}")
    mode = stat.S_IMODE(metadata.st_mode)
    if mode & 0o111 == 0:
        raise RuntimeError(f"release binary is not executable: {path}")
    if controller and (metadata.st_uid, metadata.st_gid) != (0, 0):
        raise RuntimeError(
            "release controller must be owned root:root: "
            f"{path} is {metadata.st_uid}:{metadata.st_gid}"
        )
    if controller and mode != 0o755:
        raise RuntimeError(
            f"release controller must have exact mode 0755: {path} is {mode:04o}"
        )


def _validate_ancestor_metadata(path: Path, metadata: os.stat_result) -> None:
    if stat.S_ISLNK(metadata.st_mode):
        raise RuntimeError(f"release controller ancestor is a symlink: {path}")
    if not stat.S_ISDIR(metadata.st_mode):
        raise RuntimeError(f"release controller ancestor is not a directory: {path}")
    if metadata.st_uid != 0:
        raise RuntimeError(f"release controller ancestor is not root-owned: {path}")
    if stat.S_IMODE(metadata.st_mode) & 0o022:
        raise RuntimeError(
            f"release controller ancestor is group/world writable: {path}"
        )


def _validate_controller_ancestors(path: Path) -> None:
    for ancestor in reversed(path.parents):
        try:
            metadata = ancestor.lstat()
        except OSError as exc:
            raise RuntimeError(
                f"cannot inspect release controller ancestor {ancestor}: {exc}"
            ) from exc
        _validate_ancestor_metadata(ancestor, metadata)


def _open_pinned_binary(path: Path, *, controller: bool) -> _PinnedBinary:
    try:
        before = path.lstat()
    except OSError as exc:
        raise RuntimeError(f"cannot inspect release binary {path}: {exc}") from exc
    _validate_binary_metadata(path, before, controller=controller)

    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
    except OSError as exc:
        raise RuntimeError(f"cannot safely open release binary {path}: {exc}") from exc
    try:
        pinned = os.fstat(descriptor)
        _validate_binary_metadata(path, pinned, controller=controller)
        if (before.st_dev, before.st_ino) != (pinned.st_dev, pinned.st_ino):
            raise RuntimeError(f"release binary changed while opening: {path}")
        return _PinnedBinary(path, descriptor, pinned, controller)
    except BaseException:
        os.close(descriptor)
        raise


def _sha256(descriptor: int) -> str:
    with os.fdopen(os.dup(descriptor), "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def _strict_version(label: str, version: str) -> str:
    if _STRICT_SEMVER_PATTERN.fullmatch(version) is None:
        raise RuntimeError(f"{label} is not a strict semantic version: {version!r}")
    return version


def _parse_version_output(label: str, output: str) -> str:
    parts = output.strip().split()
    if len(parts) != 2 or parts[0] != "mvm":
        raise RuntimeError(
            f"{label} did not report an exact strict semantic version: {output!r}"
        )
    try:
        return _strict_version(f"{label} version", parts[1])
    except RuntimeError as exc:
        raise RuntimeError(
            f"{label} did not report a strict semantic version: {output!r}"
        ) from exc


def _probe_version(descriptor: int, label: str) -> str:
    with tempfile.TemporaryDirectory(prefix=f"mvm-{label}-version-") as root:
        isolation_root = Path(root)
        cache_dir = isolation_root / "cache"
        config_dir = isolation_root / "config"
        temp_dir = isolation_root / "tmp"
        cache_dir.mkdir()
        config_dir.mkdir()
        temp_dir.mkdir()
        env = {
            "HOME": str(isolation_root),
            "PATH": "/usr/bin:/bin",
            "LANG": "C",
            "LC_ALL": "C",
            "NO_COLOR": "1",
            "MVM_CACHE_DIR": str(cache_dir),
            "MVM_CONFIG_DIR": str(config_dir),
            "MVM_TEMP_DIR": str(temp_dir),
        }
        result = subprocess.run(
            [f"/proc/self/fd/{descriptor}", "--version"],
            capture_output=True,
            text=True,
            timeout=10,
            env=env,
            pass_fds=(descriptor,),
        )
    if result.returncode != 0:
        detail = result.stderr.strip() or f"rc={result.returncode}"
        raise RuntimeError(f"{label} version probe failed: {detail}")
    return _parse_version_output(label, result.stdout)


def _evidence(binary: _PinnedBinary, label: str) -> _BinaryEvidence:
    return _BinaryEvidence(
        path=binary.path,
        device=binary.metadata.st_dev,
        inode=binary.metadata.st_ino,
        sha256=_sha256(binary.descriptor),
        version=_probe_version(binary.descriptor, label),
    )


def _validate_matching_evidence(
    controller: _BinaryEvidence,
    candidate: _BinaryEvidence,
    requested_version: str,
) -> None:
    controller_version = _strict_version("controller version", controller.version)
    candidate_version = _strict_version("candidate version", candidate.version)
    requested = _strict_version("requested version", requested_version)
    if controller.path == candidate.path:
        raise RuntimeError(
            "release candidate and controller have the same canonical path"
        )
    if (controller.device, controller.inode) == (
        candidate.device,
        candidate.inode,
    ):
        raise RuntimeError("release candidate and controller share one inode")
    if controller.sha256 != candidate.sha256:
        raise RuntimeError("release candidate and controller SHA-256 differ")
    if controller_version != candidate_version:
        raise RuntimeError("release candidate and controller versions differ")
    if requested != candidate_version:
        raise RuntimeError(
            "requested version does not match release binaries: "
            f"requested {requested}, reported {candidate_version}"
        )


def _require_path_unchanged(binary: _PinnedBinary) -> None:
    try:
        current = binary.path.lstat()
    except OSError as exc:
        raise RuntimeError(
            f"release binary path changed during validation: {binary.path}: {exc}"
        ) from exc
    _validate_binary_metadata(
        binary.path,
        current,
        controller=binary.controller,
    )
    if (current.st_dev, current.st_ino) != (
        binary.metadata.st_dev,
        binary.metadata.st_ino,
    ):
        raise RuntimeError(
            f"release binary path changed during validation: {binary.path}"
        )


def verify_release_binary_identity(
    *,
    controller_command: str,
    configured_candidate: Path,
    candidate: Path,
    requested_version: str,
) -> None:
    """Verify installed and candidate binaries are distinct, trusted, and equal."""
    controller_path = _controller_path(controller_command)
    candidate_path = _candidate_path(configured_candidate, candidate)
    if controller_path == candidate_path:
        raise RuntimeError(
            "release candidate and controller have the same canonical path"
        )
    _validate_controller_ancestors(controller_path)

    controller: _PinnedBinary | None = None
    release_candidate: _PinnedBinary | None = None
    try:
        controller = _open_pinned_binary(controller_path, controller=True)
        release_candidate = _open_pinned_binary(candidate_path, controller=False)
        _validate_matching_evidence(
            _evidence(controller, "controller"),
            _evidence(release_candidate, "candidate"),
            requested_version,
        )
        _require_path_unchanged(controller)
        _require_path_unchanged(release_candidate)
    finally:
        if release_candidate is not None:
            os.close(release_candidate.descriptor)
        if controller is not None:
            os.close(controller.descriptor)
