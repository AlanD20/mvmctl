"""Typed cgroup-v2 VM resource envelope system specification — Tier 2."""

from __future__ import annotations

import json
import re
import shlex
import uuid
from dataclasses import dataclass
from typing import Any, Iterator

import pytest

from tests.system.conftest import (
    _guest_run,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
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
    entries: list[dict[str, Any]] = json.loads(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout
    )
    return next((entry for entry in entries if entry.get("name") == vm_name), None)


def _read(runner_vm: str, path: str) -> str:
    return _guest_run(runner_vm, f"cat {shlex.quote(path)}").stdout.strip()


class TestVMCgroupEnvelope:
    def test_running_vm_envelope_and_lifecycle_cleanup(
        self,
        runner_vm: str,
        cgroup_config_overrides: None,
    ) -> None:
        vm_name = f"sys-cgroup-{uuid.uuid4().hex[:8]}"
        net_name = f"sys-cgroup-net-{uuid.uuid4().hex[:6]}"
        vm_id = ""
        ensure_vm_deps(runner_vm)
        controller_probe = _guest_run(
            runner_vm, "cat /sys/fs/cgroup/cgroup.controllers", check=False
        )
        controllers = (
            set(controller_probe.stdout.split())
            if controller_probe.returncode == 0
            else set()
        )
        missing = {"cpu", "memory", "pids"} - controllers
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            created = _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--vcpu",
                "2",
                "--mem",
                "512",
                check=False,
                timeout=120,
            )
            if missing:
                assert created.returncode != 0
                combined = (created.stdout + created.stderr).lower()
                assert "cgroup" in combined
                assert "controller" in combined or "v2" in combined or "unified" in combined
                assert _vm_entry(runner_vm, vm_name) is None
                return

            assert created.returncode == 0, created.stderr
            vm = _vm_entry(runner_vm, vm_name)
            assert vm is not None and vm["status"] == "running"
            vm_id = vm["id"]
            pid = int(vm["pid"])
            relative = f"/mvmctl/{vm_id}"
            leaf = f"/sys/fs/cgroup{relative}"

            membership = _read(runner_vm, f"/proc/{pid}/cgroup").splitlines()
            assert f"0::{relative}" in membership
            assert str(pid) in _read(runner_vm, f"{leaf}/cgroup.procs").split()

            inspected = json.loads(
                _run_mvm(
                    runner_vm, "vm", "inspect", vm_name, "--json"
                ).stdout
            )
            cgroup = inspected["resources"]["cgroup"]
            requested = cgroup["requested"]
            actual = cgroup["actual"]
            assert cgroup["path"] == leaf
            assert cgroup["status"] == "enforced"
            assert cgroup.get("mismatches", []) == []
            assert requested == actual
            assert requested == {
                "policy_version": 1,
                "cpu_quota_micros": 200000,
                "cpu_period_micros": 100000,
                "cpu_weight": 100,
                "memory_high_bytes": 640 * 1024 * 1024,
                "memory_max_bytes": 640 * 1024 * 1024,
                "swap_max_bytes": 0,
                "pids_max": 256,
            }

            assert _read(runner_vm, f"{leaf}/cpu.max") == "200000 100000"
            assert _read(runner_vm, f"{leaf}/cpu.weight") == "100"
            assert _read(runner_vm, f"{leaf}/memory.high") == str(640 * 1024 * 1024)
            assert _read(runner_vm, f"{leaf}/memory.max") == str(640 * 1024 * 1024)
            assert _read(runner_vm, f"{leaf}/memory.swap.max") == "0"
            assert _read(runner_vm, f"{leaf}/pids.max") == "256"
            for key in (
                "cpu_quota_micros",
                "cpu_period_micros",
                "cpu_weight",
                "memory_high_bytes",
                "memory_max_bytes",
                "pids_max",
            ):
                assert int(actual[key]) > 0
            assert actual["swap_max_bytes"] == 0

            _run_mvm(runner_vm, "vm", "stop", vm_name, timeout=120)
            assert _guest_run(
                runner_vm, f"test ! -e {shlex.quote(leaf)}", check=False
            ).returncode == 0
            _run_mvm(runner_vm, "vm", "start", vm_name, timeout=120)
            assert _guest_run(
                runner_vm, f"test -d {shlex.quote(leaf)}", check=False
            ).returncode == 0
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            assert _guest_run(
                runner_vm, f"test ! -e {shlex.quote(leaf)}", check=False
            ).returncode == 0
            assert _vm_entry(runner_vm, vm_name) is None
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=120
            )
            if vm_id:
                leaf = f"/sys/fs/cgroup/mvmctl/{vm_id}"
                assert _guest_run(
                    runner_vm, f"test ! -e {shlex.quote(leaf)}", check=False
                ).returncode == 0
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
