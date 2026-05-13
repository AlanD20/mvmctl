"""Config management system tests.

Merged from: test_config.py (existing), test_cli_edge_cases.py (config test classes)
"""

from __future__ import annotations

import re

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
        match = re.search(
            r"vcpu_count\s*[=:]\s*(\d+|\(default\))", result.stdout
        )
        assert match, f"Could not find vcpu_count value in: {result.stdout}"
        value_str = match.group(1)
        if value_str.isdigit():
            assert int(value_str) > 0, (
                f"Expected positive vcpu_count, got {value_str}"
            )
        else:
            assert value_str == "(default)", (
                f"Unexpected value format: {value_str}"
            )

    @pytest.mark.serial
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

    @pytest.mark.serial
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

    @pytest.mark.serial
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


# ============================================================================
# Config edge cases (from test_cli_edge_cases.py)
# ============================================================================


class TestConfigEdgeCases:
    """Tests for config command edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_config]

    def test_config_get_category_only(self, mvm_binary):
        """``config get defaults.vm`` (no key) should return multiple keys."""
        result = _run_mvm(mvm_binary, "config", "get", "defaults.vm")
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout
        assert "mem_size_mib" in result.stdout
        assert "boot_args" in result.stdout

    @pytest.mark.serial
    def test_config_reset_category_only(self, mvm_binary):
        """``config reset defaults.vm`` (no key) should reset all keys in category."""
        _run_mvm(mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "6")

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "6" in result.stdout

        result = _run_mvm(mvm_binary, "config", "reset", "defaults.vm")
        assert result.returncode == 0
        assert "override(s)" in result.stdout

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "6" not in result.stdout

    def test_config_reset_no_args(self, mvm_binary):
        """``config reset`` with no args should print guidance (exit 0)."""
        result = _run_mvm(mvm_binary, "config", "reset")
        assert result.returncode == 0
        assert "Provide a category" in result.stdout

    def test_config_set_invalid_category(self, mvm_binary):
        """``config set`` with invalid category should fail."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "set",
            "nonexistent.cat",
            "some_key",
            "some_value",
            check=False,
        )
        assert result.returncode != 0


class TestConfigEdgeCasesExtended:
    """Additional config command edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_config]

    def test_config_get_nonexistent_key(self, mvm_binary):
        """``config get`` with nonexistent key should return guidance."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "defaults.vm",
            "nonexistent_key_xyz",
            check=False,
        )
        assert result.returncode == 0

    def test_config_set_invalid_value_type(self, mvm_binary):
        """``config set`` with an invalid value type (string for int) should fail."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "set",
            "defaults.vm",
            "vcpu_count",
            "not-a-number",
            check=False,
        )
        assert result.returncode != 0


class TestConfigEdgeCasesResetAllAfterSet:
    """Test config reset --all after multiple values are set."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_config,
    ]

    def test_config_reset_all_with_multiple_overrides(self, mvm_binary):
        """Set multiple config overrides, then reset --all, verify all gone."""
        _run_mvm(mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "8")
        _run_mvm(
            mvm_binary,
            "config",
            "set",
            "defaults.vm",
            "mem_size_mib",
            "2048",
        )

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "8" in result.stdout

        _run_mvm(mvm_binary, "config", "reset", "--all")

        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "8" not in result.stdout
