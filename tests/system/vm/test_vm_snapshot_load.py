"""VM snapshot and restore system tests."""

from __future__ import annotations

import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


@pytest.fixture(scope="class")
def snapshot_vm_deps(runner_vm) -> None:
    """Prepare the mirrored launch assets once per snapshot class."""
    ensure_vm_deps(runner_vm)


def _remove_snapshot_vm(runner_vm: str, name: str) -> None:
    """Remove only the owned VM and require its public record to disappear."""
    listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
    if any(vm["name"] == name for vm in listed):
        _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
    assert all(vm["name"] != name for vm in _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
    ))


@pytest.fixture
def snapshot_vm(runner_vm, snapshot_vm_deps) -> Generator[dict, None, None]:
    """Own one tiny VM, one capture identity, and its unique no-NAT network."""
    name = f"sys-snapshot-{uuid.uuid4().hex[:8]}"
    network = f"{name}-net"
    owned = {"name": name, "network": network, "snapshot_name": f"{name}-capture",
             "snapshot_id": None}
    try:
        _run_mvm(
            runner_vm, "network", "create", network, "--subnet", _unique_subnet(network),
            "--no-nat",
        )
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--image", "alpine:3.23",
                "--network", network, "--cloud-init-mode", "off", "--skip-deblob",
                "--no-pci", "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout, list,
        )
        assert len(created) == 1, created
        source = created[0]
        assert source["name"] == name and source["status"] == "running"
        assert (source["vcpu_count"], source["mem_size_mib"], source["disk_size_mib"]) == (1, 256, 512)
        owned["source"] = source
        owned["marker"] = uuid.uuid4().hex
        _run_mvm(
            runner_vm, "exec", name, "--",
            f"printf '%s' '{owned['marker']}' > /root/snapshot-marker && sync",
        )
        owned["boot_id"] = _snapshot_boot_id(runner_vm, name)
        yield owned
    finally:
        # Restored VM references are released before the capture and its network.
        _remove_snapshot_vm(runner_vm, name)
        if owned["snapshot_id"] is not None:
            _run_mvm(runner_vm, "snapshot", "rm", owned["snapshot_id"], "--force", timeout=120)
        assert all(snapshot["name"] != owned["snapshot_name"] for snapshot in _json_success_data(
            _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
        ))
        networks = _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        )
        if any(item["name"] == network for item in networks):
            _run_mvm(runner_vm, "network", "rm", network, "--force")
        assert all(item["name"] != network for item in _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        ))


def _snapshot_vm_state(runner_vm: str, name: str, status: str) -> dict:
    """List and inspect must agree on the exact public VM state."""
    matches = [vm for vm in _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
    ) if vm["name"] == name]
    assert len(matches) == 1, matches
    vm = matches[0]
    assert vm["status"] == status, vm
    inspected = _json_success_data(
        _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
    )["vm"]
    assert inspected["id"] == vm["id"]
    assert inspected["status"] == status, inspected
    assert inspected["pid"] == vm["pid"]
    return vm


def _snapshot_process_identity(vm: dict) -> tuple[int, int]:
    """Admit the public PID and start ticks against the current Linux task."""
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0
    fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    assert len(fields) >= 20 and int(fields[19]) == ticks, (vm, fields)
    assert fields[0] not in ("Z", "X"), fields
    return pid, ticks


def _snapshot_boot_id(runner_vm: str, name: str) -> str:
    boot_id = _run_mvm(
        runner_vm, "exec", name, "--", "cat", "/proc/sys/kernel/random/boot_id",
    ).stdout.strip()
    assert str(uuid.UUID(boot_id)) == boot_id
    return boot_id


def _capture_full_snapshot(runner_vm: str, owned: dict, *, pause: bool = False) -> dict:
    """Retain the public request ID before asserting successful full publication."""
    result = _run_mvm(
        runner_vm, "snapshot", "create", owned["name"], "--name", owned["snapshot_name"],
        *(("--pause",) if pause else ()), "--json", check=False, timeout=240,
    )
    intents = [line.removeprefix("Snapshot request ID: ") for line in result.stderr.splitlines()
               if line.startswith("Snapshot request ID: ")]
    assert len(intents) <= 1, result.stderr
    if intents:
        owned["snapshot_id"] = intents[0]
        assert len(intents[0]) == 64 and all(c in "0123456789abcdef" for c in intents[0])
    assert result.returncode == 0, (result.stdout, result.stderr)
    captured = _json_success_data(result.stdout, dict)
    assert captured["id"] == owned["snapshot_id"]
    assert captured["name"] == owned["snapshot_name"]
    assert captured["source_vm_id"] == owned["source"]["id"]
    assert captured["memory_file"] and captured["state_file"] and captured["rootfs_file"]
    listed = _json_success_data(
        _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
    )
    matches = [item for item in listed if item["id"] == captured["id"]]
    assert len(matches) == 1 and matches[0]["source_vm_id"] == owned["source"]["id"]
    inspected = _json_success_data(
        _run_mvm(runner_vm, "snapshot", "inspect", captured["id"], "--json").stdout, dict,
    )
    assert inspected["snapshot"]["id"] == captured["id"]
    assert inspected["snapshot"]["source_vm_id"] == owned["source"]["id"]
    return captured


def _restore_full_snapshot(runner_vm: str, owned: dict, *, resume: bool) -> dict:
    """Restore only onto the admitted captured endpoint after source removal."""
    _remove_snapshot_vm(runner_vm, owned["name"])
    restored = _json_success_data(
        _run_mvm(
            runner_vm, "snapshot", "restore", owned["snapshot_id"], owned["name"],
            *(("--resume",) if resume else ()), "--json", timeout=180,
        ).stdout, list,
    )
    assert len(restored) == 1, restored
    vm = restored[0]
    assert vm["name"] == owned["name"]
    assert vm["id"] != owned["source"]["id"]
    assert vm["status"] == ("running" if resume else "paused")
    for key in ("network_id", "ipv4", "mac", "vcpu_count", "mem_size_mib", "disk_size_mib"):
        assert vm[key] == owned["source"][key], (key, vm, owned["source"])
    return _snapshot_vm_state(runner_vm, owned["name"], vm["status"])


def _assert_snapshot_guest_restored(runner_vm: str, owned: dict) -> None:
    """Full restore preserves guest boot state and synced disk bytes over working vsock."""
    assert _snapshot_boot_id(runner_vm, owned["name"]) == owned["boot_id"]
    assert _run_mvm(
        runner_vm, "exec", owned["name"], "--", "cat", "/root/snapshot-marker",
    ).stdout == owned["marker"]


class TestSnapshotDestroyRestore:
    """Running full capture survives source removal and restores the captured guest."""

    def test_snapshot_destroy_restore_workflow(self, runner_vm: str, snapshot_vm: dict) -> None:
        """Running capture resumes its source and restores full guest state after destruction."""
        name = snapshot_vm["name"]
        identity = _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running"))
        _capture_full_snapshot(runner_vm, snapshot_vm)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running")) == identity
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)
        restored = _restore_full_snapshot(runner_vm, snapshot_vm, resume=True)
        assert _snapshot_process_identity(restored) != identity
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)


class TestSnapshotCreateFlags:
    """Caller naming and --pause are observed on a running full capture."""

    def test_snapshot_create_with_name(self, runner_vm: str, snapshot_vm: dict) -> None:
        """The exact caller-supplied name is published in both listing and inspection."""
        snapshot_vm["snapshot_name"] = f"caller-snapshot-{uuid.uuid4().hex}"
        captured = _capture_full_snapshot(runner_vm, snapshot_vm)
        listed = _json_success_data(
            _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
        )
        matches = [item for item in listed if item["id"] == captured["id"]]
        assert len(matches) == 1, matches
        assert matches[0]["name"] == snapshot_vm["snapshot_name"]
        assert matches[0]["source_vm_name"] == snapshot_vm["name"]
        inspected = _json_success_data(
            _run_mvm(runner_vm, "snapshot", "inspect", captured["id"], "--json").stdout, dict,
        )
        assert inspected["snapshot"]["name"] == snapshot_vm["snapshot_name"]
        assert inspected["snapshot"]["id"] == captured["id"]

    def test_snapshot_create_pause_flag(self, runner_vm: str, snapshot_vm: dict) -> None:
        """--pause leaves the originally running source paused without replacing its process."""
        name = snapshot_vm["name"]
        identity = _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running"))
        _capture_full_snapshot(runner_vm, snapshot_vm, pause=True)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "paused")) == identity
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running")) == identity
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)


class TestSnapshotInspect:
    """Public inspection exposes the captured identity and selected resources."""

    def test_snapshot_inspect(self, runner_vm: str, snapshot_vm: dict) -> None:
        """Text inspection identifies the exact capture, source, and resource values."""
        captured = _capture_full_snapshot(runner_vm, snapshot_vm)
        output = _run_mvm(runner_vm, "snapshot", "inspect", captured["id"]).stdout
        for expected in (
            f"Snapshot: {snapshot_vm['snapshot_name']}",
            f"ID: {captured['id']}",
            f"Source VM ID: {snapshot_vm['source']['id']}",
            f"Source VM Name: {snapshot_vm['name']}",
            f"Base Dir: {captured['snapshot_dir']}",
            "Resources",
            "Vcpu: 1",
            "Mem: 256",
            "Disk: 512",
        ):
            assert expected in output, (expected, output)

    def test_snapshot_inspect_json(self, runner_vm: str, snapshot_vm: dict) -> None:
        """Schema-1 inspection retains exact nested capture metadata and resource values."""
        captured = _capture_full_snapshot(runner_vm, snapshot_vm)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "snapshot", "inspect", captured["id"], "--json").stdout, dict,
        )
        assert set(inspected) == {"snapshot", "assets", "resources"}, inspected
        assert captured["created_at"]
        assert inspected["snapshot"] == {
            "id": captured["id"],
            "name": snapshot_vm["snapshot_name"],
            "source_vm_id": snapshot_vm["source"]["id"],
            "source_vm_name": snapshot_vm["name"],
            "base_dir": captured["snapshot_dir"],
            "created_at": captured["created_at"],
        }
        assert inspected["resources"] == {"vcpu": 1, "mem": 256, "disk": 512}
        assert isinstance(inspected["assets"], dict), inspected




class TestVMSnapshot:
    """Paused capture and both restore modes preserve the captured guest and endpoint."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_snapshot_while_paused(self, runner_vm: str, snapshot_vm: dict) -> None:
        """A full capture of an already paused VM retains its process and paused state."""
        name = snapshot_vm["name"]
        identity = _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running"))
        _run_mvm(runner_vm, "vm", "pause", name)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "paused")) == identity
        _capture_full_snapshot(runner_vm, snapshot_vm)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "paused")) == identity
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running")) == identity
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)

    def test_snapshot_then_restore(self, runner_vm: str, snapshot_vm: dict) -> None:
        """Default full restore starts paused; explicit resume retains that process and guest boot."""
        name = snapshot_vm["name"]
        _capture_full_snapshot(runner_vm, snapshot_vm)
        _snapshot_vm_state(runner_vm, name, "running")
        restored = _restore_full_snapshot(runner_vm, snapshot_vm, resume=False)
        identity = _snapshot_process_identity(restored)
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _snapshot_process_identity(_snapshot_vm_state(runner_vm, name, "running")) == identity
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)

    def test_restore_with_resume_flag(self, runner_vm: str, snapshot_vm: dict) -> None:
        """A paused full capture restores running with --resume and preserved guest bytes."""
        name = snapshot_vm["name"]
        _run_mvm(runner_vm, "vm", "pause", name)
        _snapshot_vm_state(runner_vm, name, "paused")
        _capture_full_snapshot(runner_vm, snapshot_vm)
        _snapshot_vm_state(runner_vm, name, "paused")
        restored = _restore_full_snapshot(runner_vm, snapshot_vm, resume=True)
        _snapshot_process_identity(restored)
        _assert_snapshot_guest_restored(runner_vm, snapshot_vm)
