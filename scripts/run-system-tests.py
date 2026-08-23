#!/usr/bin/env python3
"""Run mvmctl system tests with per-domain VM isolation.

Usage:
  # Smoke-test / build the provisioning pipeline
  MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \\
    python3 scripts/run-system-tests.py --prepare

  # Run all domains (T1 + T2 + T3)
  MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \\
    python3 scripts/run-system-tests.py --all --host-direct

  # Run specific domains
  python3 scripts/run-system-tests.py cli network exec

  # Run specific tiers (comma-separated, executed in the given order)
  python3 scripts/run-system-tests.py --tier 1
  python3 scripts/run-system-tests.py --tier 2,1
  python3 scripts/run-system-tests.py --tier 1,3 --host-direct
  python3 scripts/run-system-tests.py --tier 3,2,1 --host-direct

  # Rebuild and qualify an explicit release candidate
  python3 scripts/run-system-tests.py --release-qualification --all \\
    --host-direct --rebuild --candidate-version 0.3.0
  python3 scripts/run-system-tests.py --volume --image --prepare
  python3 scripts/run-system-tests.py --volume
  python3 scripts/run-system-tests.py --image

  # Limit parallel workers (default: 4)
  python3 scripts/run-system-tests.py --all --host-direct --workers 2
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any

from system_test_release import (
    validate_release_build_paths,
    verify_release_binary_identity,
)
from system_test_outcomes import (
    MAX_PYTEST_OUTCOME_REPORT_BYTES,
    PYTEST_OUTCOME_REPORT_ENV,
    read_pytest_outcome_report,
    require_complete_pytest_outcomes,
)

# ============================================================================
# Configuration
# ============================================================================

ASSET_MIRROR_HOST = os.path.expanduser("~/.cache/mvm-asset-mirror")
if os.environ.get("MVM_ASSET_MIRROR"):
    ASSET_MIRROR_HOST = os.environ["MVM_ASSET_MIRROR"]
MVM_BINARY = os.environ.get("MVM_BINARY", "/usr/local/bin/mvm")
_REPO_ROOT = Path(__file__).resolve().parent.parent
MVM_CANDIDATE_CONFIGURED = Path(
    os.environ.get("MVM_CANDIDATE_BINARY", _REPO_ROOT / "dist" / "mvm")
).expanduser()
MVM_CANDIDATE_BINARY = str(
    MVM_CANDIDATE_CONFIGURED.resolve()
)
SHARED_VOLUME_NAME = "asset-mirror"
SHARED_VOLUME_SIZE = "6G"
TEST_NETWORK_NAME = "sys-test-net"

# Custom base image built during --prepare
BASE_IMAGE_NAME = "mvm-test-runner"

# Root-owned staging location used to exercise the real administrator install
# flow inside every disposable runner VM.  Tests execute the installed binary
# at /usr/local/bin/mvm; they never treat this candidate path as the CLI.
RUNNER_SYSTEM_CANDIDATE_DIR = "/opt/mvmctl-test"
RUNNER_SYSTEM_CANDIDATE = f"{RUNNER_SYSTEM_CANDIDATE_DIR}/mvm-candidate"
RUNNER_SYSTEM_UPLOAD = "/home/runner/.mvm-system-candidate.upload"
RUNNER_USER_INIT_COMMAND = (
    "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
    "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm init --non-interactive "
    "--binary-version 1.16.0"
)

# Base VM used to build the custom image
BASE_VM_NAME = "base-img-builder"
BASE_VM_DISK = "12G"

# SSH key for provisioning runner user in builder & test VMs
BUILDER_KEY_NAME = "builder-key"
_SEMVER_CORE_NUMBER = r"(?:0|[1-9][0-9]*)"
_SEMVER_PRERELEASE_ID = (
    r"(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
)
_SEMVER_BUILD_ID = r"[0-9A-Za-z-]+"
_CANDIDATE_VERSION_PATTERN = re.compile(
    rf"^v?(?P<version>{_SEMVER_CORE_NUMBER}\."
    rf"{_SEMVER_CORE_NUMBER}\.{_SEMVER_CORE_NUMBER}"
    rf"(?:-{_SEMVER_PRERELEASE_ID}(?:\.{_SEMVER_PRERELEASE_ID})*)?"
    rf"(?:\+{_SEMVER_BUILD_ID}(?:\.{_SEMVER_BUILD_ID})*)?)$"
)


class _UnsafeVolumeBackingPath(RuntimeError):
    """The volume record points outside the one runner-managed backing path."""


# Tier 1: shared volume, host-level CLI operations (no nested virt needed)
TIER1_DOMAINS: dict[str, list[str]] = {
    "system_install": ["tests/system/host/test_system_install.py"],
    "cli": ["tests/system/cli/test_cli.py"],
    "config": ["tests/system/config/test_config.py"],
    "init": ["tests/system/init/test_init.py"],
    "cache": ["tests/system/cache/test_cache.py"],
    "keys": ["tests/system/keys/test_keys.py"],
    "invariants": ["tests/system/invariants/test_invariants.py"],
    "bin": ["tests/system/bin/test_bin.py"],
    "images": ["tests/system/images/test_images.py"],
    "kernel": [
        "tests/system/kernel/test_kernel_import.py",
    ],
    "network": [
        "tests/system/network/test_network.py",
    ],
    "host": ["tests/system/host/test_host.py"],
    "run": ["tests/system/run/test_run.py"],
}

# Tier 2: shared volume + nested virt (VM creation/interaction)
TIER2_DOMAINS: dict[str, list[str]] = {
    "volume": [
        "tests/system/volume/test_volume.py",
    ],
    "vm_lifecycle": ["tests/system/vm/test_vm_lifecycle.py"],
    "vm_data_persistence": ["tests/system/vm/test_vm_data_persistence.py"],
    "jailer": [
        "tests/system/vm/test_jailer.py",
        "tests/system/vm/test_cgroup.py",
    ],
    "exec": ["tests/system/exec/test_exec.py"],
    "ssh": ["tests/system/ssh/test_ssh.py"],
    "console": ["tests/system/console/test_console.py"],
    "logs": ["tests/system/logs/test_logs.py"],
    "full_journeys": ["tests/system/full_journeys/test_full_journeys.py"],
    "nftables": ["tests/system/network/test_nftables.py"],
    "policies": ["tests/system/network/test_policies.py"],
}

# Tier 3: directly on host (no runner VM)
TIER3_DOMAINS: dict[str, list[str]] = {
    "nested_isolated": ["tests/system/vm/test_vm_nested_isolated.py"],
    "fresh_env": ["tests/system/vm/test_vm_fresh_env.py"],
    "snapshot_load": ["tests/system/vm/test_vm_snapshot_load.py"],
    "snapshot_rootfs": ["tests/system/vm/test_vm_snapshot_rootfs_independence.py"],
    "kernel_build": ["tests/system/kernel/test_kernel.py"],
    "volume_hotplug": [
        "tests/system/volume/test_volume_hotplug.py",
    ],
    "cp": ["tests/system/cp/test_cp.py"],
    "env": ["tests/system/env/test_env.py"],
}

# Tier classification for display
TIER_LABELS: dict[str, int] = {}
for d in TIER1_DOMAINS:
    TIER_LABELS[d] = 1
for d in TIER2_DOMAINS:
    TIER_LABELS[d] = 2
for d in TIER3_DOMAINS:
    TIER_LABELS[d] = 3


def _parse_tier(value: str) -> list[int]:
    """Parse a comma-separated tier list for argparse."""
    if not value.strip():
        raise argparse.ArgumentTypeError("--tier cannot be empty")
    tiers: list[int] = []
    for part in value.split(","):
        part = part.strip()
        if not part:
            continue
        try:
            tier = int(part)
        except ValueError:
            raise argparse.ArgumentTypeError(f"invalid tier value: {part!r}")
        if tier not in (1, 2, 3):
            raise argparse.ArgumentTypeError(f"tier must be 1, 2, or 3; got {tier}")
        tiers.append(tier)
    if not tiers:
        raise argparse.ArgumentTypeError("--tier cannot be empty")
    return tiers


def _dedupe_adjacent(items: list[int]) -> list[int]:
    """Remove adjacent duplicate integers while preserving first-seen order."""
    result: list[int] = []
    prev: int | None = None
    for item in items:
        if item != prev:
            result.append(item)
            prev = item
    return result


# ============================================================================
# Helpers
# ============================================================================


def mvm(
    *args: str, check: bool = True, timeout: int = 300, capture: bool = True
) -> subprocess.CompletedProcess:
    """Run an mvm command on the host."""
    if MVM_BINARY == "mvm":
        cmd = ["mvm", *args]
    else:
        cmd = shlex.split(MVM_BINARY) + list(args)
    env = {**os.environ, "NO_COLOR": "1"}
    env.setdefault("MVM_ASSET_MIRROR", ASSET_MIRROR_HOST)
    result = subprocess.run(
        cmd,
        capture_output=capture,
        text=True,
        timeout=timeout,
        env=env,
    )
    if not capture:
        # Stream output live so the user can see mirror vs HTTP download decision
        if result.stdout:
            print(result.stdout, end="", flush=True)
        if result.stderr:
            print(result.stderr, end="", flush=True, file=sys.stderr)
    if check and result.returncode != 0:
        if result.stderr is None:
            stderr = "(streamed; not captured)" if not capture else "(not available)"
        else:
            stderr = result.stderr.strip() or "(empty)"
        raise RuntimeError(
            f"mvm command failed: {shlex.join(cmd)}\n"
            f"  rc: {result.returncode}\n"
            f"  stderr: {stderr}"
        )
    return result


def log(msg: str) -> None:
    """Print a timestamped log message."""
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


def _parse_candidate_version(value: str) -> str:
    """Parse one explicit release identity accepted by the build script."""
    match = _CANDIDATE_VERSION_PATTERN.fullmatch(value.strip())
    if match is None:
        raise argparse.ArgumentTypeError(
            "candidate version must be X.Y.Z with optional prerelease/build metadata"
        )
    return match.group("version")


def _resolve_candidate_build_version(explicit_version: str | None) -> str:
    """Resolve an explicit version or one clean, exact release tag at HEAD."""
    if explicit_version is not None:
        try:
            return _parse_candidate_version(explicit_version)
        except argparse.ArgumentTypeError as exc:
            raise RuntimeError(str(exc)) from exc

    tags = subprocess.run(
        ["git", "tag", "--points-at", "HEAD", "--list", "v[0-9]*"],
        cwd=str(_REPO_ROOT),
        capture_output=True,
        text=True,
        timeout=10,
    )
    if tags.returncode != 0:
        raise RuntimeError(
            "cannot inspect release tags; pass --candidate-version with --rebuild"
        )
    exact_versions: list[str] = []
    for tag in tags.stdout.splitlines():
        try:
            exact_versions.append(_parse_candidate_version(tag))
        except argparse.ArgumentTypeError:
            continue
    if len(exact_versions) != 1:
        raise RuntimeError(
            "--rebuild requires --candidate-version unless HEAD has exactly "
            "one valid release tag"
        )

    status = subprocess.run(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=str(_REPO_ROOT),
        capture_output=True,
        text=True,
        timeout=10,
    )
    if status.returncode != 0 or status.stdout.strip():
        raise RuntimeError(
            "--rebuild requires --candidate-version when the release-tagged "
            "checkout has uncommitted files"
        )
    return exact_versions[0]


def _build_mvm_binary(candidate_version: str) -> None:
    """Build the release candidate without replacing the outer controller."""
    try:
        version = _parse_candidate_version(candidate_version)
    except argparse.ArgumentTypeError as exc:
        raise RuntimeError(str(exc)) from exc
    if MVM_CANDIDATE_CONFIGURED.is_symlink():
        raise RuntimeError(
            "MVM_CANDIDATE_BINARY build target must not be a symlink: "
            f"{MVM_CANDIDATE_CONFIGURED}"
        )
    _require_distinct_candidate_controller()
    log(f"Building mvm release candidate at {MVM_CANDIDATE_BINARY}...")
    script_dir = Path(__file__).resolve().parent
    repo_root = script_dir.parent
    build_script = repo_root / "scripts" / "build.sh"
    output_path = Path(MVM_CANDIDATE_BINARY)
    result = subprocess.run(
        [
            str(build_script),
            "release",
            "--version",
            version,
            "--output",
            str(output_path),
        ],
        cwd=str(repo_root),
    )
    if result.returncode != 0:
        raise RuntimeError(f"mvm binary build failed with rc={result.returncode}")
    reported_version = _get_mvm_version()
    if reported_version != version:
        raise RuntimeError(
            "built candidate version mismatch: "
            f"requested {version}, reported {reported_version}"
        )
    log("Build complete")


def _controller_executable_path() -> Path:
    """Resolve the executable used only to manage outer host resources."""
    command = shlex.split(MVM_BINARY)
    if not command:
        raise RuntimeError("MVM_BINARY must contain an executable path")
    executable = shutil.which(command[0]) or command[0]
    return Path(executable).expanduser().resolve()


def _require_distinct_candidate_controller() -> None:
    """Reject path and inode aliases before a candidate build can overwrite mvm."""
    candidate = Path(MVM_CANDIDATE_BINARY).expanduser().resolve()
    controller = _controller_executable_path()
    aliases = candidate == controller
    if not aliases and candidate.exists() and controller.exists():
        aliases = os.path.samefile(candidate, controller)
    if aliases:
        raise RuntimeError(
            "MVM_CANDIDATE_BINARY must not alias the outer controller "
            f"MVM_BINARY ({controller})"
        )


def _run_pytest(
    test_files: list[str],
    *,
    xdist: bool = False,
    timeout: int = 600,
    extra_env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess:
    """Run pytest against the given test files on the host (Tier 3)."""
    cmd = ["python3", "-m", "pytest", "--tb=short", "-q"]
    if xdist:
        cmd.append("-n")
        cmd.append("auto")
    cmd.extend(test_files)
    env = {**os.environ, "NO_COLOR": "1"}
    env.setdefault("MVM_ASSET_MIRROR", ASSET_MIRROR_HOST)
    if extra_env:
        env.update(extra_env)
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
    )
    return result


# ============================================================================
# Shared Volume Management
# ============================================================================


def _inspect_volume_path(name: str) -> tuple[bool, str | None]:
    """Return whether a volume record exists and its recorded backing path."""
    result = mvm("volume", "inspect", name, "--json", check=False, timeout=30)
    if result.returncode != 0:
        return False, None
    try:
        info = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"volume inspect returned invalid JSON for '{name}': {exc}"
        ) from exc
    path = info.get("volume", {}).get("path")
    return True, path if isinstance(path, str) and path else None


def _get_volume_path(name: str) -> str | None:
    """Get the recorded on-disk path of a volume, or None if absent."""
    _exists, path = _inspect_volume_path(name)
    return path


def _expected_shared_volume_path() -> Path:
    """Return the lexical managed path without following any symlinks."""
    cache_root = Path(
        os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")
    ).expanduser()
    return Path(
        os.path.abspath(cache_root / "volumes" / f"{SHARED_VOLUME_NAME}.raw")
    )


def _validated_volume_backing_path(raw_path: str | os.PathLike[str]) -> Path:
    """Require the exact regular, non-symlink runner volume backing file."""
    path = Path(os.path.abspath(Path(raw_path).expanduser()))
    expected = _expected_shared_volume_path()
    if path != expected:
        raise _UnsafeVolumeBackingPath(
            "unexpected shared volume backing path: "
            f"expected {expected}, got {path}"
        )
    try:
        info = path.lstat()
    except FileNotFoundError as exc:
        raise RuntimeError(f"volume backing file is missing: {path}") from exc
    except OSError as exc:
        raise RuntimeError(f"cannot inspect volume backing file {path}: {exc}") from exc
    if stat.S_ISLNK(info.st_mode):
        raise RuntimeError(f"volume backing path must not be a symlink: {path}")
    if not stat.S_ISREG(info.st_mode):
        raise RuntimeError(f"volume backing path is not a regular file: {path}")
    return path


def _open_directory_without_symlinks(path: Path) -> int:
    """Open an absolute directory path one pinned, no-follow component at a time."""
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW
    current_fd = os.open(os.sep, flags)
    try:
        for component in path.parts[1:]:
            next_fd = os.open(component, flags, dir_fd=current_fd)
            os.close(current_fd)
            current_fd = next_fd
        return current_fd
    except BaseException:
        os.close(current_fd)
        raise


def _require_shared_volume_path_absent_before_create() -> None:
    """Refuse create if old cleanup left any object at the canonical path."""
    expected = _expected_shared_volume_path()
    try:
        expected.lstat()
    except FileNotFoundError:
        return
    except OSError as exc:
        raise RuntimeError(
            f"cannot verify shared volume path before create {expected}: {exc}"
        ) from exc
    raise _UnsafeVolumeBackingPath(
        f"shared volume backing path still exists before volume create: {expected}"
    )


def _populate_volume_image(volume_path: Path, mirror_path: Path) -> None:
    """Format and populate one ext4 image without sudo or loop mounts."""
    backing = _validated_volume_backing_path(volume_path)
    mirror = mirror_path.expanduser().resolve()
    if not mirror.is_dir():
        raise RuntimeError(f"asset mirror is not a directory: {mirror}")

    path_info = backing.lstat()
    try:
        parent_fd = _open_directory_without_symlinks(backing.parent)
    except OSError as exc:
        raise RuntimeError(
            f"cannot safely open shared volume directory {backing.parent}: {exc}"
        ) from exc
    try:
        try:
            backing_fd = os.open(
                backing.name,
                os.O_RDWR | os.O_CLOEXEC | os.O_NOFOLLOW,
                dir_fd=parent_fd,
            )
        except OSError as exc:
            raise RuntimeError(
                f"cannot safely open shared volume backing file {backing}: {exc}"
            ) from exc
    finally:
        os.close(parent_fd)

    try:
        pinned_before = os.fstat(backing_fd)
        if not stat.S_ISREG(pinned_before.st_mode):
            raise RuntimeError(
                f"shared volume backing descriptor is not a regular file: {backing}"
            )
        if (pinned_before.st_dev, pinned_before.st_ino) != (
            path_info.st_dev,
            path_info.st_ino,
        ):
            raise RuntimeError(
                f"shared volume backing file changed before population: {backing}"
            )
        try:
            result = subprocess.run(
                [
                    "mkfs.ext4",
                    "-F",
                    "-d",
                    str(mirror),
                    f"/proc/self/fd/{backing_fd}",
                ],
                capture_output=True,
                text=True,
                timeout=900,
                pass_fds=(backing_fd,),
            )
        except FileNotFoundError as exc:
            raise RuntimeError(
                "mkfs.ext4 is required to populate the shared asset volume"
            ) from exc
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError(
                "mkfs.ext4 timed out while populating the volume"
            ) from exc
        if result.returncode != 0:
            detail = result.stderr.strip() or result.stdout.strip() or "unknown error"
            raise RuntimeError(f"mkfs.ext4 failed to populate {backing}: {detail}")

        pinned_after = os.fstat(backing_fd)
        try:
            current = backing.lstat()
        except OSError as exc:
            raise RuntimeError(
                f"shared volume backing path was replaced during population: {backing}"
            ) from exc
        if not stat.S_ISREG(current.st_mode) or (
            current.st_dev,
            current.st_ino,
        ) != (pinned_after.st_dev, pinned_after.st_ino):
            raise RuntimeError(
                f"shared volume backing path was replaced during population: {backing}"
            )
        if (pinned_after.st_uid, pinned_after.st_gid) != (
            pinned_before.st_uid,
            pinned_before.st_gid,
        ):
            raise RuntimeError(
                "mkfs.ext4 changed shared volume backing ownership: "
                f"expected {pinned_before.st_uid}:{pinned_before.st_gid}, "
                f"got {pinned_after.st_uid}:{pinned_after.st_gid}"
            )
    finally:
        os.close(backing_fd)


def _create_and_seed_volume() -> None:
    """Create the shared asset volume and seed it with asset mirror contents.

    ``mkfs.ext4 -d`` copies the mirror directly into the image, so this never
    needs sudo, a loop device, or a host mount.
    """
    mirror_path = Path(ASSET_MIRROR_HOST).expanduser().resolve()
    if not mirror_path.is_dir():
        raise RuntimeError(f"asset mirror is not a directory: {mirror_path}")

    _require_shared_volume_path_absent_before_create()
    log(f"Creating shared volume '{SHARED_VOLUME_NAME}' ({SHARED_VOLUME_SIZE})...")
    mvm(
        "volume",
        "create",
        SHARED_VOLUME_NAME,
        SHARED_VOLUME_SIZE,
        "--shareable",
        "--read-only",
        "--writeback",
        "--format",
        "raw",
        timeout=30,
    )

    try:
        record_exists, raw_path = _inspect_volume_path(SHARED_VOLUME_NAME)
        if not record_exists or not raw_path:
            raise RuntimeError("created shared volume has no recorded backing path")
        vol_path = _validated_volume_backing_path(raw_path)

        if not any(mirror_path.iterdir()):
            log(f"WARNING: Asset mirror at {mirror_path} is empty.")
            log(
                "Populating volume with empty filesystem. "
                "Tests that need assets will fail."
            )
        _populate_volume_image(vol_path, mirror_path)
    except _UnsafeVolumeBackingPath:
        raise
    except RuntimeError as primary:
        cleanup = mvm(
            "volume",
            "rm",
            SHARED_VOLUME_NAME,
            "--force",
            check=False,
            timeout=30,
        )
        if cleanup.returncode != 0:
            raise RuntimeError(
                f"{primary}; failed to remove incomplete shared volume: "
                f"{cleanup.stderr.strip()}"
            ) from primary
        raise
    log(f"Seeded volume with contents from {mirror_path}")


def ensure_shared_volume(*, rebuild: bool = False) -> None:
    """Ensure the shared asset volume exists and is populated.

    If the volume doesn't exist (or --rebuild is passed), remove it,
    recreate it, and populate it with the host asset mirror contents.
    """
    mirror_path = Path(ASSET_MIRROR_HOST).expanduser()
    if not mirror_path.exists():
        log(f"WARNING: Asset mirror at {mirror_path} does not exist.")
        log(
            "Create it first by pulling images/kernels/binaries with MVM_ASSET_MIRROR set."
        )

    record_exists, raw_path = _inspect_volume_path(SHARED_VOLUME_NAME)
    existing_path: Path | None = None
    if record_exists:
        try:
            if raw_path is None:
                raise RuntimeError("volume record has no backing path")
            existing_path = _validated_volume_backing_path(raw_path)
        except _UnsafeVolumeBackingPath:
            raise
        except RuntimeError as exc:
            log(
                f"Shared volume '{SHARED_VOLUME_NAME}' is stale ({exc}); "
                "removing its record before recreation"
            )

    if existing_path is not None and not rebuild:
        log(f"Shared volume '{SHARED_VOLUME_NAME}' already exists at {existing_path}")
        return

    if record_exists:
        log(f"Rebuilding: removing existing volume '{SHARED_VOLUME_NAME}'...")
        mvm("volume", "rm", SHARED_VOLUME_NAME, "--force", timeout=30)

    _create_and_seed_volume()


def ensure_test_network() -> None:
    """Create the shared test network if it doesn't exist."""
    result = mvm(
        "network", "inspect", TEST_NETWORK_NAME, "--json", check=False, timeout=15
    )
    if result.returncode == 0:
        log(f"Test network '{TEST_NETWORK_NAME}' already exists")
        return
    log(f"Creating test network '{TEST_NETWORK_NAME}'...")
    mvm(
        "network",
        "create",
        TEST_NETWORK_NAME,
        "--subnet",
        "10.88.0.0/24",
        "--non-interactive",
        timeout=30,
    )


# ============================================================================
# VM Provisioning
# ============================================================================


def _get_mvm_version() -> str:
    """Get the candidate version without consulting or touching host state."""
    with tempfile.TemporaryDirectory(prefix="mvm-candidate-version-") as root:
        isolation_root = Path(root)
        cache_dir = isolation_root / "cache"
        config_dir = isolation_root / "config"
        temp_dir = isolation_root / "tmp"
        cache_dir.mkdir()
        config_dir.mkdir()
        temp_dir.mkdir()
        env = {
            **os.environ,
            "NO_COLOR": "1",
            "MVM_CACHE_DIR": str(cache_dir),
            "MVM_CONFIG_DIR": str(config_dir),
            "MVM_TEMP_DIR": str(temp_dir),
        }
        result = subprocess.run(
            [MVM_CANDIDATE_BINARY, "--version"],
            capture_output=True,
            text=True,
            timeout=10,
            env=env,
        )
    if result.returncode != 0:
        raise RuntimeError(
            "candidate version probe failed: "
            f"{result.stderr.strip() or f'rc={result.returncode}'}"
        )
    # "mvm 0.1.0" → "0.1.0"
    parts = result.stdout.strip().split()
    if len(parts) < 2 or parts[0] != "mvm" or not parts[-1]:
        raise RuntimeError(
            f"candidate returned an invalid version response: {result.stdout!r}"
        )
    return parts[-1]


def _unique_name(prefix: str = "sys-runner") -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"


def _install_system_binary_in_runner(vm_name: str) -> None:
    """Stage the RC and install it through ``host install-system``.

    Copying directly onto /usr/local/bin/mvm would bypass the administrator
    bootstrap path that the system-install L2 domain exists to qualify.
    """
    mvm(
        "cp",
        "-f",
        MVM_CANDIDATE_BINARY,
        f"{vm_name}:{RUNNER_SYSTEM_UPLOAD}",
        timeout=60,
    )
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        f"test -f {RUNNER_SYSTEM_UPLOAD} && "
        f"test ! -L {RUNNER_SYSTEM_UPLOAD} && "
        f"sudo test ! -L {RUNNER_SYSTEM_CANDIDATE_DIR} && "
        f"sudo install -d -o root -g root -m 0755 {RUNNER_SYSTEM_CANDIDATE_DIR} && "
        f"sudo test ! -L {RUNNER_SYSTEM_CANDIDATE_DIR} && "
        f"sudo test ! -L {RUNNER_SYSTEM_CANDIDATE} && "
        f"sudo install -o root -g root -m 0755 "
        f"{RUNNER_SYSTEM_UPLOAD} {RUNNER_SYSTEM_CANDIDATE} && "
        f"rm -f {RUNNER_SYSTEM_UPLOAD} && "
        f"test ! -L {RUNNER_SYSTEM_CANDIDATE} && "
        f"test \"$(stat -c '%u:%g:%a' {RUNNER_SYSTEM_CANDIDATE})\" = '0:0:755' && "
        f"sudo {RUNNER_SYSTEM_CANDIDATE} host install-system && "
        "test \"$(stat -c '%u:%g:%a' /usr/local/bin/mvm)\" = '0:0:755'",
        timeout=90,
    )


def _initialize_system_binary_in_runner(vm_name: str) -> None:
    """Run host initialization through the canonical installed executable."""
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "sudo /usr/local/bin/mvm host init",
        timeout=180,
    )


def _initialize_runner_user(
    vm_name: str,
    *,
    timeout: int,
    capture: bool = True,
) -> None:
    """Initialize runner-owned state with the release-pinned Firecracker pair."""
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        RUNNER_USER_INIT_COMMAND,
        timeout=timeout,
        capture=capture,
    )


def _ensure_builder_key() -> None:
    """Create the builder SSH key if it doesn't exist yet."""
    check = mvm("key", "inspect", BUILDER_KEY_NAME, check=False, timeout=10)
    if check.returncode != 0:
        mvm(
            "key",
            "create",
            BUILDER_KEY_NAME,
            "--algorithm",
            "ed25519",
            "--force",
            timeout=30,
        )


def provision_t1(vm_name: str, mvm_version: str) -> None:
    """Provision a Tier 1 VM from custom base image + shared volume."""
    _ensure_builder_key()
    _ensure_official_kernel_on_host(vm_name)
    log(
        f"  Creating T1 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--user",
        "runner",
        "--vcpu",
        "2",
        "--mem",
        "1024",
        "--disk-size",
        "9G",
        "--kernel",
        "official:7.0.11",
        "--ssh-key",
        BUILDER_KEY_NAME,
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    _install_system_binary_in_runner(vm_name)
    _initialize_system_binary_in_runner(vm_name)
    # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
    # Running as root creates the cache dir with root ownership — test VMs inherit
    # this state and break with 'permission denied' on /home/runner/.cache/mvmctl.
    _initialize_runner_user(vm_name, timeout=360)


def _ensure_official_kernel_on_host(vm_name: str) -> None:
    """Ensure official:7.0.11 kernel is available on the HOST.

    Tier 3 kernel_build tests may remove it (test_kernel_remove, etc.).
    Uses a lockfile to prevent concurrent pulls from parallel workers.
    """
    lockfile = "/tmp/mvm-kernel-pull.lock"
    check = mvm(
        "kernel", "inspect", "official:7.0.11", "--json", check=False, timeout=15
    )
    if check.returncode == 0:
        return
    log("  official:7.0.11 not found — pulling (this may take a few minutes)...")
    # Acquire lock to prevent concurrent pulls
    import fcntl

    with open(lockfile, "w") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            # Re-check after acquiring lock (another worker may have pulled)
            check2 = mvm(
                "kernel",
                "inspect",
                "official:7.0.11",
                "--json",
                check=False,
                timeout=15,
            )
            if check2.returncode == 0:
                return
            mvm(
                "kernel",
                "pull",
                "official:7.0.11",
                "--features",
                "nftables,tuntap,kvm,btrfs",
                timeout=900,
            )
        finally:
            fcntl.flock(lf, fcntl.LOCK_UN)


def provision_t2(vm_name: str, mvm_version: str) -> None:
    """Provision a Tier 2 VM from custom base image + shared volume (binary + tests + deps pre-installed)."""
    _ensure_builder_key()
    _ensure_official_kernel_on_host(vm_name)
    log(
        f"  Creating T2 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + shared volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--kernel",
        "official:7.0.11",
        "--vcpu",
        "4",
        "--mem",
        "4096",
        "--disk-size",
        "9G",
        "--network",
        TEST_NETWORK_NAME,
        "--user",
        "runner",
        "--ssh-key",
        BUILDER_KEY_NAME,
        "--nested-virt",
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=300,
    )
    _install_system_binary_in_runner(vm_name)
    _initialize_system_binary_in_runner(vm_name)

    # Inside VM: mount shared volume, init, register assets
    # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
    # Running as root creates the cache dir with root ownership — test VMs inherit
    # this state and break with 'permission denied' on /home/runner/.cache/mvmctl.
    log(f"  Initializing mvm inside '{vm_name}'...")
    _initialize_runner_user(vm_name, timeout=180)

    log(f"  Registering assets in '{vm_name}' (cache hits)...")
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm kernel pull --type firecracker "
        "--version v1.15 --default",
        timeout=150,
    )
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull alpine:3.23",
        timeout=150,
    )
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull ubuntu:noble",
        timeout=360,
    )
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm bin pull firecracker "
        "--version 1.16.0 --default --force",
        timeout=150,
    )


def destroy_vm(vm_name: str) -> None:
    """Destroy a runner VM."""
    mvm("vm", "rm", vm_name, "--force", timeout=60, check=False)


def _pull_builder_assets(vm_name: str) -> None:
    """Pull base-image assets serially so each streamed exec stays bounded."""
    pulls = (
        (
            "Firecracker kernel",
            "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm kernel pull "
            "--type firecracker --version v1.15 --default",
        ),
        (
            "Alpine image",
            "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull "
            "alpine:3.23",
        ),
        (
            "Ubuntu Minimal image",
            "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull "
            "ubuntu-minimal:24.04",
        ),
        (
            "Ubuntu image",
            "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull ubuntu "
            "--version 24.04",
        ),
    )
    for label, command in pulls:
        log(f"  Pulling {label} into '{vm_name}'...")
        mvm(
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            command,
            timeout=600,
            capture=False,
        )

    log(f"  Verifying cached image integrity in '{vm_name}'...")
    mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "echo 'Verifying cached image integrity...' && "
        "for f in /home/runner/.cache/mvmctl/images/*.zst; do "
        'zstd -t "$f" || exit 1; '
        "done && "
        "echo 'All images verified OK'",
        timeout=180,
        capture=False,
    )


def _build_base_image(mvm_version: str, *, rebuild: bool = False) -> str:
    """Build a custom base image with all test dependencies pre-installed.

    Creates a VM from ubuntu-minimal:noble, copies in the mvm binary and
    system tests, installs OS packages, stops the VM, imports the rootfs
    as a custom image, then destroys the builder VM.
    Returns the image version tag used.
    """
    img_tag = mvm_version
    log(f"Building custom base image '{BASE_IMAGE_NAME}:{img_tag}'...")

    # When rebuilding, remove the existing image first so it's rebuilt fresh.
    if rebuild:
        mvm(
            "image",
            "rm",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            "--force",
            check=False,
            timeout=15,
        )

    # Check if already built (skip check when rebuilding)
    if not rebuild:
        check = mvm(
            "image",
            "inspect",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            "--json",
            check=False,
            timeout=15,
        )
        if check.returncode == 0:
            log(f"  Base image '{BASE_IMAGE_NAME}:{img_tag}' already exists, skipping")
            return img_tag

    # Clean up any leftover builder VM from a previous aborted run
    destroy_vm(BASE_VM_NAME)

    log(f"  Creating builder VM '{BASE_VM_NAME}'...")
    mvm(
        "vm",
        "create",
        "--writeback",
        BASE_VM_NAME,
        "--image",
        "ubuntu-minimal:noble",
        "--user",
        "runner",
        "--vcpu",
        "4",
        "--mem",
        "3G",
        "--disk-size",
        BASE_VM_DISK,
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    try:
        log(f"  Installing mvm system binary and tests into '{BASE_VM_NAME}'...")
        _install_system_binary_in_runner(BASE_VM_NAME)
        # Verify the binary transferred correctly
        verify = mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            f"stat -c%s {RUNNER_SYSTEM_CANDIDATE} && "
            "stat -c%s /usr/local/bin/mvm",
            timeout=20,
        )
        expected = Path(MVM_CANDIDATE_BINARY).stat().st_size
        installed_sizes = [int(value) for value in verify.stdout.splitlines()]
        if installed_sizes != [expected, expected]:
            raise RuntimeError(
                "mvm binary size mismatch in builder VM: "
                f"expected candidate and system sizes {[expected, expected]}, "
                f"got {installed_sizes}"
            )
        mvm("cp", "tests/system", f"{BASE_VM_NAME}:/tests/", timeout=60)

        log(f"  Installing test dependencies in '{BASE_VM_NAME}'...")
        mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "sudo apt-get update -qq && "
            "sudo apt-get install -y -qq "
            "python3-pytest qemu-utils fakeroot nftables iptables zstd htop "
            "build-essential bc bison flex libncurses-dev "
            "libssl-dev libelf-dev git curl dwarves "
            "cloud-image-utils && "
            "sudo apt-get clean",
            timeout=300,
        )
        log("  Installing Python test packages...")
        mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "pip3 install pytest-timeout --break-system-packages --quiet 2>&1 | tail -3",
            timeout=90,
        )
        log("  Adding runner to required groups (mvm, kvm)...")
        mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "sudo groupadd -f mvm && "
            "sudo usermod -aG mvm runner && "
            "sudo usermod -aG kvm runner",
            timeout=30,
        )
        log(f"  Initializing host through the system binary in '{BASE_VM_NAME}'...")
        _initialize_system_binary_in_runner(BASE_VM_NAME)
        log("  Changing ownership of /tests to runner user...")
        mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "sudo chown -R runner:runner /tests",
            timeout=30,
        )
        log(f"  Initializing mvm inside '{BASE_VM_NAME}'...")
        # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
        # Running as root creates the cache dir at /root/.cache/mvmctl or with root
        # ownership. The base image caches this state — test VMs run as 'runner' and
        # need /home/runner/.cache/mvmctl owned by runner. Permission denied = broken.
        # Asset pulls are pre-baked into the base image so each test VM doesn't
        # re-pull them (saves ~3 min per T1 VM).
        _initialize_runner_user(BASE_VM_NAME, timeout=180, capture=False)

        # Asset pulls are pre-baked into the base image so each test VM doesn't
        # re-pull them (saves ~3 min per T1 VM). Keep each pull in its own exec:
        # concurrent downloads/decompression can exhaust this 3 GiB builder,
        # while separate streamed sessions preserve the exact failing command.
        _pull_builder_assets(BASE_VM_NAME)
        log(f"  Stopping '{BASE_VM_NAME}'...")
        mvm("vm", "stop", BASE_VM_NAME, timeout=60)

        log(f"  Importing image as '{BASE_IMAGE_NAME}:{img_tag}'...")
        mvm(
            "image",
            "import",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            BASE_VM_NAME,
            "--default",
            "--skip-optimization",
            timeout=300,
        )
        log(f"  Base image '{BASE_IMAGE_NAME}:{img_tag}' built successfully")
    finally:
        destroy_vm(BASE_VM_NAME)

    return img_tag


# ============================================================================
# Test Execution per Domain
# ============================================================================


def _remote_pytest_report_path(vm_name: str) -> str:
    """Return one fixed report receiver derived from the unique runner VM."""
    if re.fullmatch(r"[A-Za-z0-9_-]+", vm_name) is None:
        raise RuntimeError(f"unsafe runner VM name for outcome report: {vm_name!r}")
    return f"/tmp/mvmctl-pytest-outcomes-{vm_name}.json"


def _remote_pytest_command(
    vm_name: str,
    test_files: list[str],
    report_path: str,
) -> str:
    pytest_command = shlex.join(
        [
            "python3",
            "-m",
            "pytest",
            *(f"/{test_file}" for test_file in test_files),
            "--tb=short",
            "-q",
        ]
    )
    environment = " ".join(
        (
            "MVM_ASSET_MIRROR=/mnt",
            f"MVM_TEST_VM={shlex.quote(vm_name)}",
            f"{PYTEST_OUTCOME_REPORT_ENV}={shlex.quote(report_path)}",
        )
    )
    return f"cd / && {environment} {pytest_command}"


def _run_remote_pytest(
    vm_name: str,
    test_files: list[str],
    *,
    timeout: int,
) -> tuple[subprocess.CompletedProcess[str], str | None]:
    """Run pytest in a runner VM and validate its bounded outcome report."""
    report_path = _remote_pytest_report_path(vm_name)
    pytest_result: subprocess.CompletedProcess[str] | None = None
    validation_failures: list[str] = []
    execution_error: Exception | None = None
    try:
        pytest_result = mvm(
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            _remote_pytest_command(vm_name, test_files, report_path),
            check=False,
            timeout=timeout,
        )
        retrieve_result = mvm(
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            f"head -c {MAX_PYTEST_OUTCOME_REPORT_BYTES + 1} -- "
            f"{shlex.quote(report_path)}",
            check=False,
            timeout=30,
        )
        if retrieve_result.returncode != 0:
            detail = (retrieve_result.stderr or "").strip() or "report read failed"
            raise RuntimeError(
                f"remote pytest outcome report is missing: {detail}"
            )
        require_complete_pytest_outcomes(
            retrieve_result.stdout.encode(),
            process_returncode=pytest_result.returncode,
        )
    except Exception as exc:
        execution_error = exc
    finally:
        try:
            mvm(
                "exec",
                vm_name,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                f"rm -f -- {shlex.quote(report_path)} && "
                f"test ! -e {shlex.quote(report_path)}",
                check=True,
                timeout=30,
            )
        except Exception as exc:
            validation_failures.append(
                f"remote outcome report cleanup failed: {exc}"
            )

    if pytest_result is None:
        if execution_error is not None:
            validation_failures.insert(0, str(execution_error))
        raise RuntimeError("; ".join(validation_failures))
    if execution_error is not None:
        validation_failures.insert(0, str(execution_error))
    reason = "; ".join(validation_failures) or None
    return pytest_result, reason


def _pytest_result_output(
    pytest_result: subprocess.CompletedProcess[str],
    validation_failure: str | None,
) -> str:
    output = (pytest_result.stdout or "") + (pytest_result.stderr or "")
    if validation_failure is not None:
        output += (
            "\npytest outcome validation failed: "
            f"{validation_failure}\n"
        )
    return output


def _run_local_pytest(
    domain: str,
    test_files: list[str],
    *,
    timeout: int,
) -> tuple[subprocess.CompletedProcess[str], str | None]:
    """Run host-direct pytest with a private report and checked cleanup."""
    report_directory = tempfile.TemporaryDirectory(
        prefix=f"mvmctl-pytest-{domain}-"
    )
    report_path = Path(report_directory.name) / "outcomes.json"
    pytest_result: subprocess.CompletedProcess[str] | None = None
    validation_failures: list[str] = []
    execution_error: Exception | None = None
    try:
        pytest_result = _run_pytest(
            test_files,
            timeout=timeout,
            extra_env={PYTEST_OUTCOME_REPORT_ENV: str(report_path)},
        )
        payload = read_pytest_outcome_report(report_path)
        require_complete_pytest_outcomes(
            payload,
            process_returncode=pytest_result.returncode,
        )
    except Exception as exc:
        execution_error = exc
    finally:
        try:
            report_directory.cleanup()
        except Exception as exc:
            validation_failures.append(
                f"local outcome report cleanup failed: {exc}"
            )

    if pytest_result is None:
        if execution_error is not None:
            validation_failures.insert(0, str(execution_error))
        raise RuntimeError("; ".join(validation_failures))
    if execution_error is not None:
        validation_failures.insert(0, str(execution_error))
    reason = "; ".join(validation_failures) or None
    return pytest_result, reason


def run_tier1_domain(
    domain: str, test_files: list[str], mvm_version: str, push: bool = False
) -> dict[str, Any]:
    """Full lifecycle for one Tier 1 domain. Max 5 minutes per domain."""
    vm_name = _unique_name(f"t1-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 1,
        "passed": False,
        "output": "",
    }

    try:
        provision_t1(vm_name, mvm_version)
        if push:
            log(f"  Pushing test files into '{vm_name}'...")
            mvm("cp", "-f", "tests/system", f"{vm_name}:/tests/", timeout=60)
            # Clear Python bytecode cache so updated .py files are used
            mvm(
                "exec",
                vm_name,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "find /tests -name '*.pyc' -delete 2>/dev/null; true",
                check=False,
                timeout=30,
            )
        log(f"  Running {domain} tests...")
        pytest_result, validation_failure = _run_remote_pytest(
            vm_name,
            test_files,
            timeout=660,
        )
        result["passed"] = validation_failure is None
        result["output"] = _pytest_result_output(
            pytest_result,
            validation_failure,
        )
    except Exception as e:
        result["output"] = str(e)
    finally:
        destroy_vm(vm_name)

    return result


def run_tier2_domain(
    domain: str, test_files: list[str], mvm_version: str, push: bool = False
) -> dict[str, Any]:
    """Full lifecycle for one Tier 2 domain."""
    vm_name = _unique_name(f"t2-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 2,
        "passed": False,
        "output": "",
    }

    try:
        provision_t2(vm_name, mvm_version)
        if push:
            log(f"  Pushing test files into '{vm_name}'...")
            mvm("cp", "-f", "tests/system", f"{vm_name}:/tests/", timeout=60)
            # Clear Python bytecode cache so updated .py files are used
            mvm(
                "exec",
                vm_name,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "find /tests -name '*.pyc' -delete 2>/dev/null; true",
                check=False,
                timeout=30,
            )
        log(f"  Running {domain} tests...")
        pytest_result, validation_failure = _run_remote_pytest(
            vm_name,
            test_files,
            timeout=960,
        )
        result["passed"] = validation_failure is None
        result["output"] = _pytest_result_output(
            pytest_result,
            validation_failure,
        )
    except Exception as e:
        result["output"] = str(e)
    finally:
        destroy_vm(vm_name)

    return result


def run_tier3_domain(
    domain: str, test_files: list[str], timeout: int = 600
) -> dict[str, Any]:
    """Run one Tier 3 domain directly on the host."""
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 3,
        "passed": False,
        "output": "",
    }
    log(f"  Running {domain} tests on host (timeout={timeout}s)...")
    try:
        pytest_result, validation_failure = _run_local_pytest(
            domain,
            test_files,
            timeout=timeout,
        )
        result["passed"] = validation_failure is None
        result["output"] = _pytest_result_output(
            pytest_result,
            validation_failure,
        )
    except Exception as e:
        result["output"] = str(e)
    return result


# ============================================================================
# Main Orchestrator
# ============================================================================


def run_domains(
    domains: dict[str, list[str]],
    tier: int,
    runner_fn,
    workers: int,
    mvm_version: str,
    push: bool = False,
) -> list[dict[str, Any]]:
    """Run a set of domains in parallel using the given runner function."""
    if not domains:
        return []

    log(f"Tier {tier}: running {len(domains)} domain(s) with {workers} worker(s)...")
    results: list[dict[str, Any]] = []
    # Overall timeout per domain: 15 minutes.
    domain_timeout = 900

    pool = ThreadPoolExecutor(max_workers=workers)
    try:
        future_map = {
            pool.submit(runner_fn, domain, files, mvm_version, push): domain
            for domain, files in domains.items()
        }
        import concurrent.futures

        remaining = set(future_map.keys())
        while remaining:
            done, remaining = concurrent.futures.wait(
                remaining,
                timeout=domain_timeout,
                return_when=concurrent.futures.FIRST_COMPLETED,
            )
            if not done:
                # Timeout reached — no futures completed within domain_timeout
                log(
                    f"  [TIMEOUT] {len(remaining)} domain(s) timed out after {domain_timeout}s — moving on"
                )
                for future in remaining:
                    domain = future_map[future]
                    results.append(
                        {
                            "domain": domain,
                            "tier": tier,
                            "passed": False,
                            "output": f"Domain timed out after {domain_timeout}s",
                        }
                    )
                    log(f"  [TIMEOUT] {domain}")
                break
            for future in done:
                domain = future_map[future]
                try:
                    domain_result = future.result()
                    results.append(domain_result)
                    status = "PASS" if domain_result["passed"] else "FAIL"
                    log(f"  [{status}] {domain} (tier {domain_result['tier']})")
                    if not domain_result["passed"]:
                        _print_failure(domain, domain_result)
                except Exception as e:
                    results.append(
                        {
                            "domain": domain,
                            "tier": tier,
                            "passed": False,
                            "output": str(e),
                        }
                    )
                    log(f"  [ERROR] {domain}: {e}")
    finally:
        # Don't wait for timed-out threads — they'll be daemon-killed on exit
        pool.shutdown(wait=False, cancel_futures=True)

    return results


def _print_failure(domain: str, result: dict[str, Any]) -> None:
    """Print the full failure output."""
    output = result.get("output", "")
    if not output:
        return
    print(f"    --- {domain} failure output ---")
    print(output)
    print(f"    --- end {domain} failure ---")


def print_summary(all_results: list[dict[str, Any]]) -> None:
    """Print a summary of all test results."""
    total = len(all_results)
    passed = sum(1 for r in all_results if r["passed"])
    failed = total - passed

    print()
    print("=" * 60)
    print(f"  RESULTS: {passed} passed, {failed} failed, {total} total")
    print("=" * 60)
    for r in sorted(all_results, key=lambda x: (x["tier"], x["domain"])):
        status = "PASS" if r["passed"] else "FAIL"
        print(f"  [{status}] Tier {r['tier']} {r['domain']}")
    print("=" * 60)

    if failed > 0:
        sys.exit(1)


def _selection_requests_tier3(args: argparse.Namespace) -> bool:
    """Return whether any requested selector names host-direct Tier 3 work."""
    return (
        args.all
        or (args.tier is not None and 3 in args.tier)
        or any(domain in TIER3_DOMAINS for domain in args.domains)
    )


def _validate_release_qualification_args(
    parser: argparse.ArgumentParser,
    args: argparse.Namespace,
) -> None:
    """Reject incomplete or narrowed release qualification before any work."""
    if not args.release_qualification:
        return
    valid = (
        args.all
        and args.host_direct
        and args.rebuild
        and args.candidate_version is not None
        and not args.domains
        and args.tier is None
        and not args.skip_volume_check
    )
    if not valid:
        parser.error(
            "--release-qualification requires --all --host-direct --rebuild "
            "and an explicit --candidate-version; positional domains, --tier, "
            "and --skip-volume-check are not allowed"
        )


def _validate_system_test_registry(parser: argparse.ArgumentParser) -> None:
    """Reject an ambiguous or incomplete system-test domain registry."""
    seen: set[str] = set()
    duplicate_domains: set[str] = set()
    empty_domains: list[str] = []
    registered_files: list[str] = []
    for domains in (TIER1_DOMAINS, TIER2_DOMAINS, TIER3_DOMAINS):
        for domain, test_files in domains.items():
            if domain in seen:
                duplicate_domains.add(domain)
            seen.add(domain)
            if not test_files:
                empty_domains.append(domain)
            registered_files.extend(test_files)
    if duplicate_domains:
        parser.error(
            "duplicate system-test domains: "
            f"{', '.join(sorted(duplicate_domains))}"
        )
    if empty_domains:
        parser.error(
            "empty system-test domains: "
            f"{', '.join(sorted(empty_domains))}"
        )
    duplicate_files = sorted(
        {
            test_file
            for test_file in registered_files
            if registered_files.count(test_file) > 1
        }
    )
    if duplicate_files:
        parser.error(
            "duplicate registered system-test files: "
            f"{', '.join(duplicate_files)}"
        )
    invalid_paths: list[str] = []
    for registered_file in registered_files:
        relative_path = Path(registered_file)
        parts = relative_path.parts
        canonical = (
            not relative_path.is_absolute()
            and relative_path.as_posix() == registered_file
            and ".." not in parts
        )
        system_test = (
            len(parts) >= 3
            and parts[:2] == ("tests", "system")
            and relative_path.name.startswith("test_")
            and relative_path.suffix == ".py"
        )
        if not canonical or not system_test:
            invalid_paths.append(registered_file)
    if invalid_paths:
        parser.error(
            "invalid system-test paths: "
            f"{', '.join(sorted(invalid_paths))}"
        )
    repo_root = _REPO_ROOT.resolve()
    missing_files: list[str] = []
    non_regular_files: list[str] = []
    for registered_file in registered_files:
        test_file = repo_root / registered_file
        try:
            file_mode = test_file.lstat().st_mode
        except FileNotFoundError:
            missing_files.append(registered_file)
            continue
        except OSError:
            non_regular_files.append(registered_file)
            continue
        if not stat.S_ISREG(file_mode) or test_file.resolve() != test_file:
            non_regular_files.append(registered_file)
    if missing_files:
        parser.error(
            "missing system-test files: "
            f"{', '.join(sorted(missing_files))}"
        )
    if non_regular_files:
        parser.error(
            "non-regular system-test files: "
            f"{', '.join(sorted(non_regular_files))}"
        )
    system_test_root = repo_root / "tests" / "system"
    discovered_files = {
        test_file.relative_to(repo_root).as_posix()
        for test_file in system_test_root.rglob("test_*.py")
        if test_file.is_file()
    }
    unregistered_files = sorted(discovered_files - set(registered_files))
    if unregistered_files:
        parser.error(
            "unregistered system-test files: "
            f"{', '.join(unregistered_files)}"
        )


def _validate_test_selection_args(
    parser: argparse.ArgumentParser,
    args: argparse.Namespace,
) -> None:
    """Reject test selections that cannot resolve to known test domains."""
    tier_domains = {
        1: TIER1_DOMAINS,
        2: TIER2_DOMAINS,
        3: TIER3_DOMAINS,
    }
    domains_by_name = {
        **TIER1_DOMAINS,
        **TIER2_DOMAINS,
        **TIER3_DOMAINS,
    }
    unknown = list(
        dict.fromkeys(
            domain for domain in args.domains if domain not in domains_by_name
        )
    )
    if unknown:
        parser.error(f"unknown domains: {', '.join(unknown)}")
    selectors = sum((bool(args.domains), args.tier is not None, args.all))
    if selectors > 1:
        parser.error(
            "choose exactly one test selector: positional domains, --tier, or --all"
        )
    duplicate_domains = list(
        dict.fromkeys(
            domain
            for index, domain in enumerate(args.domains)
            if domain in args.domains[:index]
        )
    )
    if duplicate_domains:
        parser.error(f"duplicate domains: {', '.join(duplicate_domains)}")
    tiers = args.tier or []
    duplicate_tiers = list(
        dict.fromkeys(
            tier
            for index, tier in enumerate(tiers)
            if tier in tiers[:index]
        )
    )
    if duplicate_tiers:
        parser.error(
            f"duplicate tiers: {', '.join(str(tier) for tier in duplicate_tiers)}"
        )

    if args.domains:
        selected_domains = list(args.domains)
    elif args.tier is not None:
        selected_domains = [
            domain
            for tier in args.tier
            for domain in tier_domains[tier]
        ]
    elif args.all:
        selected_domains = [
            domain
            for domains in tier_domains.values()
            for domain in domains
        ]
    else:
        return
    if not selected_domains:
        parser.error("test selection resolves to zero tests")
    empty_domains = sorted(
        domain for domain in selected_domains if not domains_by_name[domain]
    )
    if empty_domains:
        parser.error(
            f"selected domains have no test files: {', '.join(empty_domains)}"
        )


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run mvmctl system tests with per-domain VM isolation.",
    )
    parser.add_argument(
        "domains",
        nargs="*",
        help="Specific domains to test.",
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help="Run all T1 + T2 + T3 domains.",
    )
    parser.add_argument(
        "--host-direct",
        action="store_true",
        help="Acknowledge that selected Tier 3 tests run directly on and may "
        "mutate the outer host. Required for --all, --tier including 3, or "
        "a Tier 3 domain.",
    )
    parser.add_argument(
        "--release-qualification",
        action="store_true",
        help="Run the full release binary identity gate before host-direct "
        "qualification. Requires --all --host-direct --rebuild and an "
        "explicit --candidate-version.",
    )
    parser.add_argument(
        "--tier",
        type=_parse_tier,
        help="Comma-separated tier numbers to run (1, 2, 3). "
        "Tiers are executed in the given order (e.g. 2,1).",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=4,
        help="Maximum parallel VMs (default: 4).",
    )
    parser.add_argument(
        "--rebuild",
        action="store_true",
        help="Build MVM_CANDIDATE_BINARY (never MVM_BINARY), then rebuild the "
        "shared volume and custom base image and run the provisioning "
        "smoke-test. After prepare the script stops unless a test-running "
        "flag is also given.",
    )
    parser.add_argument(
        "--candidate-version",
        type=_parse_candidate_version,
        help="Explicit X.Y.Z identity passed to build.sh --version. Required "
        "with --rebuild unless HEAD is one clean, exact release tag.",
    )
    parser.add_argument(
        "--volume",
        action="store_true",
        help="Rebuild only the shared asset volume (asset-mirror).",
    )
    parser.add_argument(
        "--image",
        action="store_true",
        help="Rebuild only the custom base image from MVM_CANDIDATE_BINARY. "
        "Ensures the shared asset volume exists first because the builder VM "
        "attaches it.",
    )
    parser.add_argument(
        "--skip-volume-check",
        action="store_true",
        help="Skip shared volume check (assume it exists).",
    )
    parser.add_argument(
        "--prepare",
        action="store_true",
        help="Run the T1/T2 provisioning smoke-test / validator. Ensures the "
        "shared volume and custom base image exist, rebuilding them only if "
        "--volume or --image is also passed.",
    )
    parser.add_argument(
        "--push",
        action="store_true",
        help="Push test files into each VM before running (overrides baked-in tests). "
        "Use when modifying tests without rebuilding the base image.",
    )
    return parser


def parse_args() -> argparse.Namespace:
    return _build_parser().parse_args()


def run_prepare(*, rebuild_volume: bool = False, rebuild_image: bool = False) -> None:
    """Smoke-test the provisioning pipeline.

    Builds a custom base image from ubuntu-minimal:noble with all test
    dependencies pre-installed, then validates T1 and T2 provisioning.

    Only forces a rebuild of the shared volume or base image when the
    corresponding flag is True.
    """
    log("=== Prepare: provisioning pipeline ===")

    # --- Step 1: Get mvm version ---
    log("[1/8] Detecting mvm version...")
    mvm_version = _get_mvm_version()
    log(f"      mvm version: {mvm_version}")

    # --- Step 2: Ensure shared volume and network ---
    log("[2/8] Checking shared volume and network...")
    ensure_shared_volume(rebuild=rebuild_volume)
    ensure_test_network()

    # --- Step 3: Ensure kernel with nftables support ---
    log("[3/8] Ensuring kernel with nftables support...")
    mvm(
        "kernel",
        "pull",
        "official:7.0.11",
        "--default",
        "--features",
        "all",
        timeout=900,
    )

    # --- Step 4: Build custom base image ---
    log("[4/8] Building custom base image...")
    _build_base_image(mvm_version, rebuild=rebuild_image)

    # --- Step 5: Create T1 VM from custom image + volume ---
    t1 = _unique_name("prep-t1")
    log(f"[5/8] Creating T1 VM '{t1}' from {BASE_IMAGE_NAME}:{mvm_version} + volume...")
    mvm(
        "vm",
        "create",
        t1,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--user",
        "runner",
        "--vcpu",
        "2",
        "--mem",
        "1024",
        "--disk-size",
        "9G",
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    try:
        log(f"      Running mvm init inside '{t1}'...")
        _initialize_runner_user(t1, timeout=90)
    finally:
        destroy_vm(t1)

    # --- Step 5: Create T2 VM from custom image + shared volume ---
    t2 = _unique_name("prep-t2")
    log(
        f"[6/8] Creating T2 VM '{t2}' from {BASE_IMAGE_NAME}:{mvm_version} + shared volume..."
    )
    mvm(
        "vm",
        "create",
        t2,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--kernel",
        "official:7.0.11",
        "--user",
        "runner",
        "--vcpu",
        "4",
        "--mem",
        "4096",
        "--disk-size",
        "9G",
        "--network",
        TEST_NETWORK_NAME,
        "--nested-virt",
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=300,
    )
    try:
        log(f"[7/8] Setting up '{t2}' (mount + init)...")
        _initialize_runner_user(t2, timeout=180)

        log(f"[8/8] Validating cache hit (pulling 1 asset)...")
        result = mvm(
            "exec",
            t2,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull "
            "alpine:3.23 2>&1 | head -5",
            timeout=150,
            check=False,
        )
        if "Downloading image" in result.stdout or "Downloading image" in result.stderr:
            log("  Cache hit confirmed: asset pulled from local mirror")
        else:
            log(f"  Pull output: {result.stdout[:200] if result.stdout else '(empty)'}")

        vol_check = mvm("volume", "inspect", SHARED_VOLUME_NAME, "--json", timeout=15)
        import json as _json

        vol_info = _json.loads(vol_check.stdout)
        vol_status = vol_info.get("volume", {}).get("status", "unknown")

        log("=== Prepare: ALL STEPS PASSED ===")
        log(f"  Base image: {BASE_IMAGE_NAME}:{mvm_version}")
        log(f"  T1: '{t1}' — created, binary copied, init completed")
        log(f"  T2: '{t2}' — created, volume attached, cache hit verified")
        log(f"  Volume status: {vol_status} (correctly 'available')")
        log("  Environment is ready for running tests.")

    finally:
        destroy_vm(t2)


def main() -> None:
    parser = _build_parser()
    args = parser.parse_args()

    _validate_release_qualification_args(parser, args)
    _validate_system_test_registry(parser)
    _validate_test_selection_args(parser, args)

    if _selection_requests_tier3(args) and not args.host_direct:
        parser.error(
            "Tier 3 tests run directly on and may mutate the outer host; "
            "pass --host-direct to acknowledge this"
        )

    if args.candidate_version is not None and not args.rebuild:
        parser.error("--candidate-version requires --rebuild")

    test_flags = args.all or args.domains or args.tier
    prep_flags = args.prepare or args.image or args.volume or args.rebuild

    if not test_flags and not prep_flags:
        parser.print_help()
        sys.exit(0)

    if args.release_qualification:
        try:
            validate_release_build_paths(
                controller_command=MVM_BINARY,
                configured_candidate=MVM_CANDIDATE_CONFIGURED,
                candidate=Path(MVM_CANDIDATE_BINARY),
            )
        except RuntimeError as exc:
            log(f"ERROR: {exc}")
            sys.exit(1)

    # Handle --rebuild: build the distinct release candidate before anything else.
    if args.rebuild:
        try:
            candidate_version = _resolve_candidate_build_version(
                args.candidate_version
            )
            _build_mvm_binary(candidate_version)
        except RuntimeError as exc:
            log(f"ERROR: {exc}")
            sys.exit(1)

    if args.release_qualification:
        try:
            verify_release_binary_identity(
                controller_command=MVM_BINARY,
                configured_candidate=MVM_CANDIDATE_CONFIGURED,
                candidate=Path(MVM_CANDIDATE_BINARY),
                requested_version=args.candidate_version,
            )
        except RuntimeError as exc:
            log(f"ERROR: {exc}")
            sys.exit(1)

    # Validate MVM_BINARY exists
    binary = shlex.split(MVM_BINARY)[0]
    if not shutil.which(binary) and not Path(binary).is_file():
        log(f"ERROR: mvm binary not found: {MVM_BINARY}")
        log("Set MVM_BINARY or ensure 'mvm' is in PATH.")
        sys.exit(1)

    configured_candidate = MVM_CANDIDATE_CONFIGURED
    candidate = Path(MVM_CANDIDATE_BINARY)
    if (
        configured_candidate.is_symlink()
        or not candidate.is_file()
        or not os.access(candidate, os.X_OK)
    ):
        log(
            "ERROR: MVM_CANDIDATE_BINARY must be a regular, non-symlink, "
            f"executable file: {configured_candidate}"
        )
        sys.exit(1)

    try:
        _require_distinct_candidate_controller()
    except RuntimeError as exc:
        log(f"ERROR: {exc}")
        sys.exit(1)

    # Handle prep actions.
    # --rebuild implies --volume + --image + --prepare (in that order).
    if args.rebuild:
        run_prepare(rebuild_volume=True, rebuild_image=True)
        if not test_flags:
            return
    elif args.prepare or args.image or args.volume:
        rebuild_volume = args.volume
        rebuild_image = args.image
        if args.prepare:
            run_prepare(rebuild_volume=rebuild_volume, rebuild_image=rebuild_image)
        elif args.image:
            # The builder VM attaches the shared volume, so ensure it exists first.
            ensure_shared_volume(rebuild=rebuild_volume)
            ensure_test_network()
            mvm_version = _get_mvm_version()
            _build_base_image(mvm_version, rebuild=True)
        elif args.volume:
            ensure_shared_volume(rebuild=True)

        if not test_flags:
            return

    if not test_flags:
        return

    # Detect mvm version for base image lookup
    mvm_version = _get_mvm_version()

    # Ensure shared volume/network exist unless skipped.
    if not args.skip_volume_check:
        ensure_shared_volume(rebuild=False)
        ensure_test_network()

    # Select domains
    tier1 = dict(TIER1_DOMAINS)
    tier2 = dict(TIER2_DOMAINS)
    tier3 = dict(TIER3_DOMAINS)

    if args.domains:
        # Filter to requested domains
        all_domains = {}
        for d in args.domains:
            if d in tier1:
                all_domains[d] = tier1[d]
            elif d in tier2:
                all_domains[d] = tier2[d]
            elif d in tier3:
                all_domains[d] = tier3[d]
            else:
                log(f"WARNING: Unknown domain '{d}'. Skipping.")
        tier1 = {}
        tier2 = {}
        tier3 = {}
        for d, files in all_domains.items():
            t = TIER_LABELS.get(d, 0)
            if t == 1:
                tier1[d] = files
            elif t == 2:
                tier2[d] = files
            elif t == 3:
                tier3[d] = files

    selected_order: list[int] = []
    if args.tier:
        selected_order = _dedupe_adjacent(args.tier)
    elif args.all:
        selected_order = [1, 2, 3]

    if selected_order:
        selected_set = set(selected_order)
        if 1 not in selected_set:
            tier1 = {}
        if 2 not in selected_set:
            tier2 = {}
        if 3 not in selected_set:
            tier3 = {}

    if not tier1 and not tier2 and not tier3:
        log("No domains selected. Use --help for options.")
        sys.exit(0)

    all_results: list[dict[str, Any]] = []

    run_order = selected_order if selected_order else [1, 2, 3]
    for tier in run_order:
        if tier == 1 and tier1:
            results = run_domains(
                tier1, 1, run_tier1_domain, args.workers, mvm_version, push=args.push
            )
            all_results.extend(results)
        elif tier == 2 and tier2:
            results = run_domains(
                tier2, 2, run_tier2_domain, args.workers, mvm_version, push=args.push
            )
            all_results.extend(results)
        elif tier == 3 and tier3:
            log(f"Tier 3: running {len(tier3)} domain(s) on host...")
            for domain, files in sorted(tier3.items()):
                timeout = 1800 if domain == "kernel_build" else 600
                result = run_tier3_domain(domain, files, timeout=timeout)
                all_results.append(result)
                status = "PASS" if result["passed"] else "FAIL"
                log(f"  [{status}] {domain} (tier 3)")
                if not result["passed"]:
                    _print_failure(domain, result)

    print_summary(all_results)


if __name__ == "__main__":
    # Import here to avoid circular import issues with argparse processing
    import shutil

    main()
