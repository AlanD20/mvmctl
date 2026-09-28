"""VM snapshot and restore system tests."""

from __future__ import annotations

import json
import uuid
from pathlib import Path
from typing import Any, Generator

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


def _capture_full_snapshot(runner_vm: str, owned: dict) -> dict:
    """Retain the public request ID before asserting successful full publication."""
    result = _run_mvm(
        runner_vm, "snapshot", "create", owned["name"], "--name", owned["snapshot_name"],
        "--json", check=False, timeout=240,
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
    """Snapshot create flag tests: --name, --pause."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_snapshot_create_with_name(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Create a snapshot with --name and verify custom name in ls --json."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        snap_id: str | None = None
        snap_name = "my-custom-snap"

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
            )
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "pause", vm_name)

            # Snapshot create with --name
            _run_mvm(
                runner_vm,
                "snapshot",
                "create",
                vm_name,
                "--name",
                snap_name,
            )

            # Verify snapshot name in ls --json
            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert len(vm_snaps) >= 1, "Snapshot not found in listing"
            assert vm_snaps[-1]["name"] == snap_name, (
                f"Expected snapshot name '{snap_name}', "
                f"got '{vm_snaps[-1]['name']}'"
            )
            snap_id = vm_snaps[-1]["id"]
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            if snap_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snap_id, "--force", check=False
                )

    def test_snapshot_create_pause_flag(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Create a snapshot with --pause and verify VM stays paused."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        snap_id: str | None = None

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
            )
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "pause", vm_name)

            # Snapshot create with --pause — VM should stay paused
            _run_mvm(
                runner_vm,
                "snapshot",
                "create",
                vm_name,
                "--pause",
            )

            # Verify VM is still paused
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "paused", (
                f"Expected VM status 'paused' after snapshot --pause, "
                f"got '{vm_entry.get('status')}'"
            )

            # Clean up snapshot for VM removal
            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            if vm_snaps:
                snap_id = vm_snaps[-1]["id"]
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            if snap_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snap_id, "--force", check=False
                )


class TestSnapshotInspect:
    """Snapshot inspect tests: default format and --json output."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_snapshot_inspect(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Create a snapshot and inspect it — verify basic fields."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        snap_id: str | None = None

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
            )
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "pause", vm_name)

            _run_mvm(runner_vm, "snapshot", "create", vm_name)

            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps
            snap_id = vm_snaps[-1]["id"]

            # Inspect snapshot (default format) — verify it succeeds
            result = _run_mvm(runner_vm, "snapshot", "inspect", snap_id)
            assert result.returncode == 0
            # Should contain the snapshot ID in output
            assert snap_id[:8] in result.stdout, (
                f"Expected snapshot ID prefix '{snap_id[:8]}' "
                f"in inspect output, got: {result.stdout}"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            if snap_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snap_id, "--force", check=False
                )

    def test_snapshot_inspect_json(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Create a snapshot and inspect it with --json — verify expected fields."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        snap_id: str | None = None

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
            )
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "pause", vm_name)

            _run_mvm(runner_vm, "snapshot", "create", vm_name)

            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps
            snap_id = vm_snaps[-1]["id"]

            # Inspect snapshot with --json
            result = _run_mvm(
                runner_vm,
                "snapshot",
                "inspect",
                snap_id,
                "--json",
            )
            assert result.returncode == 0
            data: dict[str, Any] = json.loads(result.stdout)
            # Verify expected fields in the inspect output
            snapshot_data = data.get("snapshot", data)
            assert "id" in snapshot_data, (
                f"Expected 'id' in snapshot inspect --json, "
                f"got keys: {list(snapshot_data.keys())}"
            )
            assert snapshot_data["id"].startswith(snap_id[:8]), (
                f"Expected snapshot id to start with prefix '{snap_id[:8]}', "
                f"got '{snapshot_data.get('id')}'"
            )
            assert "name" in snapshot_data
            assert "source_vm_name" in snapshot_data
            assert "created_at" in snapshot_data
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            if snap_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snap_id, "--force", check=False
                )




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
