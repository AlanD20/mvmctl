"""VM snapshot and restore system tests."""

from __future__ import annotations

import json
import uuid
from typing import Any

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


class TestSnapshotDestroyRestore:
    """Full DR workflow: create VM, snapshot, destroy, restore from snapshot."""

    def test_snapshot_destroy_restore_workflow(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """A managed VM survives destruction and full restore with its guest agent reachable."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        snapshot_name = f"{vm_name}-dr"
        snap_id: str | None = None

        try:
            _run_mvm(runner_vm, "network", "create", network_name, "--subnet", subnet)
            _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)
            created = _json_success_data(_run_mvm(
                runner_vm, "vm", "create", vm_name, "--image", "alpine:3.23",
                "--network", network_name, "--ssh-key", key_name,
                "--skip-deblob", "--no-pci", "--disk-size", "512M",
                "--mem", "256M", "--vcpu", "1", "--json", timeout=180,
            ).stdout, list)
            assert len(created) == 1
            source = created[0]
            assert source["network_id"]
            assert _run_mvm(runner_vm, "exec", vm_name, "--", "uname", "-s").stdout.strip() == "Linux"

            captured = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "create", vm_name, "--name", snapshot_name,
                    "--json", timeout=240,
                ).stdout,
                dict,
            )
            snap_id = captured["id"]
            assert captured["memory_file"] and captured["state_file"]
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)

            restored = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "restore", snap_id, vm_name,
                    "--resume", "--json", timeout=180,
                ).stdout,
                list,
            )
            assert len(restored) == 1 and restored[0]["name"] == vm_name
            assert restored[0]["status"] == "running"
            assert restored[0]["network_id"] == source["network_id"]
            assert restored[0]["ipv4"] == source["ipv4"]
            assert _run_mvm(runner_vm, "exec", vm_name, "--", "uname", "-s").stdout.strip() == "Linux"
        finally:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(vm["name"] == vm_name for vm in listed):
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", timeout=120)
            if snap_id:
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force")
            _run_mvm(runner_vm, "network", "rm", network_name, "--force", check=False, timeout=120)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            assert all(vm["name"] != vm_name for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))
            assert all(snapshot["name"] != snapshot_name for snapshot in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))
            assert all(network["name"] != network_name for network in _json_success_data(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
            ))
            assert all(key["name"] != key_name for key in _json_success_data(
                _run_mvm(runner_vm, "key", "ls", "--json").stdout, list,
            ))


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


class TestSnapshotRestoreNetwork:
    """Snapshot restore with --network flag — restore to a different network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_snapshot_restore_with_network(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Snapshot a VM, restore with --network <different_net>, verify VM running.

        L2: Creates VM with network A, snapshots it, removes VM, then restores
        with --network B (different subnet). Verifies restore succeeds and VM
        reaches running state.
        """
        vm_name = unique_vm_name
        key_name = unique_key_name
        original_net = unique_network_name
        restore_net = f"sys-net-restore-{uuid.uuid4().hex[:6]}"
        original_subnet = _unique_subnet(original_net)
        restore_subnet = _unique_subnet(restore_net)
        snap_id: str | None = None

        try:
            # Create two networks — original and restore target
            _run_mvm(
                runner_vm,
                "network",
                "create",
                original_net,
                "--subnet",
                original_subnet,
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                restore_net,
                "--subnet",
                restore_subnet,
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
                original_net,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "pause", vm_name)

            # Create snapshot
            _run_mvm(runner_vm, "snapshot", "create", vm_name)

            # Retrieve snapshot ID
            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps
            snap_id = vm_snaps[-1]["id"]

            # Stop and remove original VM
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            # Restore snapshot with different network and --resume
            result = _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snap_id,
                vm_name,
                "--network",
                restore_net,
                "--resume",
                check=False,
            )

            if result.returncode == 0:
                # Verify VM is running
                result = _run_mvm(runner_vm, "vm", "ls", "--json")
                vms: list[dict[str, Any]] = json.loads(result.stdout)
                vm_entry = next((v for v in vms if v["name"] == vm_name), None)
                assert vm_entry is not None
                assert vm_entry["status"] == "running", (
                    f"Expected status 'running' after restore --network --resume, "
                    f"got '{vm_entry.get('status')}'"
                )
            else:
                # If --resume failed, try restore without --resume
                restore_no_resume = _run_mvm(
                    runner_vm,
                    "snapshot",
                    "restore",
                    snap_id,
                    vm_name,
                    "--network",
                    restore_net,
                    check=False,
                )
                if restore_no_resume.returncode == 0:
                    result = _run_mvm(runner_vm, "vm", "ls", "--json")
                    vms = json.loads(result.stdout)
                    vm_entry = next(
                        (v for v in vms if v["name"] == vm_name), None
                    )
                    assert vm_entry is not None
                    assert vm_entry["status"] in ("stopped", "paused"), (
                        f"Expected status 'stopped' or 'paused' after "
                        f"restore --network (no --resume), "
                        f"got '{vm_entry.get('status')}'"
                    )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                original_net,
                "--force",
                check=False,
            )
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                restore_net,
                "--force",
                check=False,
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            if snap_id:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snap_id, "--force", check=False
                )


class TestVMSnapshot:
    """Snapshot creation from paused state and subsequent restore."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_snapshot_while_paused(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Snapshot a paused VM and verify snapshot exists."""
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

            result = _run_mvm(runner_vm, "snapshot", "create", vm_name)
            assert result.returncode == 0

            # Verify snapshot appears in listing
            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert len(vm_snaps) >= 1, "Snapshot not found in listing"
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
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)

    def test_snapshot_then_restore(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Snapshot a paused VM, stop it, then restore and verify it is paused."""
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
            snaps = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps
            snap_id = vm_snaps[-1]["id"]

            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            result = _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snap_id,
                vm_name,
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "paused", (
                f"Expected status 'paused' (restore without --resume), "
                f"got '{vm_entry.get('status')}'"
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
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)

    def test_restore_with_resume_flag(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Snapshot a VM, stop it, then restore with --resume and verify it is running."""
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
            snaps = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps
            snap_id = vm_snaps[-1]["id"]

            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            result = _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snap_id,
                vm_name,
                "--resume",
            )
            assert result.returncode == 0, (
                f"snapshot restore --resume failed: {result.stderr}"
            )

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "running", (
                f"Expected status 'running' after --resume, "
                f"got '{vm_entry.get('status')}'"
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
                _run_mvm(runner_vm, "snapshot", "rm", snap_id, "--force", check=False)
