"""Canonical Firecracker Jailer launcher system tests — Tier 2 runner VM."""

from __future__ import annotations

import json
import shlex
import uuid
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _run_mvm,
    cleanup_vm_resources,
    create_vm_core,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.tier2,
    pytest.mark.needs_kvm,
    pytest.mark.slow,
]


def _vm_entry(runner_vm: str, vm_name: str) -> dict[str, Any]:
    result = _run_mvm(runner_vm, "vm", "ls", "--json")
    vms: list[dict[str, Any]] = json.loads(result.stdout)
    entry = next((vm for vm in vms if vm.get("name") == vm_name), None)
    assert entry is not None, f"VM {vm_name!r} is absent from vm ls --json"
    return entry


def _jail_root(vm_id: str) -> str:
    return f"/var/lib/mvmctl/jailer/firecracker/{vm_id}/root"


@pytest.fixture
def jailed_vm(runner_vm: str) -> Generator[dict[str, Any], None, None]:
    vm_name = f"sys-jailer-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-jailer-net-{uuid.uuid4().hex[:6]}"
    vm = create_vm_core(vm_name, net_name, extra_args=["--console"])
    vm["network_name"] = net_name
    try:
        yield vm
    finally:
        cleanup_vm_resources(vm_name, net_name)


class TestCanonicalJailerLaunch:
    def test_process_is_non_root_inside_expected_jail(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Prove the VM process is chrooted and runs as the invoking identity."""
        vm = _vm_entry(runner_vm, jailed_vm["name"])
        pid = int(vm["pid"])
        invoking_uid = int(_guest_run(runner_vm, "id -u").stdout.strip())

        status = _guest_run(runner_vm, f"cat /proc/{pid}/status").stdout
        uid_line = next(line for line in status.splitlines() if line.startswith("Uid:"))
        process_uid = int(uid_line.split()[1])
        assert process_uid == invoking_uid
        assert process_uid != 0

        process_root = _guest_run(
            runner_vm, f"readlink /proc/{pid}/root"
        ).stdout.strip()
        expected_root = _jail_root(vm["id"])
        assert process_root == expected_root
        cmdline = _guest_run(
            runner_vm, f"tr '\\0' ' ' < /proc/{pid}/cmdline"
        ).stdout
        assert "firecracker" in cmdline

        api_socket = vm["api_socket_path"]
        socket_state = _guest_run(
            runner_vm, f"test -S {shlex.quote(api_socket)} && echo socket"
        )
        assert socket_state.stdout.strip() == "socket"

    def test_trusted_pair_is_root_owned_and_not_user_writable(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Verify both exact-version executables satisfy Jailer's trust contract."""
        vm = _vm_entry(runner_vm, jailed_vm["name"])
        binaries: list[dict[str, Any]] = json.loads(
            _run_mvm(runner_vm, "bin", "ls", "--json").stdout
        )
        selected_ids = {vm["binary_id"], vm["jailer_binary_id"]}
        pair = [item for item in binaries if item.get("id") in selected_ids]
        assert {item.get("type") for item in pair} == {"firecracker", "jailer"}
        assert len({item.get("version") for item in pair}) == 1
        for item in pair:
            path = shlex.quote(item["path"])
            stat = _guest_run(runner_vm, f"stat -Lc '%u %a' {path}").stdout.strip()
            owner, mode_text = stat.split()
            mode = int(mode_text, 8)
            assert owner == "0"
            assert mode & 0o022 == 0

    def test_exec_cp_console_and_lifecycle_parity(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Exercise API socket, vsock exec/cp, console, stop/start, and reboot."""
        vm_name = jailed_vm["name"]
        marker = f"jailer-{uuid.uuid4().hex}"
        exec_result = _run_mvm(
            runner_vm, "exec", vm_name, "--", "printf", marker
        )
        assert exec_result.stdout == marker

        source = f"/tmp/{marker}.txt"
        _guest_run(runner_vm, f"printf %s {shlex.quote(marker)} > {shlex.quote(source)}")
        _run_mvm(runner_vm, "cp", source, f"{vm_name}:/tmp/jailer-marker.txt")
        copied = _run_mvm(
            runner_vm, "exec", vm_name, "--", "cat", "/tmp/jailer-marker.txt"
        )
        assert copied.stdout.strip() == marker

        console = _run_mvm(runner_vm, "console", vm_name, "--state")
        console_state = (console.stdout + console.stderr).lower()
        assert "running" in console_state or "stopped" in console_state

        before = _vm_entry(runner_vm, vm_name)
        jail = _jail_root(before["id"])
        _run_mvm(runner_vm, "vm", "stop", vm_name)
        stopped = _vm_entry(runner_vm, vm_name)
        assert stopped["status"] == "stopped"
        assert _guest_run(
            runner_vm, f"test ! -e {shlex.quote(jail)}", check=False
        ).returncode == 0

        _run_mvm(runner_vm, "vm", "start", vm_name)
        started = _vm_entry(runner_vm, vm_name)
        assert started["status"] == "running"
        assert int(started["pid"]) != int(before["pid"])
        assert _guest_run(
            runner_vm, f"test -d {shlex.quote(jail)}", check=False
        ).returncode == 0

        _run_mvm(runner_vm, "vm", "reboot", vm_name)
        rebooted = _vm_entry(runner_vm, vm_name)
        assert rebooted["status"] == "running"
        assert int(rebooted["pid"]) != int(started["pid"])

    def test_volume_and_snapshot_parity(
        self, runner_vm: str, jailed_vm: dict[str, Any]
    ) -> None:
        """Selected volume and snapshot resources remain usable without collection mounts."""
        vm_name = jailed_vm["name"]
        volume_name = f"sys-jailer-vol-{uuid.uuid4().hex[:6]}"
        snapshot_id: str | None = None
        try:
            _run_mvm(runner_vm, "volume", "create", volume_name, "64M")
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "attach", vm_name, volume_name)
            attached = json.loads(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout
            )
            assert attached["volume"]["status"] == "attached"
            _run_mvm(runner_vm, "vm", "start", vm_name)
            partitions = _run_mvm(
                runner_vm, "exec", vm_name, "--", "cat", "/proc/partitions"
            )
            assert "vdb" in partitions.stdout

            _run_mvm(runner_vm, "snapshot", "create", vm_name)
            snapshots: list[dict[str, Any]] = json.loads(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout
            )
            selected = [s for s in snapshots if s.get("source_vm_name") == vm_name]
            assert selected
            snapshot_id = selected[-1]["id"]

            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "detach", vm_name, volume_name)
            detached = json.loads(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout
            )
            assert detached["volume"]["status"] == "available"

            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")
            restored = _run_mvm(
                runner_vm, "snapshot", "restore", snapshot_id, vm_name, "--resume",
                check=False, timeout=180,
            )
            assert restored.returncode == 0, restored.stderr
            restored_vm = _vm_entry(runner_vm, vm_name)
            assert restored_vm["status"] == "running"
            assert _guest_run(
                runner_vm, f"test -d {shlex.quote(_jail_root(restored_vm['id']))}",
                check=False,
            ).returncode == 0
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", volume_name, "--force", check=False)
            if snapshot_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snapshot_id, "--force", check=False
                )


class TestJailerFailClosed:
    def test_missing_exact_jailer_does_not_fallback_to_direct_firecracker(
        self, runner_vm: str
    ) -> None:
        """An alternate-version Jailer cannot substitute for the selected exact pair."""
        ensure_vm_deps(runner_vm)
        vm_name = f"sys-jailer-fail-{uuid.uuid4().hex[:8]}"
        binaries: list[dict[str, Any]] = json.loads(
            _run_mvm(runner_vm, "bin", "ls", "--json").stdout
        )
        selected_fc = next(
            item
            for item in binaries
            if item.get("type") == "firecracker" and item.get("is_default")
        )
        selected_jailer = next(
            item
            for item in binaries
            if item.get("type") == "jailer"
            and item.get("version") == selected_fc["version"]
        )
        alternate_version = "1.14.2" if selected_fc["version"] != "1.14.2" else "1.14.1"
        alternate_preexisting = any(
            item.get("version") == alternate_version and item.get("is_present")
            for item in binaries
        )
        try:
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker", "--version",
                alternate_version, "--force", check=False, timeout=300,
            )
            alternate_bins: list[dict[str, Any]] = json.loads(
                _run_mvm(runner_vm, "bin", "ls", "--json").stdout
            )
            assert any(
                item.get("type") == "jailer"
                and item.get("version") == alternate_version
                and item.get("is_present")
                for item in alternate_bins
            )
            _run_mvm(
                runner_vm, "bin", "rm", selected_jailer["id"][:6], "--force"
            )
            result = _run_mvm(
                runner_vm, "vm", "create", vm_name, "--image", "alpine:3.23",
                check=False, timeout=180,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "jailer" in combined
            assert "missing" in combined or "pair" in combined
            listed = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert not any(vm.get("name") == vm_name for vm in listed)
            processes = _guest_run(
                runner_vm, f"pgrep -af {shlex.quote(vm_name)}", check=False
            )
            assert processes.returncode != 0
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm, "bin", "pull", "firecracker", "--version",
                selected_fc["version"], "--force", "--default", check=False,
                timeout=300,
            )
            if not alternate_preexisting:
                _run_mvm(
                    runner_vm, "bin", "rm", "--version", alternate_version,
                    "--force", check=False,
                )
