"""Canonical Firecracker Jailer launcher system tests — Tier 2 runner VM."""

from __future__ import annotations

import json
import shlex
import socket
import time
import uuid
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _parse_vm_list,
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
    vms = _parse_vm_list(result.stdout)
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
        """Prove the VM process is chrooted under an isolated non-root identity."""
        vm = _vm_entry(runner_vm, jailed_vm["name"])
        pid = int(vm["pid"])
        invoking_uid = int(_guest_run(runner_vm, "id -u").stdout.strip())

        status = _guest_run(runner_vm, f"cat /proc/{pid}/status").stdout
        uid_line = next(line for line in status.splitlines() if line.startswith("Uid:"))
        process_uid = int(uid_line.split()[1])
        assert process_uid != invoking_uid
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
            source_inspect = json.loads(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout
            )
            source_cgroup = source_inspect["resources"]["cgroup"]
            assert source_cgroup["status"] == "enforced"
            source_limits = source_cgroup["requested"]

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
            restored_inspect = json.loads(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout
            )
            restored_cgroup = restored_inspect["resources"]["cgroup"]
            assert restored_cgroup["status"] == "enforced"
            assert restored_cgroup["requested"] == source_limits
            assert restored_cgroup["actual"] == source_limits
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


@pytest.fixture
def networkless_vm_name(runner_vm: str) -> Generator[str, None, None]:
    """Give each mutating networkless journey its own VM and checked teardown."""
    name = f"sys-networkless-{uuid.uuid4().hex[:8]}"
    try:
        yield name
    finally:
        listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        if any(vm["name"] == name for vm in listed):
            removed = _run_mvm(runner_vm, "vm", "rm", name, "--force", check=False, timeout=120)
            assert removed.returncode == 0, (removed.stdout, removed.stderr)
        listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        assert all(vm["name"] != name for vm in listed), f"VM {name} survived checked teardown"


def _networkless_guest_interfaces(runner_vm: str, name: str) -> set[str]:
    """Wait for the guest agent and read the guest's kernel interface list."""
    deadline = time.monotonic() + 20
    last_error = ""
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm, "exec", name, "--", "cat", "/proc/net/dev", check=False, timeout=15,
        )
        if result.returncode == 0:
            return {
                line.split(":", 1)[0].strip()
                for line in result.stdout.splitlines()
                if ":" in line
            }
        last_error = result.stderr
        time.sleep(0.5)
    raise AssertionError(f"vsock agent did not return guest interfaces: {last_error}")


class TestNetworklessJailerLifecycle:
    def test_ps_and_inspect_follow_stop_and_start(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The public process list and inspection follow the VM lifecycle."""
        ensure_vm_deps(runner_vm)
        name = networkless_vm_name
        _run_mvm(
            runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            timeout=180,
        )
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert any(vm["name"] == name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "running"

        _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert all(vm["name"] != name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "stopped"

        _run_mvm(runner_vm, "vm", "start", name, timeout=120)
        running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
        assert any(vm["name"] == name for vm in running)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
        )
        assert inspected["vm"]["status"] == "running"

    def test_operator_cannot_connect_to_firecracker_api(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The public VM lifecycle works without exposing direct Firecracker control."""
        ensure_vm_deps(runner_vm)
        name = networkless_vm_name
        _run_mvm(
            runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
            "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
            timeout=180,
        )
        vm = _vm_entry(runner_vm, name)
        assert vm["status"] == "running"
        assert vm["api_socket_path"]
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            assert client.connect_ex(vm["api_socket_path"]) != 0
        _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
        assert _vm_entry(runner_vm, name)["status"] == "stopped"

    def test_firecracker_uses_isolated_execution_uid(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The guest VMM must not inherit either root or the operator's UID."""
        ensure_vm_deps(runner_vm)
        name = networkless_vm_name
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        vm = _vm_entry(runner_vm, name)
        assert vm["status"] == "running"
        status = _guest_run(runner_vm, f"cat /proc/{int(vm['pid'])}/status").stdout
        process_uid = int(next(line for line in status.splitlines() if line.startswith("Uid:")).split()[1])
        operator_uid = int(_guest_run(runner_vm, "id -u").stdout.strip())
        assert operator_uid != 0
        assert process_uid not in (0, operator_uid)

    def test_raw_volume_is_attached_at_launch_and_after_restart(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """A raw data volume reaches the guest through the protected Jailer launch."""
        ensure_vm_deps(runner_vm)
        name = networkless_vm_name
        volume_name = f"sys-networkless-vol-{uuid.uuid4().hex[:8]}"
        try:
            created_volume = _json_success_data(
                _run_mvm(
                    runner_vm, "volume", "create", volume_name, "16M", "--json",
                ).stdout,
                dict,
            )
            assert created_volume["format"] == "raw"

            created_vm = _json_success_data(
                _run_mvm(
                    runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                    "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M",
                    "--vcpu", "1", "--volume", volume_name, "--json", timeout=180,
                ).stdout,
                list,
            )
            assert len(created_vm) == 1
            assert created_vm[0]["status"] == "running"

            attached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", volume_name, "--json").stdout,
                dict,
            )
            assert attached["volume"]["status"] == "attached"
            assert attached["attachment"]["vm_id"] == created_vm[0]["id"]

            for attempt in range(2):
                partitions = _run_mvm(
                    runner_vm, "exec", name, "--", "cat", "/proc/partitions", timeout=30,
                )
                assert "vdb" in partitions.stdout
                if attempt == 0:
                    _run_mvm(runner_vm, "vm", "stop", name, timeout=120)
                    _run_mvm(runner_vm, "vm", "start", name, timeout=120)
        finally:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(vm["name"] == name for vm in listed):
                removed = _run_mvm(runner_vm, "vm", "rm", name, "--force", check=False, timeout=120)
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            if any(volume["name"] == volume_name for volume in volumes):
                removed = _run_mvm(
                    runner_vm, "volume", "rm", volume_name, "--force", check=False,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            assert all(volume["name"] != volume_name for volume in volumes)

    def test_networkless_snapshot_restores_independent_data_volume(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """A full capture restores the original volume bytes without an IP endpoint."""
        ensure_vm_deps(runner_vm)
        source_name = networkless_vm_name
        restored_name = f"{source_name}-restore"
        volume_name = f"sys-snapshot-vol-{uuid.uuid4().hex[:8]}"
        snapshot_name = f"sys-snapshot-{uuid.uuid4().hex[:8]}"
        restored_volume_ids: set[str] = set()
        try:
            _run_mvm(runner_vm, "volume", "create", volume_name, "16M", "--json")
            _run_mvm(
                runner_vm, "vm", "create", source_name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--volume", volume_name, "--json", timeout=180,
            )
            _run_mvm(
                runner_vm, "exec", source_name, "--", "dd", "if=/dev/urandom", "of=/dev/vdb",
                "bs=4096", "count=1", "conv=fsync", timeout=30,
            )
            captured_hash = _run_mvm(
                runner_vm, "exec", source_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]

            captured = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "create", source_name, "--name", snapshot_name,
                    "--json", timeout=180,
                ).stdout,
                dict,
            )
            assert captured["memory_file"] and captured["state_file"]

            _run_mvm(
                runner_vm, "exec", source_name, "--", "dd", "if=/dev/zero", "of=/dev/vdb",
                "bs=4096", "count=1", "conv=fsync", timeout=30,
            )
            changed_hash = _run_mvm(
                runner_vm, "exec", source_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]
            assert changed_hash != captured_hash

            _run_mvm(runner_vm, "vm", "rm", source_name, "--force", timeout=120)
            _run_mvm(runner_vm, "volume", "rm", volume_name, "--force")
            denied = _run_mvm(
                runner_vm, "snapshot", "restore", captured["id"], restored_name,
                "--network", "not-a-network", "--json", check=False,
            )
            assert denied.returncode != 0
            assert json.loads(denied.stdout)["status"] == "error"
            assert all(vm["name"] != restored_name for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))
            restored = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "restore", captured["id"], restored_name,
                    "--resume", "--json", timeout=180,
                ).stdout,
                list,
            )
            assert len(restored) == 1
            assert restored[0]["network_placement"] == "networkless"
            assert not restored[0]["network_id"]
            assert not restored[0]["ipv4"]
            assert not restored[0]["mac"]
            restored_hash = _run_mvm(
                runner_vm, "exec", restored_name, "--", "sha256sum", "/dev/vdb", timeout=30,
            ).stdout.split()[0]
            assert restored_hash == captured_hash
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            restored_volumes = [volume for volume in volumes if volume.get("vm_id") == restored[0]["id"]]
            assert len(restored_volumes) == 1
            restored_volume_ids = {restored_volumes[0]["id"]}
        finally:
            for vm_name in (restored_name, source_name):
                listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                if any(vm["name"] == vm_name for vm in listed):
                    removed = _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=120)
                    assert removed.returncode == 0, (removed.stdout, removed.stderr)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            for volume in volumes:
                if volume["name"] == volume_name or volume["id"] in restored_volume_ids:
                    removed = _run_mvm(
                        runner_vm, "volume", "rm", volume["id"], "--force", check=False,
                    )
                    assert removed.returncode == 0, (removed.stdout, removed.stderr)
            snapshots = _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            )
            for snapshot in snapshots:
                if snapshot["name"] == snapshot_name:
                    removed = _run_mvm(
                        runner_vm, "snapshot", "rm", snapshot["id"], "--force", check=False,
                    )
                    assert removed.returncode == 0, (removed.stdout, removed.stderr)
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert all(vm["name"] not in (source_name, restored_name) for vm in listed)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
            )
            assert all(volume["name"] != volume_name and volume["id"] not in restored_volume_ids
                       for volume in volumes)
            snapshots = _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            )
            assert all(snapshot["name"] != snapshot_name for snapshot in snapshots)

    def test_create_restart_reboot_and_vsock_exec_without_guest_nic(
        self, runner_vm: str, networkless_vm_name: str,
    ) -> None:
        """The installed candidate can restart a loopback-only VM without a network record."""
        ensure_vm_deps(runner_vm)
        name = networkless_vm_name
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--no-network", "--cloud-init-mode", "off",
                "--skip-deblob", "--no-pci", "-s", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1, created
        original = created[0]
        assert original["name"] == name
        assert original["status"] == "running"
        assert original["network_placement"] == "networkless"
        assert (original["network_id"], original["ipv4"], original["mac"], original["tap_device"]) == (
            "", "", "", "",
        )
        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces

        _run_mvm(runner_vm, "vm", "stop", name)
        stopped = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                       if vm["name"] == name)
        assert stopped["status"] == "stopped"

        _run_mvm(runner_vm, "vm", "start", name)
        started = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                       if vm["name"] == name)
        assert started["status"] == "running"
        assert started["process_start_time"] != original["process_start_time"]
        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces

        _run_mvm(runner_vm, "vm", "reboot", name, timeout=120)
        rebooted = next(vm for vm in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
                        if vm["name"] == name)
        assert rebooted["status"] == "running"
        assert rebooted["process_start_time"] != started["process_start_time"]
        interfaces = _networkless_guest_interfaces(runner_vm, name)
        assert "lo" in interfaces
        assert "eth0" not in interfaces
