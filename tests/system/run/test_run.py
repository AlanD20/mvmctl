"""Installed-CLI checks for internal service entry points."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_run]


class TestRunEntryPoints:
    """Test supported internal-service entry points and admission."""

    def test_run_help(self, runner_vm):
        """``mvm run --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "--help")
        assert "Usage:" in result.stdout
        assert "console" in result.stdout
        assert "nocloudnet" in result.stdout
        assert "provision" not in result.stdout

    def test_run_console_help(self, runner_vm):
        """``mvm run console --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "console", "--help")
        assert "Usage:" in result.stdout

    def test_run_nocloudnet_help(self, runner_vm):
        """``mvm run nocloudnet --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "nocloudnet", "--help")
        assert "Usage:" in result.stdout

    @pytest.mark.parametrize(
        ("service", "required_flags"),
        [
            ("console relay", ("vm-id", "vm-path", "pty-fd")),
            ("nocloudnet serve", ("network-id", "log-file")),
        ],
    )
    def test_run_service_requires_identity(self, runner_vm, service, required_flags):
        """Internal service leaves reject missing identity before side effects."""
        result = _run_mvm(runner_vm, "run", *service.split(), check=False)
        assert result.returncode != 0
        error = result.stderr or result.stdout
        assert "required flag(s)" in error
        for flag in required_flags:
            assert f'"{flag}"' in error
