"""VM SSH access system tests."""

from __future__ import annotations

import time as _time

import pytest

from tests.system.conftest import _guest_run, _run_mvm, wait_for_ssh

pytestmark = [pytest.mark.system, pytest.mark.domain_ssh]


class TestSSHConnect:
    """Test mvm ssh connect (non-interactive via --cmd)."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_command_execution(self, runner_vm, module_vm, timing_targets):
        """Execute a command via SSH on a running VM using name as positional arg."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        ssh_available = wait_for_ssh(
            runner_vm, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            runner_vm,
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

    def test_ssh_nonexistent_vm(self, runner_vm):
        """SSH to nonexistent VM should fail gracefully."""
        result = _run_mvm(
            runner_vm,
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

    def test_ssh_timeout_flag_fails_fast(self, runner_vm):
        """SSH with --timeout to nonexistent VM should fail fast (<5s not 60s)."""
        start = _time.monotonic()
        result = _run_mvm(
            runner_vm,
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
    def test_ssh_with_ip_flag(self, runner_vm, module_vm, timing_targets):
        """Connect via IP as positional arg instead of VM name."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        ssh_available = wait_for_ssh(
            runner_vm, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["ipv4"],
            "-u", "root",
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
    def test_ssh_with_user_flag(self, runner_vm, module_vm, timing_targets):
        """SSH with explicit --user flag."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        wait_for_ssh(runner_vm, vm_info["name"], "root", ssh_timeout)

        result = _run_mvm(
            runner_vm,
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
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with explicit --key pointing to a private key file.

        Creates a throwaway key inside the test VM, then attempts SSH
        with --key pointing to the unauthorized key file path.
        The SSH connection should attempt and fail with Permission denied
        since the key isn't authorized on the VM.
        """
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        wait_for_ssh(runner_vm, vm_info["name"], "root", ssh_timeout)

        # Create a throwaway key inside the test VM for the SSH test
        test_key = "/tmp/ssh_test_key"
        _guest_run(
            runner_vm,
            f"rm -f {test_key} {test_key}.pub && "
            f"ssh-keygen -t ed25519 -f {test_key} -N '' -q",
            timeout=30,
        )

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "--key",
            test_key,
            "--cmd",
            "whoami",
            check=False,
        )
        # Will likely fail because the key isn't authorized on the VM,
        # but the SSH connection itself should attempt and fail gracefully.
        assert result.returncode != 0, (
            f"SSH with unauthorized key should fail, got rc={result.returncode} "
            f"stdout: {result.stdout} stderr: {result.stderr}"
        )
        assert "Permission denied" in result.stderr or "timed out" in result.stderr, (
            f"Expected 'Permission denied' or 'timed out' for unauthorized key, "
            f"got stderr: {result.stderr}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_exported_key_file(
        self, runner_vm, created_vm, timing_targets
    ):
        """SSH with --key pointing to an exported private key file path.

        Uses created_vm fixture (which has an authorized key), exports the
        key to a temp dir inside the test VM, then SSHes with --key pointing
        to the exported file path. Verifies SSH succeeds with the exported key.
        """
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine:3.23"]

        # Look up the key used by this VM via its ssh_keys field
        vm_ssh_keys = vm_info.get("ssh_keys", [])
        assert vm_ssh_keys, "VM has no ssh_keys — cannot test exported key"

        key_id = vm_ssh_keys[0]
        key_ls_result = _run_mvm(runner_vm, "key", "ls", "--json", check=False)
        assert key_ls_result.returncode == 0, "key ls --json failed"

        import json as _json
        keys = _json.loads(key_ls_result.stdout)
        matching_keys = [
            k for k in keys
            if k.get("id") == key_id or k.get("name") == key_id
        ]
        assert matching_keys, f"Key with id/name {key_id[:16]}... not found in cache"
        key_name = matching_keys[0]["name"]

        # Export the key to a temp directory inside the test VM
        key_export_dir = "/tmp/ssh-keys"
        _guest_run(runner_vm, f"rm -rf {key_export_dir} && mkdir -p {key_export_dir}")

        export_result = _run_mvm(
            runner_vm,
            "key",
            "export",
            key_name,
            key_export_dir,
            check=False,
        )
        assert export_result.returncode == 0, (
            f"Key export failed: {export_result.stderr}"
        )

        # Find the exported private key file (inside test VM)
        ls_result = _guest_run(
            runner_vm, f"ls -1 {key_export_dir}/", timeout=30
        )
        exported_files = ls_result.stdout.strip().splitlines()
        private_key_name = None
        for fname in exported_files:
            fname = fname.strip()
            if not fname:
                continue
            if fname.endswith(".pem") or (not fname.endswith(".pub")):
                private_key_name = fname
                break
        assert private_key_name, "Could not find exported private key file"
        private_key_path = f"{key_export_dir}/{private_key_name}"

        # Verify the private key file exists inside test VM
        check_result = _guest_run(
            runner_vm,
            f"test -f {private_key_path} && echo exists",
            check=False,
        )
        assert check_result.returncode == 0 and "exists" in check_result.stdout, (
            f"Exported key not found: {private_key_path}"
        )

        ssh_available = wait_for_ssh(
            runner_vm, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "-u", "root",
            "--key",
            private_key_path,
            "--cmd",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, (
            f"SSH with exported key file failed: rc={result.returncode} "
            f"stdout={result.stdout} stderr={result.stderr}"
        )
        assert "root" in result.stdout, (
            f"Expected 'root' in output, got: {result.stdout}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_named_key(
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with --key specifying a named key (from key cache).

        Creates a named key and attempts SSH. The connection may fail since
        the key isn't authorized on the module_vm — that's acceptable. We're
        verifying the CLI parsed the named key and attempted to use it.
        """
        import uuid as _uuid

        vm_info = module_vm

        key_name = f"sys-ssh-key-{_uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )

            result = _run_mvm(
                runner_vm,
                "ssh",
                vm_info["name"],
                "--key",
                key_name,
                "--cmd",
                "exit",
                check=False,
            )
            # The connection may fail if the key isn't authorized on the VM
            if result.returncode != 0:
                combined = (result.stdout + result.stderr).lower()
                assert (
                    "permission denied" in combined
                    or "not found" in combined
                    or "no route to host" in combined
                    or "connection refused" in combined
                    or "timed out" in combined
                ), (
                    f"Unexpected error with named key: "
                    f"stdout={result.stdout} stderr={result.stderr}"
                )
        finally:
            _run_mvm(runner_vm, "key", "rm", key_name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_timeout_flag_success(
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with --timeout on a real VM should succeed."""
        vm_info = module_vm

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "--timeout",
            "30",
            "--cmd",
            "echo timeout-test-ok",
            check=False,
        )
        assert result.returncode == 0, (
            f"SSH with --timeout failed: {result.stderr}"
        )
        assert "timeout-test-ok" in result.stdout, (
            f"Expected 'timeout-test-ok' in output, got: {result.stdout}"
        )
