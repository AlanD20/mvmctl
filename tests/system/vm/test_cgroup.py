"""Typed cgroup-v2 VM resource envelope system specification — Tier 2."""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.tier2,
    pytest.mark.needs_kvm,
    pytest.mark.slow,
]

_CGROUP_CONFIG_OVERRIDES = {
    "cgroup_vmm_headroom_mib": "128",
    "cgroup_cpu_weight": "100",
    "cgroup_pids_max": "256",
    "cgroup_swap_max_bytes": "0",
}


@dataclass(frozen=True)
class _ConfigOverride:
    exists: bool
    value: str


def _read_cgroup_config_overrides(
    runner_vm: str,
) -> dict[str, _ConfigOverride]:
    """Capture each cgroup setting's exact integer override, if present."""
    listing = _run_mvm(
        runner_vm, "config", "get", "defaults.vm"
    ).stdout
    snapshots: dict[str, _ConfigOverride] = {}
    for key in _CGROUP_CONFIG_OVERRIDES:
        match = re.search(
            rf"^\s*{re.escape(key)} = (?P<effective>-?\d+) "
            r"\((?P<source>override|default): (?P<stored>-?\d+), "
            r"type: int\)$",
            listing,
            flags=re.MULTILINE,
        )
        assert match is not None, (
            f"could not capture defaults.vm.{key} override state from "
            "the config category listing"
        )
        assert match.group("effective") == match.group("stored"), (
            f"defaults.vm.{key} effective and stored values disagree in: "
            f"{match.group(0)!r}"
        )
        is_override = match.group("source") == "override"
        snapshots[key] = _ConfigOverride(
            exists=is_override,
            value=match.group("stored"),
        )
    return snapshots


@pytest.fixture
def cgroup_config_overrides(runner_vm: str) -> Iterator[None]:
    """Apply cgroup test overrides and restore the exact prior state."""
    originals = _read_cgroup_config_overrides(runner_vm)
    modified: list[str] = []
    try:
        for key, value in _CGROUP_CONFIG_OVERRIDES.items():
            modified.append(key)
            _run_mvm(
                runner_vm, "config", "set", "defaults.vm", key, value
            )
        yield
    finally:
        failures: list[str] = []
        for key in modified:
            original = originals[key]
            # Collect each result so one failure cannot skip the remaining keys.
            if original.exists:
                result = _run_mvm(
                    runner_vm,
                    "config",
                    "set",
                    "defaults.vm",
                    key,
                    original.value,
                    check=False,
                )
            else:
                result = _run_mvm(
                    runner_vm,
                    "config",
                    "reset",
                    "defaults.vm",
                    key,
                    check=False,
                )
            if result.returncode != 0:
                failures.append(
                    f"defaults.vm.{key}: rc={result.returncode}, "
                    f"stdout={result.stdout!r}, stderr={result.stderr!r}"
                )

        restored = _read_cgroup_config_overrides(runner_vm)
        for key in modified:
            if restored[key] != originals[key]:
                failures.append(
                    f"defaults.vm.{key}: expected {originals[key]!r}, "
                    f"got {restored[key]!r}"
                )
        assert not failures, "cgroup config restoration failed:\n" + "\n".join(
            failures
        )


def _vm_entry(runner_vm: str, vm_name: str) -> dict[str, Any] | None:
    entries = _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout
    )
    return next((entry for entry in entries if entry.get("name") == vm_name), None)


def _require_preseeded_vm_assets(runner_vm: str) -> None:
    """Check the existing defaults used by vm create without mutating preparation."""
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).expanduser().is_dir(), "asset mirror directory must be preseeded"
    kernels = _json_success_data(_run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list)
    images = _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)
    binaries = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
    default_kernels = [kernel for kernel in kernels if kernel.get("is_default")]
    default_images = [image for image in images if image.get("is_default")]
    assert len(default_kernels) == 1 and default_kernels[0].get("is_present"), kernels
    assert len(default_images) == 1 and default_images[0].get("is_present"), images
    # The tests omit --image/--kernel, so validate those defaults rather than choosing a version.
    pair: dict[str, dict[str, Any]] = {}
    for kind in ("firecracker", "jailer"):
        defaults = [binary for binary in binaries if binary.get("type") == kind and binary.get("is_default")]
        assert len(defaults) == 1, (kind, binaries)
        binary = defaults[0]
        assert binary.get("is_present") and binary.get("version") == "1.16.2", binary
        pair[kind] = binary
    assert pair["firecracker"].get("architecture") == pair["jailer"].get("architecture"), pair


_CONTROL_RESERVE_BYTES = 16 * 1024 * 1024
_SAFETY_FLOOR_BYTES = 8 * 1024 * 1024
_MAX_APPEND_BYTES = 16 * 1024 * 1024
_APPEND_QUANTUM_BYTES = 1024 * 1024
_MAX_ALLOCATION_STAGES = 12
_SETUP_BUDGET_SECONDS = 180
_GUEST_ACTION_SECONDS = 8
_MAX_CALIBRATION_STEPS = 4
_CALIBRATION_MIN_AVAILABLE_BYTES = 10 * 1024 * 1024
_CALIBRATION_BUDGET_SECONDS = 60


def _guest_working_set_action(
    runner_vm: str, vm_name: str, script: str, deadline: float | None = None,
) -> subprocess.CompletedProcess[str]:
    """Bound transport separately from the independent guest workload deadline."""
    del runner_vm  # System specifications execute directly inside the runner.
    timeout = 12.0
    if deadline is not None:
        timeout = min(timeout, deadline - time.monotonic())
    assert timeout > 0, "working-set setup/action deadline exhausted"
    return subprocess.run(
        ["/usr/local/bin/mvm", "exec", vm_name, "--",
         f"timeout -s KILL {_GUEST_ACTION_SECONDS} sh -c {shlex.quote(script)}"],
        capture_output=True, text=True, timeout=timeout, check=False,
    )


def _prepare_guest_working_set(
    runner_vm: str, vm_name: str, initial_boot_id: str, record_property: Any,
) -> tuple[int, dict[str, str]]:
    """Build retained tmpfs pages in measured steps without injecting guest OOM."""
    started = time.monotonic()
    deadline = started + _SETUP_BUDGET_SECONDS
    stages: list[dict[str, Any]] = []
    allocated_bytes = 0
    stop_reason = "stage_limit"
    settled: dict[str, str] = {}
    try:
        prepared = _guest_working_set_action(
            runner_vm, vm_name,
            "set -eu; command -v timeout; command -v dd; mkdir /mnt/mvm-saturation; "
            "mount -t tmpfs -o size=224M tmpfs /mnt/mvm-saturation; "
            ": > /mnt/mvm-saturation/fill",
            deadline,
        )
        assert prepared.returncode == 0, (prepared.stdout, prepared.stderr)
        for index in range(_MAX_ALLOCATION_STAGES):
            fresh = _guest_saturation_sample(runner_vm, vm_name, deadline)
            settled = fresh
            assert fresh["boot_id"] == initial_boot_id, fresh
            assert int(fresh["swap_total_kib"]) == 0, fresh
            available_bytes = int(fresh["available_kib"]) * 1024
            assert available_bytes >= _SAFETY_FLOOR_BYTES, fresh
            append_bytes = min(
                _MAX_APPEND_BYTES, max(0, available_bytes - _CONTROL_RESERVE_BYTES),
            ) // _APPEND_QUANTUM_BYTES * _APPEND_QUANTUM_BYTES
            if append_bytes == 0:
                stop_reason = "control_reserve"
                break
            stage: dict[str, Any] = {
                "index": index, "guest_before": fresh, "append_bytes": append_bytes, "sweeps": [],
            }
            stages.append(stage)
            allocation = _guest_working_set_action(
                runner_vm, vm_name,
                f"exec dd if=/dev/zero bs=1M count={append_bytes // _APPEND_QUANTUM_BYTES} "
                "conv=fsync >> /mnt/mvm-saturation/fill",
                deadline,
            )
            stage["allocation"] = {
                "returncode": allocation.returncode, "stdout": allocation.stdout,
                "stderr": allocation.stderr,
            }
            # A successful allocation must precede sweeps; failures retain their exact command result.
            assert allocation.returncode == 0, stage
            allocated_bytes += append_bytes
            for sweep_index in range(2):
                sweep = _guest_working_set_action(
                    runner_vm, vm_name,
                    "exec dd if=/mnt/mvm-saturation/fill of=/dev/null bs=1M", deadline,
                )
                settled = _guest_saturation_sample(runner_vm, vm_name, deadline)
                stage["sweeps"].append({
                    "index": sweep_index, "returncode": sweep.returncode,
                    "stderr": sweep.stderr, "guest": settled,
                })
                assert sweep.returncode == 0, stage
                assert settled["boot_id"] == initial_boot_id, settled
                assert int(settled["swap_total_kib"]) == 0, settled
                assert int(settled["size"]) == allocated_bytes, stage
                assert int(settled["shmem_kib"]) * 1024 >= allocated_bytes, stage
                physical_bytes = int(settled["blocks"]) * 512
                physical_append = physical_bytes - int(fresh["blocks"]) * 512
                assert append_bytes <= physical_append <= _MAX_APPEND_BYTES, stage
                assert allocated_bytes <= physical_bytes <= _MAX_APPEND_BYTES * _MAX_ALLOCATION_STAGES, stage
                available_bytes = int(settled["available_kib"]) * 1024
                assert available_bytes >= _SAFETY_FLOOR_BYTES, stage
                if available_bytes <= _CONTROL_RESERVE_BYTES:
                    stop_reason = "control_reserve"
                    break
            if stop_reason == "control_reserve":
                break

        total_bytes = int(settled["total_kib"]) * 1024
        available_bytes = int(settled["available_kib"]) * 1024
        assert 0 < allocated_bytes <= _MAX_APPEND_BYTES * _MAX_ALLOCATION_STAGES, stages
        assert (total_bytes - available_bytes) * 5 >= total_bytes * 4, settled
        assert time.monotonic() <= deadline, stages
        return allocated_bytes, settled
    finally:
        record_property("saturation_setup", json.dumps({
            "elapsed_seconds": time.monotonic() - started, "stop_reason": stop_reason,
            "control_reserve_bytes": _CONTROL_RESERVE_BYTES,
            "safety_floor_bytes": _SAFETY_FLOOR_BYTES, "append_max_bytes": _MAX_APPEND_BYTES,
            "append_quantum_bytes": _APPEND_QUANTUM_BYTES,
            "stages_max": _MAX_ALLOCATION_STAGES, "budget_seconds": _SETUP_BUDGET_SECONDS,
            "guest_action_deadline_seconds": _GUEST_ACTION_SECONDS,
            "allocated_bytes": allocated_bytes, "settled_guest": settled, "stages": stages,
        }, sort_keys=True))


@dataclass
class _HostSwap:
    path: str | None
    leaf_allowance_bytes: int
    vm_name: str | None = None
    vm_removal_confirmed: bool = False


def _host_swap_paths() -> set[str]:
    return {
        line.split()[0]
        for line in Path("/proc/swaps").read_text().splitlines()[1:]
    }


@pytest.fixture(params=[False, True], ids=["host-swap-absent", "host-swap-present"])
def saturation_host_swap(
    request: pytest.FixtureRequest, runner_vm: str, record_property: Any,
) -> Iterator[_HostSwap]:
    """Change only this test's swapfile inside the disposable runner."""
    original_paths = _host_swap_paths()
    if not request.param:
        assert not original_paths, (
            "swap-absent qualification requires an already swap-free runner; "
            f"existing swap is preserved: {sorted(original_paths)}"
        )
        yield _HostSwap(path=None, leaf_allowance_bytes=0)
        assert not _host_swap_paths(), "runner swap availability changed during the test"
        return

    directory_name = f"saturation-swap-{uuid.uuid4().hex}"
    swap_path = f"/opt/mvmctl-test/{directory_name}/swap"
    create_script = """
import json
import os
import stat
import sys

name = sys.argv[1]
assert name.startswith("saturation-swap-") and len(name) == 48
assert all(character in "0123456789abcdef" for character in name[16:])
flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
parent = os.open("/", flags)
directory = None
created_directory = False
created_file = False
try:
    for component in (None, "opt", "mvmctl-test"):
        if component is not None:
            child = os.open(component, flags, dir_fd=parent)
            os.close(parent)
            parent = child
        metadata = os.fstat(parent)
        assert metadata.st_uid == metadata.st_gid == 0
        assert stat.S_IMODE(metadata.st_mode) & 0o022 == 0
    os.mkdir(name, 0o700, dir_fd=parent)
    created_directory = True
    directory = os.open(name, flags, dir_fd=parent)
    metadata = os.fstat(directory)
    assert metadata.st_uid == metadata.st_gid == 0
    assert stat.S_IMODE(metadata.st_mode) == 0o700
    file = os.open("swap", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                   0o600, dir_fd=directory)
    created_file = True
    with os.fdopen(file, "wb") as output:
        chunk = bytes(1024 * 1024)
        for _ in range(512):
            assert output.write(chunk) == len(chunk)
        output.flush()
        os.fsync(output.fileno())
        file_metadata = os.fstat(output.fileno())
        # Unsupported SEEK_DATA/SEEK_HOLE fails setup; never infer a hole-free file.
        assert os.lseek(output.fileno(), 0, os.SEEK_DATA) == 0
        assert os.lseek(output.fileno(), 0, os.SEEK_HOLE) == file_metadata.st_size
    assert stat.S_ISREG(file_metadata.st_mode)
    assert file_metadata.st_uid == file_metadata.st_gid == 0
    assert stat.S_IMODE(file_metadata.st_mode) == 0o600
    assert file_metadata.st_nlink == 1
    assert file_metadata.st_size == 512 * 1024 * 1024
    assert file_metadata.st_blocks * 512 >= file_metadata.st_size, (
        f"swapfile physical allocation is short: size={file_metadata.st_size}, "
        f"blocks={file_metadata.st_blocks}, allocated_bytes={file_metadata.st_blocks * 512}"
    )
    print(json.dumps({"directory": [metadata.st_dev, metadata.st_ino],
                      "file": [file_metadata.st_dev, file_metadata.st_ino],
                      "page_bytes": os.sysconf("SC_PAGE_SIZE")}))
except BaseException:
    if created_file:
        os.unlink("swap", dir_fd=directory)
    if created_directory:
        os.rmdir(name, dir_fd=parent)
    raise
finally:
    if directory is not None:
        os.close(directory)
    os.close(parent)
"""
    created = subprocess.run(
        ["sudo", "-n", "python3", "-c", create_script, directory_name],
        capture_output=True,
        text=True,
        timeout=90,
        check=False,
    )
    assert created.returncode == 0, (created.stdout, created.stderr)
    receipt = json.loads(created.stdout)
    cleanup_script = """
import json
import os
import stat
import subprocess
import sys

name, receipt = sys.argv[1], json.loads(sys.argv[2])
assert name.startswith("saturation-swap-") and len(name) == 48
assert all(character in "0123456789abcdef" for character in name[16:])
flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
parent = os.open("/", flags)
directory = None
try:
    for component in (None, "opt", "mvmctl-test"):
        if component is not None:
            child = os.open(component, flags, dir_fd=parent)
            os.close(parent)
            parent = child
        metadata = os.fstat(parent)
        assert metadata.st_uid == metadata.st_gid == 0
        assert stat.S_IMODE(metadata.st_mode) & 0o022 == 0
    directory = os.open(name, flags, dir_fd=parent)
    metadata = os.fstat(directory)
    assert [metadata.st_dev, metadata.st_ino] == receipt["directory"]
    assert metadata.st_uid == metadata.st_gid == 0
    assert stat.S_IMODE(metadata.st_mode) == 0o700
    metadata = os.stat("swap", dir_fd=directory, follow_symlinks=False)
    assert [metadata.st_dev, metadata.st_ino] == receipt["file"]
    assert stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1
    assert metadata.st_uid == metadata.st_gid == 0
    assert stat.S_IMODE(metadata.st_mode) == 0o600
    path = f"/opt/mvmctl-test/{name}/swap"
    paths = {line.split()[0] for line in open("/proc/swaps").read().splitlines()[1:]}
    if path in paths:
        subprocess.run(["/usr/sbin/swapoff", path], check=True, timeout=60)
    paths = {line.split()[0] for line in open("/proc/swaps").read().splitlines()[1:]}
    assert path not in paths, "test swapfile is still active; preserving its backing"
    os.unlink("swap", dir_fd=directory)
    os.rmdir(name, dir_fd=parent)
finally:
    if directory is not None:
        os.close(directory)
    os.close(parent)
"""
    availability = _HostSwap(path=swap_path, leaf_allowance_bytes=64 * 1024 * 1024)
    try:
        for command in ("mkswap", "swapon"):
            result = subprocess.run(
                ["sudo", "-n", f"/usr/sbin/{command}", swap_path],
                capture_output=True,
                text=True,
                timeout=90,
                check=False,
            )
            assert result.returncode == 0, (command, result.stdout, result.stderr)
        assert _host_swap_paths() == original_paths | {swap_path}
        entry = next(
            line.split()
            for line in Path("/proc/swaps").read_text().splitlines()[1:]
            if line.split()[0] == swap_path
        )
        assert entry[1] == "file", entry
        usable_bytes = int(entry[2]) * 1024
        page_bytes = receipt["page_bytes"]
        # /proc/swaps reports usable kernel pages, not the nominal backing-file length.
        assert availability.leaf_allowance_bytes <= usable_bytes <= 512 * 1024 * 1024 - page_bytes, (
            f"swap capacity outside test policy/file bounds: entry={entry!r}, "
            f"usable_bytes={usable_bytes}, leaf_allowance_bytes={availability.leaf_allowance_bytes}, "
            f"file_size_bytes={512 * 1024 * 1024}, header_page_bytes={page_bytes}"
        )
        assert usable_bytes % page_bytes == 0, (entry, usable_bytes, page_bytes)
        record_property("host_swap_usable_bytes", usable_bytes)
        yield availability
    finally:
        if availability.vm_name is not None:
            assert availability.vm_removal_confirmed, (
                "test VM teardown did not confirm removal; preserving its swap "
                f"prerequisite at {swap_path} until disposable-runner teardown"
            )
            assert _vm_entry(runner_vm, availability.vm_name) is None, (
                "test VM removal failed; preserving its active swap prerequisite "
                f"at {swap_path} until disposable-runner teardown"
            )
        removed = subprocess.run(
            ["sudo", "-n", "python3", "-c", cleanup_script,
             directory_name, json.dumps(receipt)],
            capture_output=True,
            text=True,
            timeout=90,
            check=False,
        )
        assert removed.returncode == 0, (removed.stdout, removed.stderr)
        assert _host_swap_paths() == original_paths
        assert not Path(swap_path).parent.exists()


@pytest.fixture
def saturation_vm(
    runner_vm: str,
    cgroup_config_overrides: None,
    saturation_host_swap: _HostSwap,
) -> Iterator[dict[str, Any]]:
    """Remove the bounded VM before its swap prerequisite is torn down."""
    _require_preseeded_vm_assets(runner_vm)
    _run_mvm(runner_vm, "config", "set", "defaults.vm", "cgroup_vmm_headroom_mib", "0")
    _run_mvm(
        runner_vm, "config", "set", "defaults.vm", "cgroup_swap_max_bytes",
        str(saturation_host_swap.leaf_allowance_bytes),
    )
    vm_name = f"sys-saturation-{uuid.uuid4().hex[:8]}"
    saturation_host_swap.vm_name = vm_name
    try:
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", vm_name,
                "--no-network", "--cloud-init-mode", "off", "--skip-deblob", "--no-pci",
                "--disk-size", "512M", "--vcpu", "1", "--mem", "224M", "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        yield {"name": vm_name, "id": created[0]["id"]}
    finally:
        remaining = _vm_entry(runner_vm, vm_name)
        if remaining is not None:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            assert _vm_entry(runner_vm, vm_name) is None
            assert not Path(f"/sys/fs/cgroup/mvmctl/{remaining['id']}").exists()
        saturation_host_swap.vm_removal_confirmed = True


def _pressure_hierarchy(
    runner_vm: str, vm_id: str, deadline: float | None = None,
) -> dict[str, Any]:
    """Keep finite ancestor ceilings and unavailable root counters in diagnostics."""
    script = """
import json
import sys
from pathlib import Path

vm_id = sys.argv[1]
assert len(vm_id) == 32 and all(character in "0123456789abcdef" for character in vm_id)
root = Path("/sys/fs/cgroup")
result = {}
for path in (root / "mvmctl" / vm_id, root / "mvmctl", root):
    values = {}
    for name in ("memory.high", "memory.max", "memory.current", "memory.peak", "memory.events", "memory.stat",
                 "memory.swap.max", "memory.swap.current", "cpu.max", "cpu.weight", "pids.max"):
        file = path / name
        values[name] = file.read_text().strip() if file.exists() else None
    events = values["memory.events"]
    values["memory.events"] = (
        {key: int(value) for key, value in (line.split() for line in events.splitlines())}
        if events is not None else None
    )
    memory_stat = values["memory.stat"]
    values["memory.stat"] = (
        {key: int(value) for key, value in (line.split() for line in memory_stat.splitlines())}
        if memory_stat is not None else None
    )
    result[str(path)] = values
print(json.dumps(result))
"""
    if deadline is None:
        result = _guest_run(
            runner_vm, f"sudo -n python3 -c {shlex.quote(script)} {shlex.quote(vm_id)}"
        )
    else:
        timeout = min(12.0, deadline - time.monotonic())
        assert timeout > 0, "pressure observation deadline exhausted"
        result = subprocess.run(
            ["sudo", "-n", "python3", "-c", script, vm_id],
            capture_output=True, text=True, timeout=timeout, check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
    return json.loads(result.stdout)


def _guest_saturation_sample(
    runner_vm: str, vm_name: str, deadline: float | None = None,
) -> dict[str, str]:
    started = time.monotonic()
    script = (
        "set -eu; "
        "printf 'mvm-saturation-sample-v1\\n'; "
        "printf 'boot_id='; cat /proc/sys/kernel/random/boot_id; "
        "awk '/^MemTotal:/ {print \"total_kib=\" $2} "
        "/^MemAvailable:/ {print \"available_kib=\" $2} "
        "/^Shmem:/ {print \"shmem_kib=\" $2} "
        "/^SwapTotal:/ {print \"swap_total_kib=\" $2} "
        "/^SwapFree:/ {print \"swap_free_kib=\" $2}' /proc/meminfo; "
        "stat -c 'size=%s' /mnt/mvm-saturation/fill; "
        "stat -c 'blocks=%b' /mnt/mvm-saturation/fill; "
        "printf 'kernel='; uname -s; "
        "printf 'mvm-saturation-sample-end\\n'"
    )
    # The CLI joins command arguments into shell text; quote the complete -c payload.
    result = _guest_working_set_action(runner_vm, vm_name, script, deadline)
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert time.monotonic() - started < 12, "fresh vsock sample exceeded 12 seconds"
    lines = result.stdout.splitlines()
    assert len(lines) == 11, f"unexpected saturation sample output: {result.stdout!r}"
    assert lines[0] == "mvm-saturation-sample-v1", result.stdout
    assert lines[-1] == "mvm-saturation-sample-end", result.stdout
    fields: dict[str, str] = {}
    for line in lines[1:-1]:
        key, separator, value = line.partition("=")
        assert separator and key not in fields, f"invalid or duplicate sample field: {line!r}"
        fields[key] = value
    assert set(fields) == {
        "boot_id", "total_kib", "available_kib", "shmem_kib", "swap_total_kib",
        "swap_free_kib", "size", "blocks", "kernel",
    }, fields
    assert re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", fields["boot_id"]), fields
    for key in set(fields) - {"boot_id", "kernel"}:
        assert re.fullmatch(r"[0-9]+", fields[key]), fields
    assert fields["kernel"] == "Linux", fields
    assert result.stdout.endswith("mvm-saturation-sample-end\n"), result.stdout
    return fields


def _calibrate_guest_pressure(
    runner_vm: str, vm_name: str, vm_id: str, pid: int, start_ticks: int,
    initial_boot_id: str, expected_policy: dict[str, Any],
    hierarchy_before: dict[str, Any], allocated_bytes: int, record_property: Any,
) -> tuple[int, dict[str, str]]:
    """Add at most four measured MiB only when staging has not triggered pressure."""
    started = time.monotonic()
    deadline = started + _CALIBRATION_BUDGET_SECONDS
    leaf = f"/sys/fs/cgroup/mvmctl/{vm_id}"
    initial_bytes = allocated_bytes
    baseline_high = hierarchy_before[leaf]["memory.events"]["high"]
    observations: list[dict[str, Any]] = []
    steps: list[dict[str, Any]] = []
    stop_reason = "observation_failed"

    def observe() -> dict[str, Any]:
        observation: dict[str, Any] = {
            "elapsed_seconds": time.monotonic() - started,
            "allocated_bytes": allocated_bytes,
        }
        observations.append(observation)
        guest = _guest_saturation_sample(runner_vm, vm_name, deadline)
        observation["guest"] = guest
        public_results: list[subprocess.CompletedProcess[str]] = []
        for arguments in (("vm", "inspect", vm_name, "--json"), ("vm", "ls", "--json")):
            timeout = min(12.0, deadline - time.monotonic())
            assert timeout > 0, observation
            result = subprocess.run(
                ["/usr/local/bin/mvm", *arguments], capture_output=True,
                text=True, timeout=timeout, check=False,
            )
            assert result.returncode == 0, (arguments, result.stdout, result.stderr)
            public_results.append(result)
        inspected = _json_success_data(public_results[0].stdout, dict)
        entry = next(
            (item for item in _parse_vm_list(public_results[1].stdout) if item.get("name") == vm_name),
            None,
        )
        observation["public_vm"] = {"inspected": inspected["vm"], "listed": entry}
        current_start = int(Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()[19])
        observation["process_start_time"] = current_start
        hierarchy = _pressure_hierarchy(runner_vm, vm_id, deadline)
        observation["pressure_hierarchy"] = hierarchy
        state = hierarchy[leaf]
        memory_stat = state["memory.stat"]
        observation["physical_bytes"] = int(guest["blocks"]) * 512
        observation["leaf_usage"] = {
            "anon_bytes": memory_stat.get("anon"), "file_bytes": memory_stat.get("file"),
            "kernel_bytes": memory_stat.get("kernel"), "current_bytes": int(state["memory.current"]),
            "peak_bytes": int(state["memory.peak"]), "swap_bytes": int(state["memory.swap.current"]),
            "events": state["memory.events"],
        }
        assert guest["boot_id"] == initial_boot_id and int(guest["swap_total_kib"]) == 0, observation
        assert int(guest["available_kib"]) * 1024 >= _SAFETY_FLOOR_BYTES, observation
        assert int(guest["size"]) == allocated_bytes, observation
        assert int(guest["shmem_kib"]) * 1024 >= allocated_bytes, observation
        assert allocated_bytes <= observation["physical_bytes"] <= (
            _MAX_APPEND_BYTES * _MAX_ALLOCATION_STAGES
            + _MAX_CALIBRATION_STEPS * _APPEND_QUANTUM_BYTES
        ), observation
        assert inspected["vm"]["status"] == "running" and int(inspected["vm"]["pid"]) == pid, observation
        assert entry is not None and entry["status"] == "running" and int(entry["pid"]) == pid, observation
        assert int(entry["process_start_time"]) == current_start == start_ticks, observation
        cgroup = inspected["resources"]["cgroup"]
        assert cgroup["status"] == "enforced" and cgroup.get("mismatches", []) == [], observation
        assert cgroup["requested"] == cgroup["actual"] == expected_policy, observation
        for control in ("memory.high", "memory.max", "memory.swap.max", "cpu.max", "cpu.weight", "pids.max"):
            assert state[control] == hierarchy_before[leaf][control], observation
        assert 0 <= observation["leaf_usage"]["swap_bytes"] <= expected_policy["swap_max_bytes"], observation
        for path, initial in hierarchy_before.items():
            current = hierarchy[path]
            if path != leaf:
                assert current["memory.max"] == initial["memory.max"], observation
                assert current["memory.high"] == initial["memory.high"], observation
            if initial["memory.events"] is not None:
                assert current["memory.events"] is not None, observation
                for counter in ("oom", "oom_kill"):
                    assert current["memory.events"][counter] == initial["memory.events"][counter], observation
        assert time.monotonic() <= deadline, observation
        return observation

    try:
        fresh = observe()
        for index in range(_MAX_CALIBRATION_STEPS):
            if fresh["leaf_usage"]["events"]["high"] > baseline_high:
                break
            stop_reason = "reserve_floor"
            assert int(fresh["guest"]["available_kib"]) * 1024 >= _CALIBRATION_MIN_AVAILABLE_BYTES, fresh
            step: dict[str, Any] = {
                "index": index, "append_bytes": _APPEND_QUANTUM_BYTES,
                "guest_before": fresh["guest"], "sweeps": [],
            }
            steps.append(step)
            stop_reason = "allocation_failed"
            allocation = _guest_working_set_action(
                runner_vm, vm_name,
                "set -eu; available_kib=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo); "
                "test \"$available_kib\" -ge 10240; "
                "exec dd if=/dev/zero bs=1M count=1 conv=fsync >> /mnt/mvm-saturation/fill",
                deadline,
            )
            step["allocation"] = {
                "returncode": allocation.returncode, "stdout": allocation.stdout, "stderr": allocation.stderr,
            }
            assert allocation.returncode == 0, step
            allocated_bytes += _APPEND_QUANTUM_BYTES
            stop_reason = "observation_failed"
            after = observe()
            step["after_allocation"] = after
            physical_append = after["physical_bytes"] - fresh["physical_bytes"]
            assert _APPEND_QUANTUM_BYTES <= physical_append <= (
                _MAX_CALIBRATION_STEPS * _APPEND_QUANTUM_BYTES
            ), step
            fresh = after
            for sweep_index in range(2):
                if fresh["leaf_usage"]["events"]["high"] > baseline_high:
                    break
                stop_reason = "sweep_failed"
                sweep = _guest_working_set_action(
                    runner_vm, vm_name,
                    "exec dd if=/mnt/mvm-saturation/fill of=/dev/null bs=1M", deadline,
                )
                sweep_result: dict[str, Any] = {
                    "index": sweep_index, "returncode": sweep.returncode,
                    "stdout": sweep.stdout, "stderr": sweep.stderr,
                }
                step["sweeps"].append(sweep_result)
                assert sweep.returncode == 0, step
                stop_reason = "observation_failed"
                fresh = observe()
                sweep_result["observation"] = fresh
            if fresh["leaf_usage"]["events"]["high"] > baseline_high:
                break

        stop_reason = "calibration_budget_exhausted"
        assert fresh["leaf_usage"]["events"]["high"] > baseline_high, (
            f"staging plus at most four MiB did not trigger fresh memory.high events: {observations!r}"
        )
        assert fresh["leaf_usage"]["peak_bytes"] > expected_policy["memory_high_bytes"], fresh
        assert 0 <= allocated_bytes - initial_bytes <= _MAX_CALIBRATION_STEPS * _APPEND_QUANTUM_BYTES
        settled = fresh["guest"]
        total_bytes, available_bytes = int(settled["total_kib"]) * 1024, int(settled["available_kib"]) * 1024
        assert (total_bytes - available_bytes) * 5 >= total_bytes * 4, fresh
        assert time.monotonic() <= deadline, observations
        stop_reason = "fresh_pressure"
        return allocated_bytes, settled
    finally:
        record_property("pressure_calibration", json.dumps({
            "elapsed_seconds": time.monotonic() - started, "stop_reason": stop_reason,
            "budget_seconds": _CALIBRATION_BUDGET_SECONDS, "steps_max": _MAX_CALIBRATION_STEPS,
            "append_quantum_bytes": _APPEND_QUANTUM_BYTES,
            "minimum_available_before_append_bytes": _CALIBRATION_MIN_AVAILABLE_BYTES,
            "safety_floor_bytes": _SAFETY_FLOOR_BYTES, "guest_action_deadline_seconds": _GUEST_ACTION_SECONDS,
            "initial_allocated_bytes": initial_bytes, "allocated_bytes": allocated_bytes,
            "extra_allocated_bytes": allocated_bytes - initial_bytes,
            "steps": steps, "observations": observations,
        }, sort_keys=True))


class TestVMCgroupEnvelope:
    def test_guest_sustains_near_saturation_under_pressure_only_memory_control(
        self,
        runner_vm: str,
        saturation_vm: dict[str, Any],
        saturation_host_swap: _HostSwap,
        record_property: Any,
    ) -> None:
        """Hold a real working set at >=80% usable-RAM occupancy for 15 seconds."""
        vm_name, vm_id = saturation_vm["name"], saturation_vm["id"]
        configured_bytes = 224 * 1024 * 1024
        leaf = f"/sys/fs/cgroup/mvmctl/{vm_id}"
        expected_policy = {
            "policy_version": 2,
            "cpu_quota_micros": 100000,
            "cpu_period_micros": 100000,
            "cpu_weight": 100,
            "memory_high_bytes": configured_bytes,
            "memory_max_bytes": 0,
            "memory_max_unlimited": True,
            "swap_max_bytes": saturation_host_swap.leaf_allowance_bytes,
            "pids_max": 256,
        }
        before = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
            dict,
        )
        assert before["vm"]["status"] == "running"
        initial_cgroup = before["resources"]["cgroup"]
        assert initial_cgroup["status"] == "enforced" and initial_cgroup.get("mismatches", []) == []
        assert initial_cgroup["requested"] == initial_cgroup["actual"] == expected_policy
        pid = int(before["vm"]["pid"])
        start_ticks = int(Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()[19])
        initial_entry = _vm_entry(runner_vm, vm_name)
        assert initial_entry is not None and initial_entry["status"] == "running"
        assert int(initial_entry["pid"]) == pid
        assert int(initial_entry["process_start_time"]) == start_ticks
        initial_boot_id = _run_mvm(
            runner_vm, "exec", vm_name, "--", "cat", "/proc/sys/kernel/random/boot_id",
        ).stdout.strip()
        assert re.fullmatch(r"[0-9a-f-]{36}", initial_boot_id)
        hierarchy_before = _pressure_hierarchy(runner_vm, vm_id)
        assert hierarchy_before[leaf]["memory.max"] == "max"
        assert hierarchy_before[leaf]["memory.high"] == str(configured_bytes)
        assert hierarchy_before[leaf]["memory.swap.max"] == str(saturation_host_swap.leaf_allowance_bytes)
        assert hierarchy_before[leaf]["cpu.max"] == "100000 100000"
        assert hierarchy_before[leaf]["cpu.weight"] == "100" and hierarchy_before[leaf]["pids.max"] == "256"
        initial_peak = hierarchy_before[leaf]["memory.peak"]
        assert isinstance(initial_peak, str) and re.fullmatch(r"[0-9]+", initial_peak), hierarchy_before
        initial_peak_bytes = int(initial_peak)
        assert initial_peak_bytes > 0, hierarchy_before
        initial_leaf_events = hierarchy_before[leaf]["memory.events"]
        assert initial_leaf_events is not None
        assert hierarchy_before["/sys/fs/cgroup/mvmctl"]["memory.events"] is not None
        record_property("pressure_hierarchy_before", json.dumps(hierarchy_before, sort_keys=True))
        record_property("configured_guest_memory_bytes", configured_bytes)
        record_property(
            "pressure_observation_scope",
            "Visible leaf and controller-bearing ancestors only; unavailable root controls "
            "and enclosing outer-controller limits are not measurable from the runner. "
            "This does not diagnose the reported incident or exclude global host OOM.",
        )

        allocated_bytes, settled_sample = _prepare_guest_working_set(
            runner_vm, vm_name, initial_boot_id, record_property,
        )
        assert settled_sample["boot_id"] == initial_boot_id, settled_sample
        allocated_bytes, settled_sample = _calibrate_guest_pressure(
            runner_vm, vm_name, vm_id, pid, start_ticks, initial_boot_id,
            expected_policy, hierarchy_before, allocated_bytes, record_property,
        )

        hold_started = time.monotonic()
        hold_deadline = hold_started + 60
        samples: list[dict[str, Any]] = []
        for index in range(4):
            time.sleep(max(0, hold_started + index * 5 - time.monotonic()))
            for _ in range(2):
                sweep = _guest_working_set_action(
                    runner_vm, vm_name,
                    "exec dd if=/mnt/mvm-saturation/fill of=/dev/null bs=1M", hold_deadline,
                )
                assert sweep.returncode == 0, (sweep.stdout, sweep.stderr)
            guest = _guest_saturation_sample(runner_vm, vm_name, hold_deadline)
            record_property(f"hold_guest_{index}", json.dumps(guest, sort_keys=True))
            total_bytes = int(guest["total_kib"]) * 1024
            available_bytes = int(guest["available_kib"]) * 1024
            assert guest["boot_id"] == initial_boot_id and guest["kernel"] == "Linux", guest
            assert available_bytes >= _SAFETY_FLOOR_BYTES, guest
            assert 0 < total_bytes <= configured_bytes, guest
            assert (total_bytes - available_bytes) * 5 >= total_bytes * 4, guest
            assert int(guest["size"]) == allocated_bytes, guest
            assert int(guest["shmem_kib"]) * 1024 >= allocated_bytes, guest
            assert allocated_bytes <= int(guest["blocks"]) * 512 <= (
                _MAX_APPEND_BYTES * _MAX_ALLOCATION_STAGES
                + _MAX_CALIBRATION_STEPS * _APPEND_QUANTUM_BYTES
            ), guest
            assert int(guest["swap_total_kib"]) == 0, guest

            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert inspected["vm"]["status"] == "running"
            assert int(inspected["vm"]["pid"]) == pid
            current_start = int(
                Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()[19]
            )
            assert current_start == start_ticks
            current_entry = _vm_entry(runner_vm, vm_name)
            assert current_entry is not None and current_entry["status"] == "running"
            assert int(current_entry["pid"]) == pid
            assert int(current_entry["process_start_time"]) == current_start
            cgroup = inspected["resources"]["cgroup"]
            assert cgroup["status"] == "enforced" and cgroup.get("mismatches", []) == []
            assert cgroup["requested"] == cgroup["actual"] == expected_policy
            hierarchy = _pressure_hierarchy(runner_vm, vm_id)
            diagnostics = f"before={hierarchy_before!r}, sample={hierarchy!r}, guest={guest!r}"
            record_property(f"hold_pressure_hierarchy_{index}", json.dumps(hierarchy, sort_keys=True))
            resident_bytes = int(hierarchy[leaf]["memory.current"])
            swap_bytes = int(hierarchy[leaf]["memory.swap.current"])
            assert 0 <= swap_bytes <= saturation_host_swap.leaf_allowance_bytes, diagnostics
            peak = hierarchy[leaf]["memory.peak"]
            assert isinstance(peak, str) and re.fullmatch(r"[0-9]+", peak), diagnostics
            peak_bytes = int(peak)
            # Successful memory.high reclaim can reduce current usage below the threshold.
            assert peak_bytes > configured_bytes and peak_bytes >= initial_peak_bytes, diagnostics
            assert hierarchy[leaf]["memory.events"]["high"] > initial_leaf_events["high"], diagnostics
            assert hierarchy[leaf]["memory.high"] == str(configured_bytes), diagnostics
            assert hierarchy[leaf]["memory.max"] == "max", diagnostics
            assert hierarchy[leaf]["memory.swap.max"] == str(saturation_host_swap.leaf_allowance_bytes)
            assert hierarchy[leaf]["cpu.max"] == "100000 100000"
            assert hierarchy[leaf]["cpu.weight"] == "100" and hierarchy[leaf]["pids.max"] == "256"
            for path, initial in hierarchy_before.items():
                current = hierarchy[path]
                if path != leaf:
                    assert current["memory.max"] == initial["memory.max"], diagnostics
                    assert current["memory.high"] == initial["memory.high"], diagnostics
                if initial["memory.events"] is not None:
                    assert current["memory.events"] is not None, diagnostics
                    for counter in ("oom", "oom_kill"):
                        assert current["memory.events"][counter] == initial["memory.events"][counter], diagnostics
            if saturation_host_swap.path is None:
                assert not _host_swap_paths()
                assert cgroup["usage"]["swap_bytes"] == 0
                assert swap_bytes == 0, diagnostics
            else:
                assert saturation_host_swap.path in _host_swap_paths()
                assert 0 <= cgroup["usage"]["swap_bytes"] <= saturation_host_swap.leaf_allowance_bytes
            samples.append({
                "elapsed_seconds": time.monotonic() - hold_started,
                "guest": guest,
                "used_fraction_of_usable_memory": (total_bytes - available_bytes) / total_bytes,
                "used_fraction_of_configured_memory": (total_bytes - available_bytes) / configured_bytes,
                "vmm_resident_bytes": resident_bytes,
                "vmm_swap_bytes": swap_bytes,
                "vmm_resident_and_swap_bytes": resident_bytes + swap_bytes,
                "vmm_peak_bytes": peak_bytes,
                "vmm_memory_stat": hierarchy[leaf]["memory.stat"],
                "vmm_pid": pid,
                "vmm_process_start_time": current_start,
                "pressure_hierarchy": hierarchy,
            })

        assert 15 <= time.monotonic() - hold_started <= 60
        record_property("sustained_saturation_samples", json.dumps(samples, sort_keys=True))

    def test_operator_inspects_protected_pressure_only_envelope(
        self,
        runner_vm: str,
        cgroup_config_overrides: None,
    ) -> None:
        vm_name = f"sys-cgroup-{uuid.uuid4().hex[:8]}"
        _require_preseeded_vm_assets(runner_vm)
        try:
            created = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "vm",
                    "create",
                    vm_name,
                    "--no-network",
                    "--cloud-init-mode",
                    "off",
                    "--skip-deblob",
                    "--no-pci",
                    "--disk-size",
                    "512M",
                    "--vcpu",
                    "2",
                    "--mem",
                    "512M",
                    "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            assert len(created) == 1
            vm = _vm_entry(runner_vm, vm_name)
            assert vm is not None and vm["status"] == "running"
            pid = int(vm["pid"])
            relative = f"/mvmctl/{vm['id']}"
            leaf = Path(f"/sys/fs/cgroup{relative}")

            membership = Path(f"/proc/{pid}/cgroup").read_text().splitlines()
            assert f"0::{relative}" in membership
            leaf_stat = leaf.stat()
            assert leaf_stat.st_uid == 0
            assert leaf_stat.st_mode & 0o777 == 0o700
            with pytest.raises(PermissionError):
                (leaf / "cpu.max").read_text()

            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            cgroup = inspected["resources"]["cgroup"]
            requested = cgroup["requested"]
            actual = cgroup["actual"]
            usage = cgroup["usage"]
            assert cgroup["path"] == str(leaf)
            assert cgroup["status"] == "enforced"
            assert cgroup.get("mismatches", []) == []
            assert requested == actual
            assert requested == {
                "policy_version": 2,
                "cpu_quota_micros": 200000,
                "cpu_period_micros": 100000,
                "cpu_weight": 100,
                "memory_high_bytes": 640 * 1024 * 1024,
                "memory_max_bytes": 0,
                "memory_max_unlimited": True,
                "swap_max_bytes": 0,
                "pids_max": 256,
            }

            assert usage["cpu_usage_micros"] >= 0
            assert usage["memory_bytes"] > 0
            assert usage["swap_bytes"] >= 0
            assert usage["pids_current"] >= 1

            _run_mvm(runner_vm, "vm", "stop", vm_name, timeout=120)
            assert not leaf.exists()
            _run_mvm(runner_vm, "vm", "start", vm_name, timeout=120)
            restarted = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert restarted["vm"]["status"] == "running"
            assert restarted["resources"]["cgroup"]["status"] == "enforced"
            assert leaf.is_dir()
        finally:
            remaining = _vm_entry(runner_vm, vm_name)
            if remaining is not None:
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
                assert not Path(f"/sys/fs/cgroup/mvmctl/{remaining['id']}").exists()
                assert _vm_entry(runner_vm, vm_name) is None
