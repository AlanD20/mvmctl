"""Host configuration system tests."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_host]


class TestHostStatus:
    """Test host status and inspection operations (non-destructive)."""

    def test_host_ls_basic(self, mvm_binary):
        """Show current host configuration state."""
        # Rationale: Only needs JSON output from host ls. No expensive
        # resources needed — testing structural field types in JSON response.
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert isinstance(data.get("kvm_accessible"), bool), (
            f"kvm_accessible must be bool: {data}"
        )
        assert isinstance(data.get("required_binaries"), dict), (
            f"required_binaries must be a dict: {data}"
        )

    def test_host_ls_json(self, mvm_binary):
        """Show current host configuration state in JSON format."""
        # Rationale: Only needs JSON output. No resources needed — testing
        # field presence in JSON response.
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert "kvm_accessible" in data
        assert "required_binaries" in data
        assert "ip_forward" in data

    def test_host_ls_initialized_or_uninitialized(self, mvm_binary):
        """host ls --json returns valid data or clear error depending on state.

        When the host IS initialized: verify JSON contains expected fields.
        When NOT initialized: verify a clear error message is returned.
        Either outcome is acceptable — the test always passes.
        """
        # Rationale: Only needs JSON output. No resources needed — handles
        # both initialized and uninitialized host states gracefully.
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            assert isinstance(data.get("kvm_accessible"), bool), (
                f"kvm_accessible must be bool: {data}"
            )
            assert isinstance(data.get("required_binaries"), dict), (
                f"required_binaries must be a dict: {data}"
            )
            assert "ip_forward" in data, (
                f"host ls --json missing ip_forward: {data}"
            )
        else:
            combined = (result.stdout + result.stderr).lower()
            assert "not initialized" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )


class TestHostCleanSafety:
    """Test host clean safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_clean_blocked_by_running_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Host clean should be blocked when a VM is running."""
        # Rationale: Needs a real VM because we need a running VM to trigger
        # the safety mechanism that blocks host clean. Network fixture needed
        # for VM creation.
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            created_network,
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "host",
                "clean",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestHostResetSafety:
    """Test host reset safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_reset_blocked_by_running_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Host reset should be blocked when a VM is running."""
        # Rationale: Needs a real VM because we need a running VM to trigger
        # the safety mechanism that blocks host reset. Network fixture needed
        # for VM creation.
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            created_network,
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "host",
                "reset",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestHostCleanDestructive:
    """Execute host clean --force (destructive, requires sudo).

    Excluded from default test runs via the ``host_reset`` marker and must be
    explicitly invoked.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.host_reset,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_clean_force(self, mvm_binary):
        """Execute host clean --force and verify it exits successfully."""
        # Rationale: Needs sudo binary execution via ~/.local/bin/mvm.
        # Host clean is the most destructive operation — requires real
        # host initialization to be meaningful.
        check = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if check.returncode != 0:
            # Skip-reason: Without a fully initialized host, host clean
            # is a no-op. Running "mvm host init" first would make this
            # test unconditionally runnable.
            pytest.skip("Host not initialized — cannot test host clean")

        mvm_bin = Path.home() / ".local" / "bin" / "mvm"
        if not mvm_bin.exists():
            # Skip-reason: Sudo execution requires the built binary at
            # ~/.local/bin/mvm. Run "cp dist/mvm ~/.local/bin/mvm" and
            # "python scripts/build_services.py" to enable this test.
            pytest.skip("mvm binary not at ~/.local/bin/mvm — cannot run sudo")

        # Remove any running VMs first (left by earlier tests in this file)
        vms = json.loads(
            _run_mvm(mvm_binary, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(mvm_binary, "vm", "rm", vm["name"], "--force", check=False)

        result = subprocess.run(
            ["sudo", str(mvm_bin), "host", "clean", "--force"],
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert result.returncode == 0, (
            f"host clean --force failed: {result.stderr}"
        )
        # L1: Verify stdout contains human-readable output about the clean
        assert len(result.stdout.strip()) > 0, (
            f"host clean --force produced no stdout: stderr={result.stderr}"
        )

    def test_host_reset_force(self, mvm_binary):
        """Execute host reset --force and verify it exits successfully."""
        # Rationale: Verifies the full host reset path — a destructive
        # operation that removes all mvm-created state (VMs, networks,
        # images, config, DB). This is the most thorough host reset
        # test and requires real host initialization to be meaningful.
        check = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if check.returncode != 0:
            # Skip-reason: Without a fully initialized host, host reset
            # is a no-op. Running "mvm host init" first would make this
            # test unconditionally runnable.
            pytest.skip("Host not initialized — cannot test host reset")

        mvm_bin = Path.home() / ".local" / "bin" / "mvm"
        if not mvm_bin.exists():
            # Skip-reason: Sudo execution requires the built binary at
            # ~/.local/bin/mvm. Run "cp dist/mvm ~/.local/bin/mvm" and
            # "python scripts/build_services.py" to enable this test.
            pytest.skip("mvm binary not at ~/.local/bin/mvm — cannot run sudo")

        # Remove any running VMs first (left by earlier tests in this file)
        vms = json.loads(
            _run_mvm(mvm_binary, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(mvm_binary, "vm", "rm", vm["name"], "--force", check=False)

        result = subprocess.run(
            ["sudo", str(mvm_bin), "host", "reset", "--force"],
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert result.returncode == 0, (
            f"host reset --force failed: {result.stderr}"
        )
        # L1: Verify stdout contains human-readable output about the reset
        assert len(result.stdout.strip()) > 0, (
            f"host reset --force produced no stdout: stderr={result.stderr}"
        )
