"""VM log streaming system tests."""

from __future__ import annotations

import os
import shlex
import subprocess

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestVMLogs:
    """Test VM log viewing operations."""

    def test_logs_boot_output(self, mvm_binary, created_vm):
        """Show boot log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", created_vm["name"])
        assert result.returncode == 0
        assert result.stdout.strip()

    def test_logs_os_output(self, mvm_binary, created_vm):
        """Show Firecracker OS log for a running VM."""
        result = _run_mvm(mvm_binary, "logs", created_vm["name"], "--os")
        assert result.returncode == 0

    def test_logs_lines_limit(self, mvm_binary, created_vm):
        """Show last N lines of boot log."""
        result = _run_mvm(
            mvm_binary, "logs", created_vm["name"], "--lines", "5"
        )
        assert result.returncode == 0
        assert len(result.stdout.splitlines()) <= 5 + 2

    def test_logs_follow_runs(self, mvm_binary, created_vm):
        """Follow log output for a brief period."""
        cmd = [*shlex.split(mvm_binary), "logs", created_vm["name"], "--follow"]
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=2,
                env={**os.environ, "NO_COLOR": "1"},
            )
            # If it exits before timeout, verify it ran successfully
            assert result.returncode == 0
        except subprocess.TimeoutExpired:
            # Expected: --follow keeps streaming until interrupted
            pass
