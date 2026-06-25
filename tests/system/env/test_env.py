"""Env management system tests — smoke tests for env CLI."""
from __future__ import annotations
import json, pytest, uuid
from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_env]

class TestEnvHelp:
    """Test env command help output."""
    def test_env_help(self, runner_vm):
        """``mvm env --help`` should show Usage."""
        result = _run_mvm(runner_vm, "env", "--help")
        assert "Usage:" in result.stdout
        assert "apply" in result.stdout
        assert "destroy" in result.stdout

class TestEnvLs:
    """Test env list (may be empty initially — smoke test)."""
    def test_env_ls_empty(self, runner_vm):
        """``mvm env ls`` should list environments (may be empty)."""
        result = _run_mvm(runner_vm, "env", "ls")
        assert result.returncode == 0
        # May have output or be empty — just verify the command runs

class TestEnvDiff:
    """Test env diff with a simple spec."""
    def test_env_diff_help(self, runner_vm):
        """``mvm env diff --help`` should show Usage."""
        result = _run_mvm(runner_vm, "env", "diff", "--help")
        assert "Usage:" in result.stdout
