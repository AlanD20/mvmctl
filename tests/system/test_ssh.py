"""VM SSH access system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = pytest.mark.system


class TestSSHConnect:
    """Test mvm ssh connect (non-interactive via --cmd)."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_command_execution(
        self, mvm_binary, created_vm, timing_targets
    ):
        """Execute a command via SSH on a running VM using --name."""
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine-3.21"]
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            "--name",
            vm_info["name"],
            "-c",
            "echo hello",
            check=False,
        )
        assert result.returncode == 0, f"SSH command failed: {result.stderr}"
        assert "hello" in result.stdout, (
            f"Expected 'hello' in output, got: {result.stdout}"
        )

    def test_ssh_nonexistent_vm(self, mvm_binary):
        """SSH to nonexistent VM should fail gracefully."""
        result = _run_mvm(
            mvm_binary,
            "ssh",
            "nonexistent-vm-xyz123",
            "-c",
            "echo hi",
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent VM, "
            f"got: {result.returncode}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_ip_flag(self, mvm_binary, created_vm, timing_targets):
        """Connect via --ip flag instead of VM name."""
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine-3.21"]
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            "--ip",
            vm_info["ipv4"],
            "-c",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, f"SSH via --ip failed: {result.stderr}"
        assert "root" in result.stdout, (
            f"Expected 'root' in output, got: {result.stdout}"
        )
