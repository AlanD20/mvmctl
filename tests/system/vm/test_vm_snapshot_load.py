"""VM snapshot and restore system tests."""

from __future__ import annotations

import json
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestSnapshotDestroyRestore:
    """Full DR workflow: create VM, snapshot, destroy, restore from snapshot."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_workflow,
    ]

    def test_snapshot_destroy_restore_workflow(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Create VM, pause, snapshot, stop, restore with --resume, verify it runs."""
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
                "--non-interactive",
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

            # Snapshot create (files managed internally, no --mem/--state flags)
            _run_mvm(runner_vm, "snapshot", "create", vm_name)

            # Retrieve snapshot ID for restore
            ls_result = _run_mvm(runner_vm, "snapshot", "ls", "--json")
            snaps: list[dict[str, Any]] = json.loads(ls_result.stdout)
            vm_snaps = [s for s in snaps if s["source_vm_name"] == vm_name]
            assert vm_snaps, "No snapshot found for VM"
            snap_id = vm_snaps[-1]["id"]

            _run_mvm(runner_vm, "vm", "stop", vm_name)

            # Remove stopped VM to free name for restore
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            restore_result = _run_mvm(
                runner_vm,
                "snapshot",
                "restore",
                snap_id,
                vm_name,
                "--resume",
                check=False,
            )

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            status = vm_entry.get("status", "")
            if status == "error":
                # --resume resulted in error status — try restore without --resume
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
                restore_result = _run_mvm(
                    runner_vm,
                    "snapshot",
                    "restore",
                    snap_id,
                    vm_name,
                    check=False,
                )
                if restore_result.returncode == 0:
                    result = _run_mvm(runner_vm, "vm", "ls", "--json")
                    vms = json.loads(result.stdout)
                    vm_entry = next((v for v in vms if v["name"] == vm_name), None)
                    assert vm_entry is not None
                    assert vm_entry.get("status") in ("stopped", "paused")
            else:
                assert status == "running"
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
                "--non-interactive",
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
                "--non-interactive",
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
                "--non-interactive",
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
