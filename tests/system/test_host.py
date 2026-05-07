"""Host configuration system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.serial, pytest.mark.domain_vm]


class TestHostStatus:
    """Test host status and inspection operations (non-destructive)."""

    def test_host_ls_basic(self, mvm_binary):
        """Show current host configuration state (table output)."""
        result = _run_mvm(mvm_binary, "host", "ls", check=False)
        # host ls may fail if host not initialized, which is acceptable
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        assert "KVM" in result.stdout or "/dev/kvm" in result.stdout

    def test_host_ls_json(self, mvm_binary):
        """Show current host configuration state in JSON format."""
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert "kvm_accessible" in data
        assert "required_binaries" in data
        assert "ip_forward" in data


class TestHostCleanSafety:
    """Test host clean safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_vm,
    ]

    def test_host_clean_blocked_by_running_vm(self, mvm_binary, unique_vm_name):
        """Host clean should be blocked when a VM is running."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
        pytest.mark.domain_vm,
    ]

    def test_host_reset_blocked_by_running_vm(self, mvm_binary, unique_vm_name):
        """Host reset should be blocked when a VM is running."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
