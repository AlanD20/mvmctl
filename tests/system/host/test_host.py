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
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
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
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert "kvm_accessible" in data
        assert "required_binaries" in data
        assert "ip_forward" in data

    def test_host_ls_uninitialized(self, mvm_binary):
        """Calling host ls --json on uninitialized host should error.

        If the host is already initialized, this test is skipped because
        we cannot safely de-initialize a production system.
        """
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode == 0:
            pytest.skip("Host is initialized — cannot test uninitialized path")
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined
            for s in ["error", "not initialized", "not found", "no such"]
        ), f"Unexpected output for uninitialized host: {combined}"


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
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
            output = (result.stdout + result.stderr).lower()
            assert (
                "running" in output
                or "cannot clean" in output
                or "stop" in output
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
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
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
            output = (result.stdout + result.stderr).lower()
            assert (
                "running" in output
                or "cannot reset" in output
                or "stop" in output
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                "--name",
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
        check = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if check.returncode != 0:
            pytest.skip("Host not initialized — cannot test host clean")

        mvm_bin = Path.home() / ".local" / "bin" / "mvm"
        if not mvm_bin.exists():
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
