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
    --host-direct --rebuild --candidate-version 0.3.0 \\
    --host-reboot-task /path/to/closed-host-reboot-task
  python3 scripts/run-system-tests.py --volume --image --prepare
  python3 scripts/run-system-tests.py --volume
  python3 scripts/run-system-tests.py --image

  # Limit parallel workers (default: 4)
  python3 scripts/run-system-tests.py --all --host-direct --workers 2
"""

from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import json
import os
import re
import shlex
import shutil
import stat
import struct
import subprocess
import sys
import tempfile
import time
import uuid
import xml.etree.ElementTree as ET
from concurrent.futures import Future, ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

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
# These markers qualify DownloadFile artifact bytes. Fixed checksum metadata
# fetched through GetBody may still use its configured upstream origin.
LOCAL_MIRROR_READ_MARKER = "Using local mirror for download"
FORBIDDEN_BUILDER_PULL_MARKERS = (
    "Mirror checksum mismatch, falling back to HTTP download",
    "Copied to asset mirror",
)
BUILDER_PULL_OUTPUT_LIMIT = 8 * 1024
MAX_RELEASE_HOST_VM_LIST_BYTES = 1024 * 1024
MAX_RELEASE_HOST_VM_LIST_ENTRIES = 4096
MAX_RELEASE_HOST_VM_FIELD_LENGTH = 1024
_RELEASE_VM_FIELDS = frozenset(("id", "name", "status"))
_RELEASE_VM_STATUSES = frozenset(
    ("starting", "running", "paused", "stopping", "stopped", "crashed", "error")
)

# Custom base image built during --prepare
BASE_IMAGE_NAME = "mvm-test-runner"
RUNNER_BASE_RECEIPT = "/opt/mvmctl-test/base-inputs.json"
MAX_RUNNER_BINDING_JSON_BYTES = 64 * 1024


@dataclass(frozen=True)
class ImportedRunnerKernelBinding:
    id: str
    sha256: str
    version: str = "7.0.11"
    arch: str = "x86_64"
    build_id: str = ""


def _runner_binding_json(stdout: str) -> dict[str, Any]:
    """Decode bounded binding evidence, refusing ambiguous JSON and envelopes."""

    def unique_fields(pairs):
        result = {}
        folded = set()
        for key, value in pairs:
            if key.casefold() in folded:
                raise RuntimeError(f"runner binding JSON has duplicate field: {key}")
            folded.add(key.casefold())
            result[key] = value
        return result

    if len(stdout.encode()) > MAX_RUNNER_BINDING_JSON_BYTES:
        raise RuntimeError("runner binding JSON exceeds size limit")
    try:
        info = json.loads(stdout, object_pairs_hook=unique_fields)
    except (ValueError, TypeError) as exc:
        raise RuntimeError(f"runner binding returned invalid JSON: {exc}") from exc
    if not isinstance(info, dict):
        raise RuntimeError("runner binding JSON must be an object")
    fields = {"schema_version", "status", "data", "error"}
    if fields.intersection(info):
        if set(info) != fields:
            raise RuntimeError(
                "runner binding JSON has incomplete or unknown envelope fields"
            )
        if type(info["schema_version"]) is not int or info["schema_version"] != 1:
            raise RuntimeError("runner binding JSON has unsupported schema")
        if info["status"] != "success" or info["error"] is not None:
            raise RuntimeError("runner binding JSON is not successful")
        info = info["data"]
        if not isinstance(info, dict):
            raise RuntimeError("runner binding JSON data must be an object")
    return info


def _sha256_runner_input(path: Path) -> str:
    """Hash a pinned regular input without following any directory or file symlink."""
    try:
        parent_fd = _open_directory_without_symlinks(path.parent)
        try:
            fd = os.open(
                path.name,
                os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                dir_fd=parent_fd,
            )
        finally:
            os.close(parent_fd)
        try:
            before = os.fstat(fd)
            if not stat.S_ISREG(before.st_mode):
                raise RuntimeError(f"runner input is not a regular file: {path}")
            digest = hashlib.sha256()
            while chunk := os.read(fd, 1024 * 1024):
                digest.update(chunk)
            after = os.fstat(fd)
            current = path.lstat()

            def identity(info):
                return (
                    info.st_dev,
                    info.st_ino,
                    info.st_size,
                    info.st_mtime_ns,
                    info.st_ctime_ns,
                )

            if identity(before) != identity(after) or identity(after) != identity(
                current
            ):
                raise RuntimeError(f"runner input changed while hashing: {path}")
            return digest.hexdigest()
        finally:
            os.close(fd)
    except OSError as exc:
        raise RuntimeError(f"cannot safely hash runner input {path}: {exc}") from exc


def _imported_runner_elf_build_id(fd: int, size: int) -> str:
    """Read one GNU build ID from bounded ELF64 x86_64 section notes."""

    def read_at(offset: int, length: int) -> bytes:
        if offset < 0 or length < 0 or offset + length > size:
            raise RuntimeError("imported runner ELF has out-of-bounds note/header")
        data = os.pread(fd, length, offset)
        if len(data) != length:
            raise RuntimeError("imported runner ELF has truncated note/header")
        return data

    header = struct.unpack("<16sHHIQQQIHHHHHH", read_at(0, 64))
    if (
        header[0][:7] != b"\x7fELF\x02\x01\x01"
        or header[1] != 2
        or header[2] != 62
        or header[3] != 1
        or header[8] != 64
        or header[11] != 64
        or not 0 < header[12] <= 4096
    ):
        raise RuntimeError(
            "imported runner ELF requires a bounded ELF64 x86_64 section table"
        )
    sections = read_at(header[6], header[11] * header[12])
    build_ids = []
    total_notes = 0
    for index in range(header[12]):
        section = struct.unpack_from("<IIQQQQIIQQ", sections, index * 64)
        if section[1] != 7:  # SHT_NOTE
            continue
        total_notes += section[5]
        if total_notes > 1024 * 1024:
            raise RuntimeError("imported runner ELF notes exceed size limit")
        notes = read_at(section[4], section[5])
        offset = 0
        while offset < len(notes):
            if len(notes) - offset < 12:
                raise RuntimeError("imported runner ELF has malformed note header")
            namesz, descsz, note_type = struct.unpack_from("<III", notes, offset)
            offset += 12
            name_end = offset + namesz
            desc_start = offset + ((namesz + 3) & ~3)
            desc_end = desc_start + descsz
            next_offset = desc_start + ((descsz + 3) & ~3)
            if next_offset > len(notes):
                raise RuntimeError("imported runner ELF has malformed note lengths")
            if notes[offset:name_end] == b"GNU\x00" and note_type == 3:
                if not 0 < descsz <= 64:
                    raise RuntimeError(
                        "imported runner ELF has invalid GNU build ID length"
                    )
                build_ids.append(notes[desc_start:desc_end].hex())
            offset = next_offset
    if len(build_ids) != 1:
        raise RuntimeError("imported runner ELF requires exactly one GNU build ID note")
    return build_ids[0]


def _imported_runner_elf_identity(path: Path, expected_sha256: str) -> str:
    """Bind the GNU build ID to the same pinned, stable ELF as the expected hash."""
    try:
        parent_fd = _open_directory_without_symlinks(path.parent)
        try:
            fd = os.open(
                path.name,
                os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                dir_fd=parent_fd,
            )
        finally:
            os.close(parent_fd)
        try:
            before = os.fstat(fd)
            if not stat.S_ISREG(before.st_mode):
                raise RuntimeError(f"imported runner ELF is not a regular file: {path}")
            digest = hashlib.sha256()
            while chunk := os.read(fd, 1024 * 1024):
                digest.update(chunk)
            if digest.hexdigest() != expected_sha256:
                raise RuntimeError(
                    "imported runner kernel ELF SHA256 does not match binding"
                )
            build_id = _imported_runner_elf_build_id(fd, before.st_size)
            after = os.fstat(fd)
            current = path.lstat()

            def identity(info):
                return (
                    info.st_dev,
                    info.st_ino,
                    info.st_size,
                    info.st_mtime_ns,
                    info.st_ctime_ns,
                )

            if identity(before) != identity(after) or identity(after) != identity(
                current
            ):
                raise RuntimeError(
                    f"imported runner ELF changed while reading identity: {path}"
                )
            return build_id
        finally:
            os.close(fd)
    except OSError as exc:
        raise RuntimeError(
            f"cannot safely read imported runner ELF {path}: {exc}"
        ) from exc


def _validate_imported_runner_kernel(binding: ImportedRunnerKernelBinding) -> str:
    if (
        re.fullmatch(r"[0-9a-f]{64}", binding.id) is None
        or re.fullmatch(r"[0-9a-f]{64}", binding.sha256) is None
        or binding.version != "7.0.11"
        or binding.arch != "x86_64"
    ):
        raise RuntimeError(
            "imported runner kernel requires canonical full ID/SHA256 and 7.0.11/x86_64"
        )
    result = mvm("kernel", "inspect", binding.id, "--json", check=False, timeout=30)
    if result.returncode != 0:
        raise RuntimeError(f"imported runner kernel is missing: {binding.id}")
    info = _runner_binding_json(result.stdout)
    kernel, storage = info.get("kernel"), info.get("storage")
    if not isinstance(kernel, dict) or not isinstance(storage, dict):
        raise RuntimeError("imported runner kernel inspection lacks kernel/storage")
    if (
        kernel.get("id") != binding.id
        or kernel.get("type") != "custom"
        or kernel.get("is_present") is not True
        or kernel.get("version") != binding.version
        or kernel.get("arch") != binding.arch
    ):
        raise RuntimeError("imported runner kernel metadata does not match binding")
    cache = Path(
        os.path.abspath(
            Path(os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")).expanduser()
        )
    )
    expected = cache / "kernels" / binding.id
    if storage.get("path") != str(expected):
        raise RuntimeError(
            f"imported runner kernel has unexpected managed path: {storage.get('path')}"
        )
    build_id = _imported_runner_elf_identity(expected, binding.sha256)
    if binding.build_id and binding.build_id != build_id:
        raise RuntimeError("imported runner ELF GNU build ID changed from binding")
    expected_id = hashlib.sha256(
        f"{binding.sha256}:{binding.version}:{binding.arch}".encode()
    ).hexdigest()
    if expected_id != binding.id:
        raise RuntimeError(
            "imported runner kernel ID does not match ELF SHA256/version/arch"
        )
    return build_id


def _resolve_imported_runner_kernel_binding() -> ImportedRunnerKernelBinding | None:
    kernel_id = os.environ.get("MVM_RUNNER_KERNEL_ID")
    digest = os.environ.get("MVM_RUNNER_KERNEL_SHA256")
    if kernel_id is None and digest is None:
        return None
    if not kernel_id or not digest:
        raise RuntimeError(
            "MVM_RUNNER_KERNEL_ID and MVM_RUNNER_KERNEL_SHA256 must be supplied together"
        )
    binding = ImportedRunnerKernelBinding(kernel_id, digest)
    build_id = _validate_imported_runner_kernel(binding)
    return ImportedRunnerKernelBinding(kernel_id, digest, build_id=build_id)


def _runner_image_key(
    version: str, binding: ImportedRunnerKernelBinding | None = None
) -> str:
    name = (
        BASE_IMAGE_NAME if binding is None else f"{BASE_IMAGE_NAME}-kernel-{binding.id}"
    )
    return f"{name}:{version}"


def _runner_base_receipt(
    version: str, binding: ImportedRunnerKernelBinding
) -> dict[str, object]:
    return {
        "receipt_schema": 1,
        "candidate_version": version,
        "candidate_sha256": _sha256_runner_input(Path(MVM_CANDIDATE_BINARY)),
        "kernel_id": binding.id,
        "kernel_sha256": binding.sha256,
        "kernel_version": binding.version,
        "kernel_arch": binding.arch,
        "kernel_build_id": binding.build_id,
    }


def _require_runner_base_receipt(
    vm_name: str, version: str, binding: ImportedRunnerKernelBinding
) -> None:
    result = mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        f"head -c 4097 {RUNNER_BASE_RECEIPT}",
        timeout=30,
    )
    if len(result.stdout.encode()) > 4096:
        raise RuntimeError("runner base input receipt exceeds size limit")
    receipt = _runner_binding_json(result.stdout)
    expected = _runner_base_receipt(version, binding)
    if (
        set(json.loads(result.stdout)) != set(expected)
        or receipt != expected
        or type(receipt.get("receipt_schema")) is not int
    ):
        raise RuntimeError(
            "runner base input receipt does not match candidate/kernel binding"
        )


def _require_imported_runner_smoke(
    vm_name: str, binding: ImportedRunnerKernelBinding
) -> None:
    if re.fullmatch(r"(?:[0-9a-f]{2}){1,64}", binding.build_id) is None:
        raise RuntimeError("smoke binding lacks a validated GNU build ID")
    info = _runner_binding_json(
        mvm("vm", "inspect", vm_name, "--json", timeout=30).stdout
    )
    assets = info.get("assets")
    kernel = assets.get("kernel") if isinstance(assets, dict) else None
    if not isinstance(kernel, dict) or kernel.get("id") != binding.id:
        raise RuntimeError("smoke VM did not select the exact imported kernel")
    # Retain live release/build-ID readback, rather than relying on asset metadata.
    script = (
        "import os,struct; from pathlib import Path; "
        "release=os.uname().release; assert release=='7.0.11',release; "
        "notes=Path('/sys/kernel/notes').read_bytes(); p=0; ids=[]\n"
        "while p+12<=len(notes):\n"
        " n,d,t=struct.unpack_from('<III',notes,p); p+=12\n"
        " name=notes[p:p+n]; p+=(n+3)&~3\n"
        " desc=notes[p:p+d]; p+=(d+3)&~3\n"
        " if name==b'GNU\\x00' and t==3: ids.append(desc.hex())\n"
        "assert len(ids)==1 and ids[0],ids\n"
        "print('Linux '+release+' GNU build ID '+ids[0])"
    )
    live = mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        shlex.join(["python3", "-c", script]),
        timeout=30,
    )
    log(f"  {vm_name}: {live.stdout.strip()}")
    if live.stdout.strip() != f"Linux {binding.version} GNU build ID {binding.build_id}":
        raise RuntimeError("smoke VM live GNU build ID does not match imported ELF")
    status = mvm(
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "10",
        "--",
        "/usr/local/bin/mvm host status --json",
        timeout=30,
    )
    data = _runner_binding_json(status.stdout)
    resources = data.get("resources")
    if (
        not isinstance(resources, dict)
        or resources.get("nftables_available") is not True
    ):
        raise RuntimeError("imported runner smoke requires nftables_available: true")


# Root-owned staging location used to exercise the real administrator install
# flow inside every disposable runner VM.  Tests execute the installed binary
# at /usr/local/bin/mvm; they never treat this candidate path as the CLI.
RUNNER_SYSTEM_CANDIDATE_DIR = "/opt/mvmctl-test"
RUNNER_SYSTEM_CANDIDATE = f"{RUNNER_SYSTEM_CANDIDATE_DIR}/mvm-candidate"
RUNNER_SYSTEM_UPLOAD = "/home/runner/.mvm-system-candidate.upload"
RUNNER_USER_INIT_COMMAND = (
    "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
    "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm init --non-interactive "
    "--binary-version 1.16.2"
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
    "host_admin": ["tests/system/host/test_host_admin.py"],
    "guestfs": ["tests/system/cache/test_guestfs_appliance.py"],
    "volume": [
        "tests/system/volume/test_volume.py",
    ],
    "vm_lifecycle": ["tests/system/vm/test_vm_lifecycle.py"],
    "vm_data_persistence": ["tests/system/vm/test_vm_data_persistence.py"],
    "jailer": [
        "tests/system/vm/test_jailer.py",
        "tests/system/vm/test_cgroup.py",
    ],
    "exec": [
        "tests/system/exec/test_exec.py",
        "tests/system/exec/test_vm_to_vm_exec.py",
    ],
    "ssh": ["tests/system/ssh/test_ssh.py"],
    "console": ["tests/system/console/test_console.py"],
    "logs": ["tests/system/logs/test_logs.py"],
    "full_journeys": ["tests/system/full_journeys/test_full_journeys.py"],
    "nftables": ["tests/system/network/test_nftables.py"],
    "policies": ["tests/system/network/test_policies.py"],
}

# Explicit continuations are registered coverage, not ordinary parallel batch work.
HOST_REBOOT_PHASE_DOMAINS = {"host_reboot": ["tests/system/host/test_host_reboot.py"]}

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
for d in HOST_REBOOT_PHASE_DOMAINS:
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


def _volume_inspect_response(stdout: str, name: str) -> tuple[dict[str, object], bool]:
    """Decode inspection data and whether the controller uses the versioned protocol."""
    try:
        info = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"volume inspect returned invalid JSON for '{name}': {exc}"
        ) from exc
    if not isinstance(info, dict):
        raise RuntimeError(f"volume inspect response for '{name}' must be an object")
    envelope_fields = {"schema_version", "status", "data", "error"}
    versioned = bool(envelope_fields.intersection(info))
    if versioned:
        if set(info) != envelope_fields:
            raise RuntimeError(
                f"volume inspect response for '{name}' has incomplete or unknown envelope fields"
            )
        if type(info["schema_version"]) is not int or info["schema_version"] != 1:
            raise RuntimeError(
                f"volume inspect response for '{name}' has unsupported schema"
            )
        if info["status"] != "success" or info["error"] is not None:
            raise RuntimeError(
                f"volume inspect response for '{name}' is not successful"
            )
        info = info["data"]
        if not isinstance(info, dict):
            raise RuntimeError(f"volume inspect data for '{name}' must be an object")
    volume = info.get("volume")
    if not isinstance(volume, dict):
        raise RuntimeError(
            f"volume inspect response for '{name}' must contain a volume object"
        )
    return info, versioned


def _volume_inspect_record(stdout: str, name: str) -> dict[str, object]:
    """Decode a volume record from the installed controller's JSON response."""
    info, _versioned = _volume_inspect_response(stdout, name)
    return info["volume"]


def _inspect_volume_path(name: str) -> tuple[bool, str | None]:
    """Return whether a volume record exists and its recorded backing path."""
    result = mvm("volume", "inspect", name, "--json", check=False, timeout=30)
    if result.returncode != 0:
        return False, None
    volume = _volume_inspect_record(result.stdout, name)
    path = volume.get("path")
    return True, path if isinstance(path, str) and path else None


def _get_volume_path(name: str) -> str | None:
    """Get the recorded on-disk path of a volume, or None if absent."""
    _exists, path = _inspect_volume_path(name)
    return path


def _inspect_shared_volume() -> tuple[bool, str | None, str | None, bool]:
    """Inspect the mirror source or its authoritative protected backing."""
    result = mvm(
        "volume", "inspect", SHARED_VOLUME_NAME, "--json", check=False, timeout=30
    )
    if result.returncode != 0:
        return False, None, None, False
    try:
        info, versioned = _volume_inspect_response(result.stdout, SHARED_VOLUME_NAME)
    except RuntimeError as exc:
        raise _UnsafeVolumeBackingPath(
            f"cannot establish shared volume identity: {exc}"
        ) from exc
    volume = info["volume"]
    if volume.get("name") != SHARED_VOLUME_NAME:
        raise _UnsafeVolumeBackingPath(
            "shared volume inspection returned a different name"
        )
    path = volume.get("path")
    if not versioned:
        disk = info.get("disk_info")
        if isinstance(disk, dict) and {
            "volume_id",
            "logical_bytes",
            "protected",
        }.intersection(disk):
            raise _UnsafeVolumeBackingPath(
                "authoritative shared volume inspection requires a versioned response"
            )
        return True, path if isinstance(path, str) and path else None, None, False

    volume_id = volume.get("id")
    if (
        not isinstance(volume_id, str)
        or re.fullmatch(r"[0-9a-f]{64}", volume_id) is None
    ):
        raise _UnsafeVolumeBackingPath(
            "shared volume inspection returned an invalid volume ID"
        )
    disk = info.get("disk_info")
    if not isinstance(disk, dict) or disk.get("volume_id") != volume_id:
        raise _UnsafeVolumeBackingPath(
            "shared volume accounting does not match its volume ID"
        )
    if type(disk.get("protected")) is not bool:
        raise _UnsafeVolumeBackingPath(
            "shared volume inspection has no explicit protection state"
        )
    logical_bytes = disk.get("logical_bytes")
    if type(logical_bytes) is not int or not 0 < logical_bytes <= sys.maxsize:
        raise _UnsafeVolumeBackingPath(
            "shared volume accounting has an invalid logical size"
        )
    if volume.get("format") != "raw" or volume.get("is_read_only") is not True:
        raise _UnsafeVolumeBackingPath("shared volume must be raw and read-only")
    if disk["protected"]:
        if path != "":
            raise _UnsafeVolumeBackingPath(
                "protected shared volume must hide its backing path"
            )
        return True, None, volume_id, True
    if not isinstance(path, str) or not path:
        raise _UnsafeVolumeBackingPath("unprotected shared volume has no source path")
    return True, path, volume_id, False


def _expected_shared_volume_path(volume_id: str | None = None) -> Path:
    """Return the lexical managed path without following any symlinks."""
    if volume_id is not None and re.fullmatch(r"[0-9a-f]{64}", volume_id) is None:
        raise _UnsafeVolumeBackingPath(
            "shared volume source requires a canonical volume ID"
        )
    cache_root = Path(os.environ.get("MVM_CACHE_DIR", "~/.cache/mvmctl")).expanduser()
    return Path(
        os.path.abspath(
            cache_root / "volumes" / f"{volume_id or SHARED_VOLUME_NAME}.raw"
        )
    )


def _validated_volume_backing_path(
    raw_path: str | os.PathLike[str],
    volume_id: str | None = None,
) -> Path:
    """Require the exact regular, non-symlink runner volume backing file."""
    path = Path(os.path.abspath(Path(raw_path).expanduser()))
    expected = _expected_shared_volume_path(volume_id)
    if path != expected:
        raise _UnsafeVolumeBackingPath(
            f"unexpected shared volume backing path: expected {expected}, got {path}"
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


def _require_shared_volume_path_absent_before_create(
    volume_id: str | None = None,
) -> None:
    """Refuse create if old cleanup left any object at the canonical path."""
    expected = _expected_shared_volume_path(volume_id)
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


def _populate_volume_image(
    volume_path: Path,
    mirror_path: Path,
    volume_id: str | None = None,
) -> None:
    """Format and populate one ext4 image without sudo or loop mounts."""
    backing = _validated_volume_backing_path(volume_path, volume_id)
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
        timeout=30,
    )

    volume_id = None
    try:
        record_exists, raw_path, volume_id, protected = _inspect_shared_volume()
        if protected:
            raise _UnsafeVolumeBackingPath(
                "created shared volume is already protected; refusing to reseed"
            )
        if not record_exists or not raw_path:
            raise RuntimeError("created shared volume has no recorded backing path")
        vol_path = _validated_volume_backing_path(raw_path, volume_id)

        if not any(mirror_path.iterdir()):
            log(f"WARNING: Asset mirror at {mirror_path} is empty.")
            log(
                "Populating volume with empty filesystem. "
                "Tests that need assets will fail."
            )
        _populate_volume_image(vol_path, mirror_path, volume_id)
    except _UnsafeVolumeBackingPath:
        raise
    except RuntimeError as primary:
        cleanup = mvm(
            "volume",
            "rm",
            volume_id or SHARED_VOLUME_NAME,
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

    Reuse authoritative protected backing without accessing its old source.
    Explicit rebuild removes the selected volume through the controller before recreation.
    """
    mirror_path = Path(ASSET_MIRROR_HOST).expanduser()
    if not mirror_path.exists():
        log(f"WARNING: Asset mirror at {mirror_path} does not exist.")
        log(
            "Create it first by pulling images/kernels/binaries with MVM_ASSET_MIRROR set."
        )

    record_exists, raw_path, volume_id, protected = _inspect_shared_volume()
    if protected:
        if rebuild:
            mvm("volume", "rm", volume_id, "--force", check=True, timeout=30)
            _create_and_seed_volume()
            return
        log(
            f"Shared volume '{SHARED_VOLUME_NAME}' already has protected backing ({volume_id})"
        )
        return
    existing_path: Path | None = None
    if record_exists:
        try:
            if raw_path is None:
                raise RuntimeError("volume record has no backing path")
            existing_path = _validated_volume_backing_path(raw_path, volume_id)
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
        mvm("volume", "rm", volume_id or SHARED_VOLUME_NAME, "--force", timeout=30)
        _require_shared_volume_path_absent_before_create(volume_id)

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


def provision_t1(
    vm_name: str,
    mvm_version: str,
    on_created: Callable[[], None],
    *,
    binding: ImportedRunnerKernelBinding | None = None,
) -> None:
    """Provision a Tier 1 VM from custom base image + shared volume."""
    if binding is not None:
        _validate_imported_runner_kernel(binding)
    _ensure_builder_key()
    if binding is None:
        _ensure_official_kernel_on_host(vm_name)
    else:
        _validate_imported_runner_kernel(binding)
    log(
        f"  Creating T1 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        _runner_image_key(mvm_version, binding),
        "--user",
        "runner",
        "--vcpu",
        "2",
        "--mem",
        "1024",
        "--disk-size",
        "9G",
        "--kernel",
        binding.id if binding is not None else "official:7.0.11",
        "--ssh-key",
        BUILDER_KEY_NAME,
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    on_created()
    if binding is not None:
        _require_runner_base_receipt(vm_name, mvm_version, binding)
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


def provision_t2(
    vm_name: str,
    mvm_version: str,
    on_created: Callable[[], None],
    *,
    binding: ImportedRunnerKernelBinding | None = None,
) -> None:
    """Provision a Tier 2 VM from custom base image + shared volume (binary + tests + deps pre-installed)."""
    if binding is not None:
        _validate_imported_runner_kernel(binding)
    _ensure_builder_key()
    if binding is None:
        _ensure_official_kernel_on_host(vm_name)
    else:
        _validate_imported_runner_kernel(binding)
    log(
        f"  Creating T2 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + shared volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        _runner_image_key(mvm_version, binding),
        "--kernel",
        binding.id if binding is not None else "official:7.0.11",
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
    on_created()
    if binding is not None:
        _require_runner_base_receipt(vm_name, mvm_version, binding)
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
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm bin pull 1.16.2 --default --force",
        timeout=150,
    )


def destroy_vm(vm_name: str, *, check: bool = True) -> None:
    """Destroy a runner VM."""
    mvm("vm", "rm", vm_name, "--force", timeout=60, check=check)


def _record_runner_destruction(
    result: dict[str, Any],
    vm_name: str,
) -> None:
    """Make mandatory runner destruction part of the domain result."""
    try:
        destroy_vm(vm_name)
    except Exception as exc:
        result["passed"] = False
        cleanup_failure = f"runner VM destruction failed for {vm_name}: {exc}"
        primary_output = result["output"].rstrip()
        result["output"] = (
            f"{primary_output}\n{cleanup_failure}"
            if primary_output
            else cleanup_failure
        )


def _bounded_builder_pull_output(
    result: subprocess.CompletedProcess[str],
) -> str:
    """Render captured pull output without flooding the qualification log."""
    sections = []
    if result.stdout:
        sections.append(f"[stdout]\n{result.stdout.rstrip()}")
    if result.stderr:
        sections.append(f"[stderr]\n{result.stderr.rstrip()}")
    output = "\n".join(sections) or "(no captured output)"
    if len(output) <= BUILDER_PULL_OUTPUT_LIMIT:
        return output

    marker = "\n... builder pull output truncated ...\n"
    remaining = BUILDER_PULL_OUTPUT_LIMIT - len(marker)
    head = remaining // 2
    return f"{output[:head]}{marker}{output[-(remaining - head):]}"


def _pull_builder_assets(vm_name: str) -> None:
    """Pull base-image assets serially and require local-mirror evidence."""
    pulls = (
        (
            "Firecracker kernel",
            (
                "kernel",
                "pull",
                "--type",
                "firecracker",
                "--version",
                "v1.15",
                "--default",
            ),
        ),
        (
            "Alpine image",
            ("image", "pull", "alpine:3.23"),
        ),
        (
            "Ubuntu Minimal image",
            ("image", "pull", "ubuntu-minimal:24.04"),
        ),
        (
            "Ubuntu image",
            ("image", "pull", "ubuntu", "--version", "24.04"),
        ),
    )
    for label, pull_args in pulls:
        log(f"  Pulling {label} into '{vm_name}'...")
        command = shlex.join(
            (
                "MVM_ASSET_MIRROR=/mnt",
                "MVM_LOG_LEVEL=INFO",
                "/usr/local/bin/mvm",
                *pull_args,
            )
        )
        result = mvm(
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            command,
            timeout=600,
            capture=True,
            check=False,
        )
        _require_local_mirror_pull(result, label)

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


def _require_local_mirror_pull(
    result: subprocess.CompletedProcess[str], label: str
) -> None:
    output = f"{result.stdout or ''}\n{result.stderr or ''}"
    print(f"  {label} output:\n{_bounded_builder_pull_output(result)}", flush=True)
    if result.returncode != 0:
        raise RuntimeError(
            f"builder asset pull failed for {label}: rc={result.returncode}"
        )
    for marker in FORBIDDEN_BUILDER_PULL_MARKERS:
        if marker in output:
            raise RuntimeError(
                f"builder asset pull for {label} reported forbidden mirror event: {marker}"
            )
    if LOCAL_MIRROR_READ_MARKER not in output:
        raise RuntimeError(
            f"builder asset pull for {label} did not prove a local mirror artifact read"
        )


def _build_base_image(
    mvm_version: str,
    *,
    rebuild: bool = False,
    binding: ImportedRunnerKernelBinding | None = None,
) -> str:
    """Build a custom base image with all test dependencies pre-installed.

    Creates a VM from ubuntu-minimal:noble, copies in the mvm binary and
    system tests, installs OS packages, stops the VM, imports the rootfs
    as a custom image, then destroys the builder VM.
    Returns the image version tag used.
    """
    img_tag = mvm_version
    image_key = _runner_image_key(img_tag, binding)
    if binding is not None:
        _validate_imported_runner_kernel(binding)
    log(f"Building custom base image '{image_key}'...")

    # When rebuilding, remove the existing image first so it's rebuilt fresh.
    if rebuild:
        exists = (
            binding is None
            or mvm(
                "image", "inspect", image_key, "--json", check=False, timeout=15
            ).returncode
            == 0
        )
        if exists:
            mvm(
                "image",
                "rm",
                image_key,
                "--force",
                check=binding is not None,
                timeout=15,
            )

    # Check if already built (skip check when rebuilding)
    if not rebuild:
        check = mvm(
            "image",
            "inspect",
            image_key,
            "--json",
            check=False,
            timeout=15,
        )
        if check.returncode == 0:
            log(f"  Base image '{image_key}' already exists, skipping")
            return img_tag

    log(f"  Creating builder VM '{BASE_VM_NAME}'...")
    kernel_args = []
    if binding is not None:
        _validate_imported_runner_kernel(binding)
        kernel_args = ["--kernel", binding.id]
    mvm(
        "vm",
        "create",
        "--writeback",
        BASE_VM_NAME,
        "--image",
        "ubuntu-minimal:noble",
        *kernel_args,
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
            f"stat -c%s {RUNNER_SYSTEM_CANDIDATE} && stat -c%s /usr/local/bin/mvm",
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
            "libssl-dev libelf-dev pkg-config git curl dwarves "
            "cloud-image-utils util-linux && "
            "sudo apt-get install -y -qq --no-install-recommends "
            "libguestfs-tools supermin qemu-system-x86 file e2fsprogs "
            "linux-image-generic && "
            "sudo apt-get clean",
            timeout=300,
        )
        # Ubuntu packages kernels as 0600; unprivileged supermin must read them.
        # Only package-owned boot images are adjusted, never the external VM kernel.
        kernel_readability = r"""
test ! -L /boot
test "$(stat -c '%u:%g:%a' /boot)" = '0:0:755'
packages=$(dpkg-query -W -f='${binary:Package} ${db:Status-Status}\n' 'linux-image-*')
count=0
while read -r package status; do
    case "$package" in linux-image-[0-9]*|linux-image-unsigned-[0-9]*) ;; *) continue ;; esac
    test "$status" = installed || continue
    paths=$(dpkg-query -L "$package")
    while IFS= read -r kernel; do
        case "$kernel" in /boot/vmlinuz-[0-9]*) ;; *) continue ;; esac
        test "${kernel%/*}" = /boot
        test -f "$kernel" && test ! -L "$kernel"
        test "$(stat -c '%u:%g' "$kernel")" = '0:0'
        identity=$(stat -c '%d:%i:%u:%g:%s' "$kernel")
        original_mode=$(stat -c '%a' "$kernel")
        test "$original_mode" = 600 || test "$original_mode" = 644
        expected=$(awk -v path="${kernel#/}" '$2 == path {print $1}' "/var/lib/dpkg/info/$package.md5sums")
        test "${#expected}" = 32
        checksum=$(md5sum -- "$kernel")
        test "${checksum%% *}" = "$expected"
        if test "$original_mode" = 600; then chmod 0644 -- "$kernel"; fi
        test "$(stat -c '%d:%i:%u:%g:%s' "$kernel")" = "$identity"
        test "$(stat -c '%a' "$kernel")" = 644
        test "$(md5sum -- "$kernel")" = "$checksum"
        runuser -u runner -- test -r "$kernel"
        printf 'GuestFS kernel readable: %s (mode %s -> 644)\n' "$kernel" "$original_mode"
        count=$((count + 1))
    done <<< "$paths"
done <<< "$packages"
test "$count" -gt 0
"""
        log("  Making packaged GuestFS kernels readable by runner...")
        mvm(
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "sudo bash -euo pipefail -c " + shlex.quote(kernel_readability),
            timeout=30,
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
        if binding is not None:
            inputs = _runner_base_receipt(mvm_version, binding)
            installed = mvm(
                "exec",
                BASE_VM_NAME,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "sha256sum /usr/local/bin/mvm",
                timeout=30,
            )
            if installed.stdout.split() != [
                inputs["candidate_sha256"],
                "/usr/local/bin/mvm",
            ]:
                raise RuntimeError(
                    "builder installed candidate SHA256 does not match base receipt"
                )
            receipt = json.dumps(inputs, sort_keys=True)
            mvm(
                "exec",
                BASE_VM_NAME,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                f"printf '%s' {shlex.quote(receipt)} | sudo tee {RUNNER_BASE_RECEIPT} >/dev/null && "
                f"sudo chmod 0444 {RUNNER_BASE_RECEIPT}",
                timeout=30,
            )
        log(f"  Stopping '{BASE_VM_NAME}'...")
        mvm("vm", "stop", BASE_VM_NAME, timeout=60)

        log(f"  Importing image as '{image_key}'...")
        mvm(
            "image",
            "import",
            image_key,
            BASE_VM_NAME,
            *(["--default"] if binding is None else []),
            "--skip-optimization",
            timeout=300,
        )
        log(f"  Base image '{image_key}' built successfully")
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
            f"MVM_TEST_QUALIFICATION_HOST={shlex.quote(vm_name)}",
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


_HOST_REBOOT_RUNNER = "mvmctl-030-test"
_HOST_REBOOT_FILE = "tests/system/host/test_host_reboot.py"
_HOST_REBOOT_LIMIT = 128 * 1024
_HOST_REBOOT_TASK_FIELDS = {
    "schema_version", "runner", "outer_before", "outer_after", "controller", "source_revision",
    "test_sha256", "candidate_sha256", "candidate_version", "kernel_id", "kernel_sha256", "guest_task",
    "before_boot", "after_boot", "pre_sha256", "pre_outcomes_sha256", "source_payload",
}
_HOST_REBOOT_SOURCE_FILES = ("tests/__init__.py", "tests/system/__init__.py", "tests/system/conftest.py",
                           "tests/system/pytest.ini", "tests/system/host/__init__.py", "tests/system/host/test_host.py",
                           _HOST_REBOOT_FILE, "tests/system/network/__init__.py", "tests/system/network/test_network.py",
                           "tests/system/vm/__init__.py", "tests/system/vm/test_vm_lifecycle.py")


def _host_reboot_json(raw: bytes) -> dict[str, Any]:
    """Bounded unambiguous JSON for only the closed reboot receipts."""
    if len(raw) > _HOST_REBOOT_LIMIT:
        raise RuntimeError("host_reboot receipt exceeds bound")
    def unique(pairs):
        values = {}
        folded = set()
        for key, value in pairs:
            if key.casefold() in folded:
                raise RuntimeError("duplicate host_reboot receipt field")
            folded.add(key.casefold())
            values[key] = value
        return values
    data = json.loads(raw, object_pairs_hook=unique)
    if not isinstance(data, dict):
        raise RuntimeError("host_reboot receipt must be an object")
    return data


def _host_reboot_read(path: Path) -> bytes:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as stream:
        row = os.fstat(stream.fileno())
        if (not stat.S_ISREG(row.st_mode) or row.st_nlink != 1 or row.st_uid != os.getuid()
                or stat.S_IMODE(row.st_mode) != 0o600 or row.st_size > _HOST_REBOOT_LIMIT):
            raise RuntimeError("host_reboot receipt must be a private owned bounded regular file")
        raw = stream.read(_HOST_REBOOT_LIMIT + 1)
        current = os.fstat(stream.fileno())
        stable = lambda info: (info.st_dev, info.st_ino, info.st_mode, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        if stable(current) != stable(row) or stable(path.lstat()) != stable(row) or len(raw) != row.st_size:
            raise RuntimeError("host_reboot receipt changed during read")
        return raw


def _host_reboot_write(task: Path, name: str, raw: bytes) -> None:
    if len(raw) > _HOST_REBOOT_LIMIT:
        raise RuntimeError("host_reboot retained output exceeds bound")
    fd = os.open(task / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    fd = os.open(task, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def _host_reboot_outer_identity() -> dict[str, Any]:
    """Observe only the designated outer VM and its exact current process; no control writes."""
    info = _runner_binding_json(mvm("vm", "inspect", _HOST_REBOOT_RUNNER, "--json", timeout=15).stdout)
    row = info.get("vm", info)
    if (row.get("name") != _HOST_REBOOT_RUNNER or row.get("status") != "running"
            or re.fullmatch(r"[0-9a-f]{32}", row.get("id", "")) is None):
        raise RuntimeError("host_reboot requires the exact existing running named runner")
    pid = int(row["pid"])
    if pid <= 0:
        raise RuntimeError("host_reboot runner process identity is incomplete")
    fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    if len(fields) < 20 or fields[0] in ("Z", "X"):
        raise RuntimeError("host_reboot runner task changed")
    ticks = int(fields[19])
    if ticks <= 0 or ("process_start_time" in row and int(row["process_start_time"]) != ticks):
        raise RuntimeError("host_reboot runner start tick changed")
    groups = Path(f"/proc/{pid}/cgroup").read_text().splitlines()
    if groups != [f"0::/mvmctl/{row['id']}"]:
        raise RuntimeError("host_reboot requires its exact observed runner cgroup")
    group = Path("/sys/fs/cgroup/mvmctl") / row["id"]
    controls = {key: (group / leaf).read_text().strip() for key, leaf in
                (("memory_high", "memory.high"), ("memory_max", "memory.max"))}
    if (Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()[19] != str(ticks)
            or Path(f"/proc/{pid}/cgroup").read_text().splitlines() != groups):
        raise RuntimeError("host_reboot runner task changed while reading controls")
    if info["resources"]["mem"] != 6144:
        raise RuntimeError("host_reboot must preserve the approved 6 GiB guest geometry")
    return dict(runner_id=row["id"], created_at=row["created_at"], pid=pid, start_ticks=ticks,
                guest_mem_size_mib=info["resources"]["mem"], **controls)


class _HostRebootReconnectUnavailable(RuntimeError):
    """Only transport unavailability permits another bounded read-only probe."""


def _host_reboot_ssh(
    command: str, *, connect_timeout: int, timeout: int, check: bool = True,
) -> subprocess.CompletedProcess[str]:
    """Use the existing named runner's SSH key; never depend on outer vsock."""
    return mvm("ssh", _HOST_REBOOT_RUNNER, "--user", "runner", "--key",
               str(Path.home() / ".config/mvmctl/keys/aland"), "--timeout", str(connect_timeout),
               "--cmd", command, check=check, timeout=timeout)


def _host_reboot_stage_source(bound: dict[str, Any]) -> None:
    """Copy only the fixed phase inputs over the same SSH transport, without vsock cp."""
    for relative in _HOST_REBOOT_SOURCE_FILES:
        raw = (_REPO_ROOT / relative).read_bytes()
        digest = bound["source_payload"][relative]
        if hashlib.sha256(raw).hexdigest() != digest:
            raise RuntimeError("host_reboot source changed before staging")
        encoded = base64.b64encode(gzip.compress(raw, mtime=0)).decode("ascii")
        # Compression keeps each fixed source file below the single-argument limit.
        # The destination is exclusively created in this task, never an existing source.
        script = ("import base64,gzip,hashlib,pathlib; "
                  f"raw=gzip.decompress(base64.b64decode({encoded!r})); "
                  f"assert hashlib.sha256(raw).hexdigest()=={digest!r}; "
                  f"path=pathlib.Path({(bound['guest_task'] + '/source/' + relative)!r}); "
                  "path.parent.mkdir(parents=True,exist_ok=True); "
                  "stream=path.open('xb'); stream.write(raw); stream.close()")
        _host_reboot_ssh(shlex.join(["python3", "-c", script]), connect_timeout=10, timeout=60)


def _host_reboot_probe(bound: dict[str, Any]) -> str:
    command = "cat /proc/sys/kernel/random/boot_id && sha256sum /usr/local/bin/mvm && /usr/local/bin/mvm --version"
    try:
        result = _host_reboot_ssh(command, connect_timeout=2, check=False, timeout=15)
    except subprocess.TimeoutExpired as exc:
        raise _HostRebootReconnectUnavailable("host_reboot reconnect timed out") from exc
    if result.returncode != 0:
        raise _HostRebootReconnectUnavailable("host_reboot reconnect unavailable")
    lines = result.stdout.strip().splitlines()
    if len(lines) != 3 or str(uuid.UUID(lines[0])) != lines[0]:
        raise RuntimeError("host_reboot reconnect lacks exact guest boot evidence")
    if lines[1].split() != [bound["candidate_sha256"], "/usr/local/bin/mvm"] or lines[2] != bound["candidate_version"]:
        raise RuntimeError("host_reboot installed candidate changed")
    return lines[0]


def _host_reboot_require_source(bound: dict[str, Any]) -> None:
    """Refuse lost or changed fixed phase inputs; never repair a rebooted task."""
    paths = [bound["guest_task"] + "/source/" + relative for relative in _HOST_REBOOT_SOURCE_FILES]
    result = _host_reboot_ssh(shlex.join(["sha256sum", "--", *paths]), connect_timeout=10, timeout=30)
    expected = [f"{bound['source_payload'][relative]}  {path}"
                for relative, path in zip(_HOST_REBOOT_SOURCE_FILES, paths)]
    if result.stdout.splitlines() != expected:
        raise RuntimeError("host_reboot staged source changed across reboot")


def _host_reboot_fetch(bound: dict[str, Any], name: str) -> bytes:
    path = bound["guest_task"] + "/" + name
    result = _host_reboot_ssh(f"head -c {_HOST_REBOOT_LIMIT + 1} -- {shlex.quote(path)}",
                              connect_timeout=10, timeout=30)
    raw = result.stdout.encode()
    if len(raw) > _HOST_REBOOT_LIMIT:
        raise RuntimeError("host_reboot remote receipt exceeds bound")
    return raw


def _host_reboot_pytest(bound: dict[str, Any], phase: str, task: Path) -> bytes:
    """Retain strict phase stdout/stderr/outcomes; do not alter ordinary remote-report cleanup."""
    source = bound["guest_task"] + "/source"
    report = bound["guest_task"] + f"/{phase}.outcomes.json"
    env = {
        "MVM_TEST_VM": _HOST_REBOOT_RUNNER, "MVM_TEST_QUALIFICATION_HOST": _HOST_REBOOT_RUNNER,
        "MVM_ASSET_MIRROR": "/home/runner/.cache/mvm-asset-mirror",
        "MVM_CONFIG_DIR": "/home/runner/.config/mvmctl-030-schema6",
        "MVM_CACHE_DIR": "/home/runner/.cache/mvmctl-030-release",
        "MVM_TEMP_DIR": bound["guest_task"] + "/tmp", "TMPDIR": bound["guest_task"] + "/tmp",
        "MVM_HOST_REBOOT_PHASE": phase, "MVM_HOST_REBOOT_TASK": bound["guest_task"],
        "MVM_HOST_REBOOT_SOURCE_REVISION": bound["source_revision"],
        "MVM_HOST_REBOOT_CANDIDATE_SHA256": bound["candidate_sha256"],
        "MVM_HOST_REBOOT_CANDIDATE_VERSION": bound["candidate_version"],
        "MVM_RUNNER_KERNEL_ID": bound["kernel_id"], "MVM_RUNNER_KERNEL_SHA256": bound["kernel_sha256"],
        "PYTHONDONTWRITEBYTECODE": "1", "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1",
        PYTEST_OUTCOME_REPORT_ENV: report,
    }
    command = (f"cd {shlex.quote(source)} && " + " ".join(f"{key}={shlex.quote(value)}" for key, value in env.items())
               + " " + shlex.join(["python3", "-B", "-m", "pytest", "-c", "tests/system/pytest.ini",
                    "--rootdir=.", "--strict-markers", "-p", "no:cacheprovider", "-p", "pytest_timeout", "--timeout=600",
                    "--timeout-method=signal", f"--basetemp={bound['guest_task']}/{phase}-tmp",
                    f"--junitxml={bound['guest_task']}/{phase}.xml", "-x", "-s", "-q",
                    _HOST_REBOOT_FILE + f"::test_host_reboot_{phase}"]))
    result = _host_reboot_ssh(command, connect_timeout=10, check=False, timeout=660)
    _host_reboot_write(task, f"{phase}.stdout", (result.stdout or "").encode())
    _host_reboot_write(task, f"{phase}.stderr", (result.stderr or "").encode())
    _host_reboot_write(task, f"{phase}.native.json", json.dumps(dict(phase=phase, returncode=result.returncode)).encode())
    raw = _host_reboot_fetch(bound, f"{phase}.outcomes.json")
    _host_reboot_write(task, f"{phase}.outcomes.json", raw)
    xml = _host_reboot_fetch(bound, f"{phase}.xml")
    _host_reboot_write(task, f"{phase}.xml", xml)
    _host_reboot_require_phase_report(phase, raw, xml, result.returncode)
    receipt = _host_reboot_fetch(bound, f"{phase}.json")
    _host_reboot_write(task, f"{phase}.json", receipt)
    return receipt


def _host_reboot_require_phase_report(phase: str, raw: bytes, xml: bytes, returncode: int) -> None:
    outcomes = require_complete_pytest_outcomes(raw, process_returncode=returncode)
    if outcomes.collected != 1:
        raise RuntimeError("host_reboot phase must pass exactly its one registered node")
    root = ET.fromstring(xml)
    totals = {key: sum(int(suite.get(key, "0")) for suite in root.iter("testsuite"))
              for key in ("tests", "failures", "errors", "skipped")}
    cases = list(root.iter("testcase"))
    if (totals != dict(tests=1, failures=0, errors=0, skipped=0) or len(cases) != 1
            or cases[0].get("classname") != "tests.system.host.test_host_reboot"
            or cases[0].get("name") != f"test_host_reboot_{phase}"
            or any(list(root.iter(tag)) for tag in ("failure", "error", "skipped"))):
        raise RuntimeError("host_reboot JUnit does not prove the exact selected phase")


def _host_reboot_require_envelope(raw: bytes, bound: dict[str, Any], outer: dict[str, Any]) -> None:
    receipt = _host_reboot_json(raw)
    if any(type(outer[key]) is not str or re.fullmatch(r"(?:0|[1-9][0-9]*)", outer[key]) is None
           for key in ("memory_high", "memory_max")):
        raise RuntimeError("host_reboot external administrator envelope requires finite observed controls")
    expected = dict(schema_version=1, runner=_HOST_REBOOT_RUNNER, runner_id=outer["runner_id"],
                    host_boot_id=bound["after_boot"], outer_pid=outer["pid"], outer_start_ticks=outer["start_ticks"],
                    memory_high=int(outer["memory_high"]), memory_max=int(outer["memory_max"]),
                    guest_mem_size_mib=outer["guest_mem_size_mib"])
    if receipt != expected or any(type(receipt[key]) is not int for key in
                                  ("schema_version", "outer_pid", "outer_start_ticks", "memory_high", "memory_max", "guest_mem_size_mib")):
        raise RuntimeError("host_reboot POST requires the exact fresh external administrator envelope receipt")
    if outer["memory_high"] != str(expected["memory_high"]) or outer["memory_max"] != str(expected["memory_max"]):
        raise RuntimeError("host_reboot actual named-runner envelope differs from the external administrator receipt")


def _host_reboot_controller() -> dict[str, Any]:
    """Pin the configured resolved existing outer executable; never replace it."""
    path = _controller_executable_path()
    def identity(row):
        return [row.st_dev, row.st_ino, row.st_uid, row.st_gid, stat.S_IMODE(row.st_mode),
                row.st_nlink, row.st_size, row.st_mtime_ns, row.st_ctime_ns]
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or not before.st_mode & 0o111:
        raise RuntimeError("host_reboot outer controller must be an existing regular executable")
    digest = _sha256_runner_input(path)
    if identity(path.lstat()) != identity(before) or _controller_executable_path() != path:
        raise RuntimeError("host_reboot outer controller changed while pinning")
    return dict(path=str(path), command=shlex.split(MVM_BINARY), identity=identity(before), sha256=digest)


def _host_reboot_read_pre(task: Path, controller: dict) -> tuple[dict, bytes]:
    """Read exact retained PRE and current release inputs; no runner queries."""
    bound = _host_reboot_json(_host_reboot_read(task / "task.json"))
    if (set(bound) != _HOST_REBOOT_TASK_FIELDS or type(bound["schema_version"]) is not int
            or bound["schema_version"] != 1 or bound["runner"] != _HOST_REBOOT_RUNNER
            or re.fullmatch(r"/home/runner/\.cache/mvmctl-host-reboot-[0-9a-f]{32}", bound["guest_task"]) is None
            or bound["before_boot"] == bound["after_boot"] or controller != bound["controller"]
            or _sha256_runner_input(Path(MVM_CANDIDATE_BINARY)) != bound["candidate_sha256"]
            or "mvm " + _get_mvm_version() != bound["candidate_version"]
            or _sha256_runner_input(_REPO_ROOT / _HOST_REBOOT_FILE) != bound["test_sha256"]
            or {relative: _sha256_runner_input(_REPO_ROOT / relative) for relative in _HOST_REBOOT_SOURCE_FILES}
               != bound["source_payload"]
            or subprocess.check_output(["git", "-C", str(_REPO_ROOT), "rev-parse", "HEAD"], text=True).strip()
               != bound["source_revision"]
            or subprocess.check_output(["git", "-C", str(_REPO_ROOT), "status", "--porcelain"], text=True)):
        raise RuntimeError("host_reboot retained PRE binding is invalid or changed")
    pre = _host_reboot_read(task / "pre.json")
    outcomes = _host_reboot_read(task / "pre.outcomes.json")
    if (hashlib.sha256(pre).hexdigest() != bound["pre_sha256"]
            or hashlib.sha256(outcomes).hexdigest() != bound["pre_outcomes_sha256"]):
        raise RuntimeError("host_reboot retained PRE proof changed")
    if require_complete_pytest_outcomes(outcomes, process_returncode=0).collected != 1:
        raise RuntimeError("host_reboot PRE must prove exactly one passed node")
    receipt = _host_reboot_json(pre)
    if (receipt["boot_id"] != bound["before_boot"] or receipt["source"] != dict(revision=bound["source_revision"],
            test_sha256=bound["test_sha256"], payload=bound["source_payload"])
            or receipt["candidate"]["sha256"] != bound["candidate_sha256"]
            or receipt["candidate"]["version"] != bound["candidate_version"]):
        raise RuntimeError("host_reboot PRE source/candidate/boot proof changed")
    return bound, pre


def _host_reboot_validate_pre(task: Path, controller: dict) -> tuple[dict, bytes, dict]:
    """POST execution additionally requires the live fresh runner authority."""
    bound, pre = _host_reboot_read_pre(task, controller)
    if _host_reboot_fetch(bound, "pre.json") != pre:
        raise RuntimeError("host_reboot retained PRE proof changed")
    if _host_reboot_probe(bound) != bound["after_boot"]:
        raise RuntimeError("host_reboot current runner boot changed again")
    outer = _host_reboot_outer_identity()
    if any(outer[key] != bound["outer_after"][key] for key in ("runner_id", "created_at", "pid", "start_ticks")):
        raise RuntimeError("host_reboot POST outer task identity changed")
    _host_reboot_require_source(bound)
    return bound, pre, outer


_HOST_REBOOT_PROOF_FILES = ("task.json", "pre.terminal.json", "admin-envelope.json", "post-authority.json", "preservation.json",
                          *(f"{phase}.{suffix}" for phase in ("pre", "post")
                            for suffix in ("json", "outcomes.json", "native.json", "xml", "stdout", "stderr")))


def _host_reboot_proof_hashes(task: Path) -> dict[str, str]:
    return {name: hashlib.sha256(_host_reboot_read(task / name)).hexdigest() for name in _HOST_REBOOT_PROOF_FILES}


def _host_reboot_require_preservation(pre: dict, post: dict, raw: bytes) -> None:
    physical = _host_reboot_json(raw)
    if physical != dict(before=post["physical_before"], after_boot=post["physical_after_boot"], final=post["physical_after"]):
        raise RuntimeError("host_reboot raw preservation proof differs from POST")
    if physical["before"] != pre["physical"]:
        raise RuntimeError("host_reboot preservation baseline differs from PRE")
    for domain in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "traffic", "exec"):
        if physical["before"][domain] != physical["after_boot"][domain] or physical["before"][domain] != physical["final"][domain]:
            raise RuntimeError(f"host_reboot persistent {domain} changed")


def require_completed_host_reboot(task: Path) -> dict[str, Any]:
    """Historical closed runtime proof plus current release inputs; no live runner query.

    Completed POST recorded fresh controls then. Full-release quiescent-host admission
    remains unchanged; those historical controls are not claimed as current authority.
    """
    row = task.lstat()
    if (not task.is_absolute() or task.resolve() != task or not stat.S_ISDIR(row.st_mode)
            or row.st_uid != os.getuid() or stat.S_IMODE(row.st_mode) != 0o700):
        raise RuntimeError("host_reboot release proof task must be a private owned canonical directory")
    _require_distinct_candidate_controller()
    controller = _host_reboot_controller()
    bound, raw_pre = _host_reboot_read_pre(task, controller)
    terminal = _host_reboot_json(_host_reboot_read(task / "post.terminal.json"))
    if (set(terminal) != {"domain", "tier", "phase", "phase_complete", "passed", "release_verdict", "output", "proof_sha256"}
            or terminal["domain"] != "host_reboot" or terminal["tier"] != 2 or terminal["phase"] != "post"
            or terminal["phase_complete"] is not True or terminal["passed"] is not True
            or terminal["release_verdict"] != "PASS" or terminal["proof_sha256"] != _host_reboot_proof_hashes(task)):
        raise RuntimeError("host_reboot release proof lacks exact completed native closure")
    pre_terminal = _host_reboot_json(_host_reboot_read(task / "pre.terminal.json"))
    if (pre_terminal["phase"] != "pre" or pre_terminal["phase_complete"] is not True
            or pre_terminal["passed"] is not False or pre_terminal["release_verdict"] != "INCOMPLETE"):
        raise RuntimeError("host_reboot release proof lacks the incomplete PRE checkpoint")
    for phase in ("pre", "post"):
        native = _host_reboot_json(_host_reboot_read(task / f"{phase}.native.json"))
        if native != dict(phase=phase, returncode=0) or type(native["returncode"]) is not int:
            raise RuntimeError("host_reboot release proof has a nonzero or malformed native result")
        _host_reboot_require_phase_report(phase, _host_reboot_read(task / f"{phase}.outcomes.json"),
                                         _host_reboot_read(task / f"{phase}.xml"), native["returncode"])
    authority = _host_reboot_json(_host_reboot_read(task / "post-authority.json"))
    if (set(authority) != {"schema_version", "runner", "host_boot_id", "outer", "controller"}
            or type(authority["schema_version"]) is not int or authority["schema_version"] != 1
            or authority["runner"] != _HOST_REBOOT_RUNNER or authority["host_boot_id"] != bound["after_boot"]
            or authority["controller"] != controller
            or authority["outer"]["guest_mem_size_mib"] != 6144
            or any(authority["outer"][key] != bound["outer_after"][key]
                   for key in ("runner_id", "created_at", "pid", "start_ticks", "guest_mem_size_mib"))):
        raise RuntimeError("host_reboot historical POST authority differs from its bound reboot")
    _host_reboot_require_envelope(_host_reboot_read(task / "admin-envelope.json"), bound, authority["outer"])
    pre, post = _host_reboot_json(raw_pre), _host_reboot_json(_host_reboot_read(task / "post.json"))
    if (type(post["schema_version"]) is not int or post["schema_version"] != 1
            or post["host_boot_id"] != bound["after_boot"] or post["vm_id"] != pre["vm"]["id"]
            or post["source"] != pre["source"] or post["candidate"] != pre["candidate"] or post["owned_cleanup"] is not True):
        raise RuntimeError("host_reboot release proof lacks exact source/candidate/owned cleanup")
    _host_reboot_require_preservation(pre, post, _host_reboot_read(task / "preservation.json"))
    if _host_reboot_controller() != controller:
        raise RuntimeError("host_reboot release authority changed during proof consumption")
    return dict(terminal, output="Completed historical PRE+host reboot+POST proof consumed; live runner controls not asserted")


def run_host_reboot_phase(phase: str, task: Path, envelope: Path | None) -> dict[str, Any]:
    """Closed named-runner continuations; PRE is never aggregate success, no root-control fallback."""
    result = dict(domain="host_reboot", tier=2, phase=phase, phase_complete=False, passed=False,
                  release_verdict="INCOMPLETE", output="")
    task_valid = False
    try:
        if phase not in ("pre", "post") or (phase == "post") != (envelope is not None):
            raise RuntimeError("host_reboot requires an exact PRE or POST continuation")
        row = task.lstat()
        if (not task.is_absolute() or task.resolve() != task or not stat.S_ISDIR(row.st_mode)
                or row.st_uid != os.getuid() or stat.S_IMODE(row.st_mode) != 0o700):
            raise RuntimeError("host_reboot task must be a private owned canonical directory")
        task_valid = True
        _require_distinct_candidate_controller()
        controller = _host_reboot_controller()
        if phase == "pre":
            if (task / "task.json").exists():
                raise RuntimeError("host_reboot PRE receipt already exists; no replay")
            kernel_id, kernel_sha = (os.environ.get(key, "") for key in ("MVM_RUNNER_KERNEL_ID", "MVM_RUNNER_KERNEL_SHA256"))
            if any(re.fullmatch(r"[0-9a-f]{64}", value) is None for value in (kernel_id, kernel_sha)):
                raise RuntimeError("host_reboot requires the existing exact imported ELF binding; no fallback")
            revision = subprocess.check_output(["git", "-C", str(_REPO_ROOT), "rev-parse", "HEAD"], text=True).strip()
            if subprocess.check_output(["git", "-C", str(_REPO_ROOT), "status", "--porcelain"], text=True):
                raise RuntimeError("host_reboot requires clean signed suite source")
            bound = dict(schema_version=1, runner=_HOST_REBOOT_RUNNER, controller=controller,
                source_revision=revision, test_sha256=_sha256_runner_input(_REPO_ROOT / _HOST_REBOOT_FILE),
                source_payload={relative: _sha256_runner_input(_REPO_ROOT / relative) for relative in _HOST_REBOOT_SOURCE_FILES},
                candidate_sha256=_sha256_runner_input(Path(MVM_CANDIDATE_BINARY)), candidate_version="mvm " + _get_mvm_version(),
                kernel_id=kernel_id, kernel_sha256=kernel_sha,
                guest_task="/home/runner/.cache/mvmctl-host-reboot-" + uuid.uuid4().hex,
                outer_before=_host_reboot_outer_identity(), outer_after=None, before_boot=None, after_boot=None,
                pre_sha256=None, pre_outcomes_sha256=None)
            bound["before_boot"] = _host_reboot_probe(bound)
            _host_reboot_write(task, "intent.json", (json.dumps(bound, sort_keys=True) + "\n").encode())
            guest = bound["guest_task"]
            _host_reboot_ssh(f"mkdir -m 700 -- {guest} && mkdir -p -- {guest}/source/tests {guest}/tmp",
                              connect_timeout=10, timeout=30)
            _host_reboot_stage_source(bound)
            raw = _host_reboot_pytest(bound, "pre", task)
            pre = _host_reboot_json(raw)
            if (pre["boot_id"] != bound["before_boot"] or pre["source"] !=
                    dict(revision=revision, test_sha256=bound["test_sha256"], payload=bound["source_payload"])
                    or pre["candidate"]["sha256"] != bound["candidate_sha256"]):
                raise RuntimeError("host_reboot PRE evidence does not match the fixed source/candidate/boot")
            bound["pre_sha256"] = hashlib.sha256(raw).hexdigest()
            bound["pre_outcomes_sha256"] = hashlib.sha256(_host_reboot_read(task / "pre.outcomes.json")).hexdigest()
            # Force reboot may discard close-only writes: persist source, reports and
            # the owned nested VM's marker filesystem before the abrupt boundary.
            _host_reboot_ssh(shlex.join(["sync", "-f", "--", guest]), connect_timeout=10, timeout=60)
            current = _host_reboot_outer_identity()
            if current != bound["outer_before"] or _host_reboot_controller() != controller:
                raise RuntimeError("host_reboot outer authority changed before lifecycle")
            mvm("vm", "reboot", _HOST_REBOOT_RUNNER, "--force", timeout=120)
            deadline = time.monotonic() + 180
            while True:
                try:
                    boot = _host_reboot_probe(bound)
                    if boot == bound["before_boot"]:
                        raise RuntimeError("host_reboot did not change runner boot identity")
                    break
                except _HostRebootReconnectUnavailable:
                    if time.monotonic() >= deadline:
                        raise
                    time.sleep(1)
            bound["after_boot"] = boot
            bound["outer_after"] = _host_reboot_outer_identity()
            if any(bound["outer_after"][key] != bound["outer_before"][key] for key in ("runner_id", "created_at")):
                raise RuntimeError("host_reboot replaced the runner identity")
            if _host_reboot_controller() != controller:
                raise RuntimeError("host_reboot changed the outer controller")
            _host_reboot_require_source(bound)
            _host_reboot_write(task, "task.json", (json.dumps(bound, sort_keys=True) + "\n").encode())
            result.update(phase_complete=True, output="PRE PHASE COMPLETE / RELEASE VERDICT INCOMPLETE; external fresh-boot admin receipt required")
        else:
            bound, pre, outer = _host_reboot_validate_pre(task, controller)
            raw_envelope = _host_reboot_read(envelope)
            _host_reboot_require_envelope(raw_envelope, bound, outer)
            _host_reboot_write(task, "admin-envelope.json", raw_envelope)
            _host_reboot_write(task, "post-authority.json", json.dumps(dict(schema_version=1,
                runner=_HOST_REBOOT_RUNNER, host_boot_id=bound["after_boot"], outer=outer, controller=controller)).encode())
            post = _host_reboot_json(_host_reboot_pytest(bound, "post", task))
            raw_preservation = _host_reboot_fetch(bound, "preservation.json")
            _host_reboot_write(task, "preservation.json", raw_preservation)
            _host_reboot_require_preservation(_host_reboot_json(pre), post, raw_preservation)
            if (post["host_boot_id"] != bound["after_boot"] or post["owned_cleanup"] is not True
                    or post["vm_id"] != _host_reboot_json(pre)["vm"]["id"]
                    or post["source"] != _host_reboot_json(pre)["source"]
                    or post["candidate"] != _host_reboot_json(pre)["candidate"]
                    or _host_reboot_probe(bound) != bound["after_boot"]
                    or _host_reboot_outer_identity() != outer
                    or _host_reboot_controller() != controller):
                raise RuntimeError("host_reboot POST lacks full journey/cleanup/preservation proof")
            result.update(phase_complete=True, passed=True, release_verdict="PASS", output="PRE+changed host boot+POST+owned cleanup passed")
            result["proof_sha256"] = _host_reboot_proof_hashes(task)
    except Exception as exc:
        result["output"] = str(exc)
    if task_valid and not (task / f"{phase}.terminal.json").exists():
        try:
            _host_reboot_write(task, f"{phase}.terminal.json", (json.dumps(result, sort_keys=True) + "\n").encode())
        except Exception as exc:
            result.update(phase_complete=False, passed=False, release_verdict="INCOMPLETE",
                          output=result["output"] + f"; host_reboot terminal retention failed: {exc}")
    return result


def run_tier1_domain(
    domain: str,
    test_files: list[str],
    mvm_version: str,
    push: bool = False,
    *,
    binding: ImportedRunnerKernelBinding | None = None,
) -> dict[str, Any]:
    """Full lifecycle for one Tier 1 domain. Max 5 minutes per domain."""
    vm_name = _unique_name(f"t1-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 1,
        "passed": False,
        "output": "",
    }
    created = False

    def mark_created() -> None:
        nonlocal created
        created = True

    try:
        provision_t1(
            vm_name,
            mvm_version,
            mark_created,
            **({"binding": binding} if binding is not None else {}),
        )
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
        if created:
            _record_runner_destruction(result, vm_name)

    return result


def run_tier2_domain(
    domain: str,
    test_files: list[str],
    mvm_version: str,
    push: bool = False,
    *,
    binding: ImportedRunnerKernelBinding | None = None,
) -> dict[str, Any]:
    """Full lifecycle for one Tier 2 domain."""
    if domain == "host_reboot":
        return {"domain": domain, "tier": 2, "passed": False,
                "output": "host_reboot requires its explicit named-runner PRE/POST continuation; no phase executed"}
    vm_name = _unique_name(f"t2-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 2,
        "passed": False,
        "output": "",
    }
    created = False

    def mark_created() -> None:
        nonlocal created
        created = True

    try:
        provision_t2(
            vm_name,
            mvm_version,
            mark_created,
            **({"binding": binding} if binding is not None else {}),
        )
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
        if created:
            _record_runner_destruction(result, vm_name)

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


def _timed_out_domain_result(
    future: Future[dict[str, Any]],
    domain: str,
    tier: int,
    timeout: int,
) -> dict[str, Any]:
    """Build one failed result after the timed-out worker is contained."""
    output = [f"Domain timed out after {timeout}s"]
    if future.cancelled():
        output.append("Worker was canceled before it started")
    else:
        try:
            late_result = future.result()
        except Exception as exc:
            output.append(f"Late worker error: {exc}")
        else:
            late_output = str(late_result.get("output", "")).rstrip()
            if late_output:
                output.append(f"Late worker output:\n{late_output}")

    return {
        "domain": domain,
        "tier": tier,
        "passed": False,
        "output": "\n".join(output),
    }


def run_domains(
    domains: dict[str, list[str]],
    tier: int,
    runner_fn,
    workers: int,
    mvm_version: str,
    push: bool = False,
    *,
    binding: ImportedRunnerKernelBinding | None = None,
) -> list[dict[str, Any]]:
    """Run a set of domains in parallel using the given runner function."""
    if not domains:
        return []

    log(f"Tier {tier}: running {len(domains)} domain(s) with {workers} worker(s)...")
    results: list[dict[str, Any]] = []
    # Overall timeout per domain: 15 minutes.
    domain_timeout = 900
    timed_out: dict[Future[dict[str, Any]], str] = {}

    pool = ThreadPoolExecutor(max_workers=workers)
    try:
        future_map = {
            pool.submit(
                runner_fn,
                domain,
                files,
                mvm_version,
                push,
                **({"binding": binding} if binding is not None else {}),
            ): domain
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
                # Running threads cannot be force-canceled safely. Cancel work
                # that has not started, then join active workers before moving on.
                log(
                    f"  [TIMEOUT] {len(remaining)} domain(s) timed out after "
                    f"{domain_timeout}s — containing workers before continuing"
                )
                for future in remaining:
                    future.cancel()
                    timed_out[future] = future_map[future]
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
        pool.shutdown(wait=True, cancel_futures=True)

    for future, domain in sorted(timed_out.items(), key=lambda item: item[1]):
        domain_result = _timed_out_domain_result(
            future,
            domain,
            tier,
            domain_timeout,
        )
        results.append(domain_result)
        log(f"  [TIMEOUT] {domain}")
        _print_failure(domain, domain_result)

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
    if args.host_reboot_task is None:
        parser.error("--release-qualification requires --host-reboot-task with completed strict POST evidence")


def _parse_release_host_vm_list(payload: str) -> list[dict[str, object]]:
    """Parse the outer controller's versioned, caller-scoped VM list."""
    try:
        payload_size = len(payload.encode("utf-8"))
    except UnicodeEncodeError as exc:
        raise RuntimeError(
            f"release host VM list is not valid UTF-8: {exc}"
        ) from exc
    if payload_size > MAX_RELEASE_HOST_VM_LIST_BYTES:
        raise RuntimeError(
            "release host VM list exceeds "
            f"{MAX_RELEASE_HOST_VM_LIST_BYTES} bytes"
        )

    def reject_duplicate_fields(
        pairs: list[tuple[str, object]],
    ) -> dict[str, object]:
        values: dict[str, object] = {}
        names_by_casefold: dict[str, str] = {}
        for field, value in pairs:
            if field in values:
                raise RuntimeError(
                    f"release host VM list has duplicate field: {field}"
                )
            folded = field.casefold()
            if folded in names_by_casefold:
                colliding = sorted((names_by_casefold[folded], field))
                raise RuntimeError(
                    "release host VM list has case-fold duplicate fields: "
                    f"{', '.join(colliding)}"
                )
            values[field] = value
            names_by_casefold[folded] = field
        return values

    def reject_nonstandard_constant(value: str) -> None:
        raise RuntimeError(
            f"release host VM list has non-standard JSON value: {value}"
        )

    try:
        envelope = json.loads(
            payload,
            object_pairs_hook=reject_duplicate_fields,
            parse_constant=reject_nonstandard_constant,
        )
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"release host VM list contains malformed JSON: {exc}"
        ) from exc
    if not isinstance(envelope, dict):
        raise RuntimeError("release host VM list must be a JSON response envelope")
    expected_envelope_fields = {"schema_version", "status", "data", "error"}
    if set(envelope) != expected_envelope_fields:
        raise RuntimeError("release host VM list has incomplete or unknown response fields")
    if type(envelope["schema_version"]) is not int or envelope["schema_version"] != 1:
        raise RuntimeError("release host VM list has unsupported response schema")
    if envelope["status"] != "success" or envelope["error"] is not None:
        raise RuntimeError("release host VM list is not a successful response")
    vms = envelope["data"]
    if not isinstance(vms, list):
        raise RuntimeError("release host VM list data must be a JSON list")
    if len(vms) > MAX_RELEASE_HOST_VM_LIST_ENTRIES:
        raise RuntimeError(
            "release host VM list exceeds "
            f"{MAX_RELEASE_HOST_VM_LIST_ENTRIES} entries"
        )

    seen_ids: set[str] = set()
    seen_names: set[str] = set()
    for index, vm in enumerate(vms):
        if not isinstance(vm, dict):
            raise RuntimeError(
                f"release host VM list entry {index} must be an object"
            )
        missing = sorted(_RELEASE_VM_FIELDS - set(vm))
        if missing:
            raise RuntimeError(
                f"release host VM list entry {index} missing fields: "
                f"{', '.join(missing)}"
            )
        for field in sorted(_RELEASE_VM_FIELDS):
            value = vm[field]
            if type(value) is not str:
                raise RuntimeError(
                    f"release host VM list entry {index} field {field} "
                    "must be a string"
                )
            if not value:
                raise RuntimeError(
                    f"release host VM list entry {index} field {field} "
                    "must not be empty"
                )
            if len(value) > MAX_RELEASE_HOST_VM_FIELD_LENGTH:
                raise RuntimeError(
                    f"release host VM list entry {index} field {field} exceeds "
                    f"{MAX_RELEASE_HOST_VM_FIELD_LENGTH} characters"
                )
        status = vm["status"]
        if status not in _RELEASE_VM_STATUSES:
            raise RuntimeError(
                f"release host VM list entry {index} has unknown status: "
                f"{status}"
            )
        vm_id = vm["id"]
        name = vm["name"]
        if vm_id in seen_ids:
            raise RuntimeError(f"release host VM list has duplicate VM id: {vm_id}")
        if name in seen_names:
            raise RuntimeError(f"release host VM list has duplicate VM name: {name}")
        seen_ids.add(vm_id)
        seen_names.add(name)
    return vms


def _require_release_host_without_active_vms() -> None:
    """Reject caller-visible active VMs; this is not host-wide quiescence proof."""
    if MVM_BINARY != "/usr/local/bin/mvm":
        raise RuntimeError(
            "release host VM admission requires exact /usr/local/bin/mvm"
        )
    result = mvm("vm", "ls", "--json", check=True, timeout=15)
    if type(result.returncode) is not int or result.returncode != 0:
        raise RuntimeError(
            "outer controller vm ls --json failed with "
            f"rc={result.returncode}"
        )
    if type(result.stdout) is not str:
        raise RuntimeError("outer controller vm ls --json returned no stdout")
    for vm in _parse_release_host_vm_list(result.stdout):
        if vm["status"] != "stopped":
            raise RuntimeError(
                "release host VM is not safely stopped: "
                f"{vm['name']} ({vm['status']})"
            )


def _validate_system_test_registry(parser: argparse.ArgumentParser) -> None:
    """Reject an ambiguous or incomplete system-test domain registry."""
    seen: set[str] = set()
    duplicate_domains: set[str] = set()
    empty_domains: list[str] = []
    registered_files: list[str] = []
    for domains in (TIER1_DOMAINS, TIER2_DOMAINS, TIER3_DOMAINS, HOST_REBOOT_PHASE_DOMAINS):
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
        **HOST_REBOOT_PHASE_DOMAINS,
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
    parser.add_argument("--host-reboot-phase", choices=("pre", "post"),
                        help="Explicit host_reboot continuation on mvmctl-030-test only; PRE is not a release verdict.")
    parser.add_argument("--host-reboot-task", type=Path,
                        help="Private retained host_reboot receipt directory; also mandatory read-only completed POST input for full release qualification.")
    parser.add_argument("--host-reboot-envelope", type=Path,
                        help="POST-only external administrator receipt for the fresh runner boot and truthful current finite controls.")
    return parser


def parse_args() -> argparse.Namespace:
    return _build_parser().parse_args()


def run_prepare(
    *,
    rebuild_volume: bool = False,
    rebuild_image: bool = False,
    binding: ImportedRunnerKernelBinding | None = None,
) -> None:
    """Smoke-test the provisioning pipeline.

    Builds a custom base image from ubuntu-minimal:noble with all test
    dependencies pre-installed, then validates T1 and T2 provisioning.

    Only forces a rebuild of the shared volume or base image when the
    corresponding flag is True.
    """
    log("=== Prepare: provisioning pipeline ===")
    if binding is not None:
        _validate_imported_runner_kernel(binding)

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
    if binding is None:
        mvm(
            "kernel",
            "pull",
            "official:7.0.11",
            "--default",
            "--features",
            "all",
            timeout=900,
        )
    else:
        _validate_imported_runner_kernel(binding)

    # --- Step 4: Build custom base image ---
    log("[4/8] Building custom base image...")
    _build_base_image(
        mvm_version,
        rebuild=rebuild_image,
        **({"binding": binding} if binding is not None else {}),
    )
    image_key = _runner_image_key(mvm_version, binding)

    # --- Step 5: Create T1 VM from custom image + volume ---
    t1 = _unique_name("prep-t1")
    log(f"[5/8] Creating T1 VM '{t1}' from {BASE_IMAGE_NAME}:{mvm_version} + volume...")
    if binding is not None:
        _validate_imported_runner_kernel(binding)
    mvm(
        "vm",
        "create",
        t1,
        "--image",
        image_key,
        *(["--kernel", binding.id] if binding is not None else []),
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
        if binding is not None:
            _require_runner_base_receipt(t1, mvm_version, binding)
        log(f"      Running mvm init inside '{t1}'...")
        _initialize_runner_user(t1, timeout=90)
        if binding is not None:
            _require_imported_runner_smoke(t1, binding)
    finally:
        destroy_vm(t1)

    # --- Step 5: Create T2 VM from custom image + shared volume ---
    t2 = _unique_name("prep-t2")
    log(
        f"[6/8] Creating T2 VM '{t2}' from {BASE_IMAGE_NAME}:{mvm_version} + shared volume..."
    )
    if binding is not None:
        _validate_imported_runner_kernel(binding)
    mvm(
        "vm",
        "create",
        t2,
        "--image",
        image_key,
        "--kernel",
        binding.id if binding is not None else "official:7.0.11",
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
        if binding is not None:
            _require_runner_base_receipt(t2, mvm_version, binding)
        log(f"[7/8] Setting up '{t2}' (mount + init)...")
        _initialize_runner_user(t2, timeout=180)
        if binding is not None:
            _require_imported_runner_smoke(t2, binding)

        log("[8/8] Validating cache hit (pulling 1 asset)...")
        result = mvm(
            "exec",
            t2,
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
            "MVM_ASSET_MIRROR=/mnt MVM_LOG_LEVEL=INFO /usr/local/bin/mvm image pull alpine:3.23",
            timeout=150,
            check=False,
        )
        _require_local_mirror_pull(result, "prepare Alpine image")

        vol_check = mvm("volume", "inspect", SHARED_VOLUME_NAME, "--json", timeout=15)
        volume = _volume_inspect_record(vol_check.stdout, SHARED_VOLUME_NAME)
        vol_status = volume.get("status", "unknown")

        log("=== Prepare: ALL STEPS PASSED ===")
        log(f"  Base image: {image_key}")
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

    if args.host_reboot_phase or args.host_reboot_envelope or "host_reboot" in args.domains:
        if (args.domains != ["host_reboot"] or not args.host_reboot_phase or not args.host_reboot_task
                or args.all or args.tier or args.host_direct or args.release_qualification
                or args.prepare or args.image or args.volume or args.rebuild or args.push
                or args.candidate_version or args.skip_volume_check
                or (args.host_reboot_phase == "post") != bool(args.host_reboot_envelope)):
            parser.error("host_reboot requires only its exact domain, phase/task and POST-only admin envelope receipt")
        result = run_host_reboot_phase(args.host_reboot_phase, args.host_reboot_task, args.host_reboot_envelope)
        print(json.dumps(result, sort_keys=True), flush=True)
        # PRE transport/lifecycle completion is deliberately not aggregate qualification success.
        sys.exit(0 if result.get("phase_complete") and (args.host_reboot_phase == "pre" or result["passed"]) else 1)
    if args.host_reboot_task and not args.release_qualification:
        parser.error("--host-reboot-task requires explicit host_reboot phase or full --release-qualification")

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

    try:
        binding = _resolve_imported_runner_kernel_binding()
    except RuntimeError as exc:
        parser.error(str(exc))
    if binding is not None:
        if args.skip_volume_check:
            parser.error("imported runner kernel binding forbids --skip-volume-check")
        selects_runners = (
            args.all
            or (args.tier and any(t in (1, 2) for t in args.tier))
            or any(d in TIER1_DOMAINS or d in TIER2_DOMAINS for d in args.domains)
        )
        if selects_runners and not (args.prepare or args.rebuild):
            parser.error(
                "imported runner kernel T1/T2 execution requires --prepare on every invocation"
            )

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
            _require_release_host_without_active_vms()
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
    if args.release_qualification:
        try:
            # Historical runtime proof, current inputs; no live runner/lifecycle query.
            require_completed_host_reboot(args.host_reboot_task)
        except Exception as exc:
            log(f"ERROR: completed host_reboot proof refused: {exc}")
            sys.exit(1)
    if args.rebuild:
        run_prepare(
            rebuild_volume=True,
            rebuild_image=True,
            **({"binding": binding} if binding is not None else {}),
        )
        if not test_flags:
            return
    elif args.prepare or args.image or args.volume:
        rebuild_volume = args.volume
        rebuild_image = args.image
        if args.prepare:
            run_prepare(
                rebuild_volume=rebuild_volume,
                rebuild_image=rebuild_image,
                **({"binding": binding} if binding is not None else {}),
            )
        elif args.image:
            # The builder VM attaches the shared volume, so ensure it exists first.
            ensure_shared_volume(rebuild=rebuild_volume)
            ensure_test_network()
            mvm_version = _get_mvm_version()
            _build_base_image(
                mvm_version,
                rebuild=True,
                **({"binding": binding} if binding is not None else {}),
            )
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
                tier1,
                1,
                run_tier1_domain,
                args.workers,
                mvm_version,
                push=args.push,
                **({"binding": binding} if binding is not None else {}),
            )
            all_results.extend(results)
        elif tier == 2 and tier2:
            results = run_domains(
                tier2,
                2,
                run_tier2_domain,
                args.workers,
                mvm_version,
                push=args.push,
                **({"binding": binding} if binding is not None else {}),
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

    if args.release_qualification:
        # Revalidate retained evidence/current release inputs after the batch, not runner liveness.
        try:
            all_results.append(require_completed_host_reboot(args.host_reboot_task))
        except Exception as exc:
            log(f"ERROR: completed host_reboot proof refused after batch: {exc}")
            sys.exit(1)
    else:
        log("BATCH RESULTS ONLY / RELEASE VERDICT INCOMPLETE: explicit host_reboot PRE/POST proof remains mandatory")
    print_summary(all_results)


if __name__ == "__main__":
    # Import here to avoid circular import issues with argparse processing
    import shutil

    main()
