"""Run command smoke tests — internal service entry points."""
from __future__ import annotations
import pytest
from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_run]

class TestRunHelp:
    """Test run command help output."""
    def test_run_help(self, runner_vm):
        """``mvm run --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "--help")
        assert "Usage:" in result.stdout
        assert "console" in result.stdout
        assert "nocloudnet" in result.stdout

    def test_run_console_help(self, runner_vm):
        """``mvm run console --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "console", "--help")
        assert "Usage:" in result.stdout

    def test_run_nocloudnet_help(self, runner_vm):
        """``mvm run nocloudnet --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "nocloudnet", "--help")
        assert "Usage:" in result.stdout

    def test_run_provision_help(self, runner_vm):
        """``mvm run provision --help`` should show Usage."""
        result = _run_mvm(runner_vm, "run", "provision", "--help")
        assert "Usage:" in result.stdout
