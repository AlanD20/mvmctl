"""Typed cgroup-v2 VM resource envelope system specification — Tier 2."""

from __future__ import annotations

import re
import shlex
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
    entries = _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout
    )
    return next((entry for entry in entries if entry.get("name") == vm_name), None)


def _memory_events(runner_vm: str, vm_id: str) -> dict[str, int]:
    """Read kernel pressure/OOM counters for the test VM's protected leaf."""
    path = f"/sys/fs/cgroup/mvmctl/{vm_id}/memory.events"
    result = _guest_run(runner_vm, f"sudo -n cat {shlex.quote(path)}")
    events = dict(line.split() for line in result.stdout.splitlines())
    assert "high" in events and "oom_kill" in events, events
    return {key: int(value) for key, value in events.items()}


class TestVMCgroupEnvelope:
    def test_operator_inspects_protected_pressure_only_envelope(
        self,
        runner_vm: str,
        cgroup_config_overrides: None,
    ) -> None:
        vm_name = f"sys-cgroup-{uuid.uuid4().hex[:8]}"
        ensure_vm_deps(runner_vm)
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

    def test_guest_stays_responsive_under_pressure_only_memory_control(
        self,
        runner_vm: str,
        cgroup_config_overrides: None,
    ) -> None:
        """A charged guest allocation must trigger pressure without killing the VMM."""
        vm_name = f"sys-pressure-{uuid.uuid4().hex[:8]}"
        ensure_vm_deps(runner_vm)
        _run_mvm(runner_vm, "config", "set", "defaults.vm", "cgroup_vmm_headroom_mib", "0")
        try:
            created = _json_success_data(
                _run_mvm(
                    runner_vm, "vm", "create", vm_name,
                    "--no-network", "--cloud-init-mode", "off", "--skip-deblob", "--no-pci",
                    "--disk-size", "512M", "--vcpu", "1", "--mem", "256M", "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            assert len(created) == 1
            before = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )["resources"]["cgroup"]
            assert before["status"] == "enforced"
            assert before["actual"]["memory_high_bytes"] == 256 * 1024 * 1024
            assert before["actual"]["memory_max_unlimited"] is True
            initial_bytes = before["usage"]["memory_bytes"]
            initial_events = _memory_events(runner_vm, created[0]["id"])

            _run_mvm(runner_vm, "exec", vm_name, "--", "mkdir", "-p", "/mnt/mvm-pressure")
            _run_mvm(
                runner_vm, "exec", vm_name, "--", "mount", "-t", "tmpfs", "-o", "size=180M",
                "tmpfs", "/mnt/mvm-pressure",
            )
            _run_mvm(
                runner_vm, "exec", vm_name, "--", "dd", "if=/dev/zero",
                "of=/mnt/mvm-pressure/fill", "bs=1M", "count=150", "conv=fsync",
            )

            after = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert after["vm"]["status"] == "running"
            assert after["resources"]["cgroup"]["status"] == "enforced"
            assert after["resources"]["cgroup"]["usage"]["memory_bytes"] >= initial_bytes + 96 * 1024 * 1024
            final_events = _memory_events(runner_vm, created[0]["id"])
            assert final_events["high"] > initial_events["high"], (
                f"guest allocation did not trigger cgroup pressure: {initial_events} -> {final_events}"
            )
            assert final_events["oom_kill"] == initial_events["oom_kill"]
            assert _run_mvm(runner_vm, "exec", vm_name, "--", "uname", "-s").stdout.strip() == "Linux"
        finally:
            try:
                if _vm_entry(runner_vm, vm_name) is not None:
                    _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
                    assert _vm_entry(runner_vm, vm_name) is None
            finally:
                # The fixture then restores the caller's exact pre-test override state.
                _run_mvm(runner_vm, "config", "set", "defaults.vm", "cgroup_vmm_headroom_mib", "128")
