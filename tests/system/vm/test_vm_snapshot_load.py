"""VM snapshot and load system tests — extracted from test_full_journeys.py."""

from __future__ import annotations

import json
from pathlib import Path
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

    pytestmark = [pytest.mark.domain_workflow]

    @pytest.mark.requires_network
    def test_snapshot_destroy_restore_workflow(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(mvm_binary, "vm", "pause", vm_name)

            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data: dict[str, Any] = json.loads(result.stdout)
            vm_dir = Path(str(data["vm_dir"]))
            mem_file = str(vm_dir / "mem.snap")
            state_file = str(vm_dir / "state.snap")

            _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                mem_file,
                state_file,
            )

            assert Path(mem_file).exists(), (
                f"Memory snapshot not found: {mem_file}"
            )
            assert Path(mem_file).stat().st_size > 0, "Memory snapshot is empty"
            assert Path(state_file).exists(), (
                f"State snapshot not found: {state_file}"
            )
            assert Path(state_file).stat().st_size > 0, (
                "State snapshot is empty"
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            _run_mvm(
                mvm_binary,
                "vm",
                "load",
                vm_name,
                mem_file,
                state_file,
                "--resume",
            )

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None, (
                f"VM '{vm_name}' not found after restore"
            )
            assert vm_entry.get("status") == "running", (
                f"Expected 'running', got '{vm_entry.get('status')}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
