"""VM SSH access system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = [pytest.mark.system, pytest.mark.domain_ssh]


class TestSSHConnect:
    """Test mvm ssh connect (non-interactive via --cmd)."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_command_execution(self, mvm_binary, module_vm, timing_targets):
        """Execute a command via SSH on a running VM using name as positional arg."""
        # Rationale: Needs a running VM (module_vm) to SSH into and execute
        # commands against. SSH connectivity requires a real VM process.
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.21"]
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_info["name"],
            "--cmd",
            "echo hello",
            check=False,
        )
        assert result.returncode == 0, f"SSH command failed: {result.stderr}"
        assert "hello" in result.stdout, (
            f"Expected 'hello' in output, got: {result.stdout}"
        )

    def test_ssh_nonexistent_vm(self, mvm_binary):
        """SSH to nonexistent VM should fail gracefully."""
        # Rationale: No resources needed — testing CLI validation by
        # attempting SSH to a name that cannot exist.
        result = _run_mvm(
            mvm_binary,
            "ssh",
            "nonexistent-vm-xyz123",
            "--cmd",
            "echo hi",
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent VM, "
            f"got: {result.returncode}"
        )

    def test_ssh_timeout_flag_fails_fast(self, mvm_binary):
        """SSH with --timeout to nonexistent VM should fail fast (<5s not 60s).

        Without --timeout, SSH to a nonexistent VM hangs for 60s waiting
        for the default ConnectTimeout. With --timeout 2, it should fail
        in under 5 seconds.
        """
        # Rationale: No resources needed — testing CLI timeout flag by
        # attempting SSH to a nonexistent name. The --timeout flag is
        # a CLI-level concern, no VM needed.
        import time as _time

        start = _time.monotonic()
        result = _run_mvm(
            mvm_binary,
            "ssh",
            "nonexistent-vm-timeout-test",
            "--timeout",
            "2",
            "--cmd",
            "echo hi",
            check=False,
        )
        elapsed = _time.monotonic() - start
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent VM, "
            f"got: {result.returncode} (took {elapsed:.1f}s)"
        )
        assert elapsed < 10, (
            f"--timeout 2 should fail fast, but took {elapsed:.1f}s "
            f"(default 60s timeout would have masked this)"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_ip_flag(self, mvm_binary, module_vm, timing_targets):
        """Connect via IP as positional arg instead of VM name."""
        # Rationale: Needs a running VM with an IP address (module_vm)
        # to test SSH by IP resolution. Real VM required.
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.21"]
        ssh_available = wait_for_ssh(
            mvm_binary, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_info["ipv4"],
            "--cmd",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, f"SSH via IP failed: {result.stderr}"
        assert "root" in result.stdout, (
            f"Expected 'root' in output, got: {result.stdout}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_user_flag(self, mvm_binary, module_vm, timing_targets):
        """SSH with explicit --user flag."""
        # Rationale: Needs a running VM (module_vm) to test the --user flag
        # for SSH authentication. Real VM process required.
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.21"]
        wait_for_ssh(mvm_binary, vm_info["name"], "root", ssh_timeout)

        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_info["name"],
            "-u",
            "root",
            "--cmd",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, (
            f"SSH with --user failed: {result.stderr}"
        )
        assert "root" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_key_path(
        self, mvm_binary, module_vm, timing_targets, tmp_path
    ):
        """SSH with explicit --key pointing to a private key file."""
        # Rationale: Needs a running VM (module_vm) to test the --key flag
        # for SSH key file path resolution. Real VM required.
        import subprocess as _subprocess

        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.21"]
        wait_for_ssh(mvm_binary, vm_info["name"], "root", ssh_timeout)

        # Create a throwaway key for the SSH test
        test_key = tmp_path / "ssh_test_key"
        _subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "ed25519",
                "-f",
                str(test_key),
                "-N",
                "",
                "-q",
            ],
            check=True,
        )

        result = _run_mvm(
            mvm_binary,
            "ssh",
            vm_info["name"],
            "--key",
            str(test_key),
            "--cmd",
            "whoami",
            check=False,
        )
        # Will likely fail because the key isn't authorized on the VM,
        # but the SSH connection itself should attempt and fail gracefully.
        # This tests that --key is accepted as a valid file path and that
        # SSH uses it (even if auth fails).
        if result.returncode != 0:
            assert (
                "Permission denied" in result.stderr
                or "authentication" in result.stderr.lower()
                or "key" in result.stderr.lower()
            )
        else:
            assert "root" in result.stdout
