"""VM snapshot and load system tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestVMSnapshot:
    """Test VM snapshot creation."""

    def test_vm_snapshot_creates_files(self, mvm_binary, created_vm):
        """Snapshot a running VM and verify snapshot files are created."""
        vm_name = created_vm["name"]

        # Get VM info to find vm_dir
        result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
        data = json.loads(result.stdout)
        vm_dir = Path(data["vm_dir"])

        mem_file = vm_dir / "mem.snap"
        state_file = vm_dir / "state.snap"

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0

            assert mem_file.exists(), f"Memory snapshot not found at {mem_file}"
            assert state_file.exists(), (
                f"State snapshot not found at {state_file}"
            )
            assert mem_file.stat().st_size > 0, "Memory snapshot file is empty"
            assert state_file.stat().st_size > 0, "State snapshot file is empty"
        finally:
            # Clean up snapshot files
            mem_file.unlink(missing_ok=True)
            state_file.unlink(missing_ok=True)


class TestVMSnapshotEdgeCases:
    """Test VM snapshot edge cases."""

    def test_vm_snapshot_stopped_vm_fails(
        self, mvm_binary, unique_vm_name, tmp_path
    ):
        """Snapshot a stopped VM should fail."""
        vm_name = unique_vm_name
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"

        # Create and stop the VM
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            vm_name,
            "--image",
            "alpine-3.21",
        )
        try:
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_snapshot_nonexistent_vm_fails(self, mvm_binary):
        """Snapshot a nonexistent VM should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "snapshot",
            "nonexistent-vm-xyz",
            "/tmp/nonexistent-mem.snap",
            "/tmp/nonexistent-state.snap",
            check=False,
        )
        assert result.returncode != 0


class TestVMLoadSnapshot:
    """Test VM snapshot load operations."""

    def test_vm_load_snapshot_accepts_args(self, mvm_binary, unique_vm_name):
        """Create snapshot of running VM, stop it, then load the snapshot."""
        vm_name = unique_vm_name

        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            vm_name,
            "--image",
            "alpine-3.21",
        )
        try:
            # Get vm_dir
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data = json.loads(result.stdout)
            vm_dir = Path(data["vm_dir"])
            mem_file = vm_dir / "mem.snap"
            state_file = vm_dir / "state.snap"

            # Snapshot (auto-pauses and resumes)
            _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
            )

            # Stop the VM (load requires stopped state)
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # Load the snapshot
            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_load_snapshot_with_resume(self, mvm_binary, unique_vm_name):
        """Create snapshot of running VM, stop it, then load with --resume."""
        vm_name = unique_vm_name

        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            vm_name,
            "--image",
            "alpine-3.21",
        )
        try:
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data = json.loads(result.stdout)
            vm_dir = Path(data["vm_dir"])
            mem_file = vm_dir / "mem.snap"
            state_file = vm_dir / "state.snap"

            _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                vm_name,
                str(mem_file),
                str(state_file),
                "--resume",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_load_nonexistent_vm_fails(self, mvm_binary):
        """Load snapshot for a nonexistent VM should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "load",
            "nonexistent-vm-xyz",
            "/tmp/nonexistent-mem.snap",
            "/tmp/nonexistent-state.snap",
            check=False,
        )
        assert result.returncode != 0


class TestVMCreateSkipCleanup:
    """Test VM create skip-cleanup behavior."""

    def test_vm_create_skip_cleanup_rejected_noninteractive(
        self, mvm_binary, unique_vm_name
    ):
        """--skip-cleanup prompts with typer.confirm() and fails in
        non-interactive mode. Then verify normal creation works."""
        vm_name = unique_vm_name

        # --skip-cleanup should fail in non-interactive mode because
        # typer.confirm() cannot be answered without a TTY
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            vm_name,
            "--image",
            "alpine-3.21",
            "--skip-cleanup",
            check=False,
        )
        assert result.returncode != 0, (
            "--skip-cleanup should fail in non-interactive mode since "
            "typer.confirm() cannot be answered"
        )

        # Normal creation (without --skip-cleanup) should work
        vm_name2 = f"{vm_name}-normal"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name2,
                "--image",
                "alpine-3.21",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == vm_name2 for v in vms), (
                f"VM {vm_name2} not found after creation"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name2, "--force", check=False)
