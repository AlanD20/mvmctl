"""Typed cgroup-v2 VM resource envelope system specification — Tier 2."""

from __future__ import annotations

import json
import shlex
import uuid
from typing import Any

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


def _vm_entry(runner_vm: str, vm_name: str) -> dict[str, Any] | None:
    entries: list[dict[str, Any]] = json.loads(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout
    )
    return next((entry for entry in entries if entry.get("name") == vm_name), None)


def _read(runner_vm: str, path: str) -> str:
    return _guest_run(runner_vm, f"cat {shlex.quote(path)}").stdout.strip()


class TestVMCgroupEnvelope:
    def test_running_vm_envelope_and_lifecycle_cleanup(self, runner_vm: str) -> None:
        vm_name = f"sys-cgroup-{uuid.uuid4().hex[:8]}"
        net_name = f"sys-cgroup-net-{uuid.uuid4().hex[:6]}"
        config_keys = {
            "cgroup_vmm_headroom_mib": "128",
            "cgroup_cpu_weight": "100",
            "cgroup_pids_max": "256",
            "cgroup_swap_max_bytes": "0",
        }
        originals: dict[str, str | None] = {}
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
            for key, value in config_keys.items():
                current = _run_mvm(
                    runner_vm, "config", "get", "defaults.vm", key, check=False
                )
                originals[key] = (
                    current.stdout.strip()
                    if current.returncode == 0 and current.stdout.strip()
                    else None
                )
                _run_mvm(
                    runner_vm, "config", "set", "defaults.vm", key, value
                )

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
            for key, value in originals.items():
                if value is None:
                    _run_mvm(
                        runner_vm,
                        "config",
                        "reset",
                        "defaults.vm",
                        key,
                        check=False,
                    )
                else:
                    _run_mvm(
                        runner_vm,
                        "config",
                        "set",
                        "defaults.vm",
                        key,
                        value,
                        check=False,
                    )
