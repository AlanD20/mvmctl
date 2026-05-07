"""Config management system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_config]


class TestConfigLifecycle:
    """Test config read and write operations."""

    def test_config_get_existing(self, mvm_binary):
        """Get an existing config value."""
        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout

    def test_config_set_and_get(self, mvm_binary):
        """Set a config value and read it back."""
        result = _run_mvm(
            mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "4"
        )
        assert result.returncode == 0

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert result.returncode == 0
        assert "4" in result.stdout

        # Cleanup: reset back to default
        _run_mvm(mvm_binary, "config", "reset", "defaults.vm", "vcpu_count")

    def test_config_reset(self, mvm_binary):
        """Reset a config value to its default."""
        _run_mvm(mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "4")

        result = _run_mvm(
            mvm_binary, "config", "reset", "defaults.vm", "vcpu_count"
        )
        assert result.returncode == 0

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert result.returncode == 0
        assert "4" not in result.stdout

    def test_config_list(self, mvm_binary):
        """List all overridable settings."""
        result = _run_mvm(mvm_binary, "config", "list")
        assert result.returncode == 0
        assert result.stdout.strip()
        assert "[defaults.vm]" in result.stdout

    def test_config_reset_all(self, mvm_binary):
        """Reset all config overrides globally."""
        # First set a value so there is something to reset
        _run_mvm(mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "6")

        # Reset all
        result = _run_mvm(mvm_binary, "config", "reset", "--all")
        assert result.returncode == 0

        # Verify the value is no longer the custom one
        result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
        )
        assert result.returncode == 0
        assert "6" not in result.stdout
