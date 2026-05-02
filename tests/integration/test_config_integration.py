"""Integration tests for the Config API through the real public API.

Tests exercise the complete config workflow:
  get → set → get → reset → get → list_all → get category

All operations run through the real api/ and core/ layers with a live
SQLite database.  No subprocess mocking is required.
"""

from __future__ import annotations

import pytest

from mvmctl.api import ConfigOperation
from mvmctl.exceptions import ConfigError

# ======================================================================
# Basic get / set / reset lifecycle
# ======================================================================


class TestConfigGetSetReset:
    """Test basic config get, set, and reset operations."""

    def test_get_returns_none_before_override(self) -> None:
        """Getting a key that has never been overridden returns None."""
        result = ConfigOperation.get(category="settings.vm", key="max_vms")
        assert result is None

    def test_set_and_get(self) -> None:
        """Setting a value persists and get returns the new value."""
        ConfigOperation.set(category="settings.vm", key="max_vms", value=50)
        result = ConfigOperation.get(category="settings.vm", key="max_vms")
        assert result == 50

    def test_reset_reverts_to_none(self) -> None:
        """Resetting a key removes the override so get returns None."""
        ConfigOperation.set(category="settings.vm", key="max_vms", value=50)
        ConfigOperation.reset(category="settings.vm", key="max_vms")
        result = ConfigOperation.get(category="settings.vm", key="max_vms")
        assert result is None

    def test_get_category(self) -> None:
        """Getting a category without a key returns all keys with metadata."""
        result = ConfigOperation.get(category="settings.vm")
        assert isinstance(result, dict)
        assert "max_vms" in result
        assert "log_lines" in result
        assert "log_follow" in result
        for key_info in result.values():
            assert "type" in key_info
            assert "override" in key_info
            assert "default" in key_info
        assert result["max_vms"]["default"] == 1000
        assert result["max_vms"]["type"] == "int"
        assert result["log_follow"]["default"] is False
        assert result["log_follow"]["type"] == "bool"


# ======================================================================
# list_all
# ======================================================================


class TestConfigListAll:
    """Test listing all config settings."""

    def test_list_all_returns_categories_and_keys(self) -> None:
        """list_all returns every overridable category and key."""
        result = ConfigOperation.list_all()
        assert isinstance(result, dict)
        assert "settings.vm" in result
        assert "defaults.vm" in result
        assert "defaults.network" in result
        assert "defaults.image" in result
        assert "defaults.kernel" in result
        assert "defaults.firecracker" in result
        assert "defaults.cloudinit" in result
        assert "defaults.binary" in result
        # Verify key-level structure
        assert "max_vms" in result["settings.vm"]
        assert "vcpu_count" in result["defaults.vm"]
        key_info = result["settings.vm"]["max_vms"]
        assert "type" in key_info
        assert "override" in key_info
        assert key_info["type"] == "int"
        assert key_info["override"] is None

    def test_list_all_shows_override(self) -> None:
        """list_all reflects the current override value."""
        ConfigOperation.set(category="settings.vm", key="max_vms", value=75)
        result = ConfigOperation.list_all()
        assert result["settings.vm"]["max_vms"]["override"] == 75


# ======================================================================
# Edge cases
# ======================================================================


class TestConfigEdgeCases:
    """Test edge cases and error handling in the Config API."""

    def test_get_nonexistent_key_returns_none(self) -> None:
        """Getting a key that does not exist in the schema returns None."""
        result = ConfigOperation.get(
            category="settings.vm", key="nonexistent_key"
        )
        assert result is None

    def test_set_invalid_value_type_raises(self) -> None:
        """Setting a value that cannot be coerced to the expected type raises."""
        with pytest.raises((ValueError, TypeError)):
            ConfigOperation.set(
                category="settings.vm",
                key="max_vms",
                value="not_an_int",
            )

    def test_reset_unset_valid_key_does_not_raise(self) -> None:
        """Resetting a valid key that was never set returns 0 and does not raise."""
        result = ConfigOperation.reset(category="settings.vm", key="max_vms")
        assert result == 0

    def test_reset_nonexistent_key_raises_config_error(self) -> None:
        """Resetting a key that does not exist in the schema raises ConfigError."""
        with pytest.raises(ConfigError):
            ConfigOperation.reset(category="settings.vm", key="nonexistent_key")

    def test_reset_all_overrides(self) -> None:
        """Resetting all overrides removes every custom value."""
        ConfigOperation.set(category="settings.vm", key="max_vms", value=50)
        ConfigOperation.set(category="settings.vm", key="log_lines", value=100)
        ConfigOperation.set(category="defaults.vm", key="vcpu_count", value=4)

        deleted = ConfigOperation.reset(all_overrides=True)
        assert deleted == 3

        assert (
            ConfigOperation.get(category="settings.vm", key="max_vms") is None
        )
        assert (
            ConfigOperation.get(category="settings.vm", key="log_lines") is None
        )
        assert (
            ConfigOperation.get(category="defaults.vm", key="vcpu_count")
            is None
        )

    def test_reset_category_removes_all_in_category(self) -> None:
        """Resetting a category removes only overrides in that category."""
        ConfigOperation.set(category="settings.vm", key="max_vms", value=50)
        ConfigOperation.set(category="settings.vm", key="log_lines", value=100)
        ConfigOperation.set(category="defaults.vm", key="vcpu_count", value=4)

        deleted = ConfigOperation.reset(category="settings.vm")
        assert deleted == 2

        assert (
            ConfigOperation.get(category="settings.vm", key="max_vms") is None
        )
        assert (
            ConfigOperation.get(category="settings.vm", key="log_lines") is None
        )
        # Other category should remain intact
        assert (
            ConfigOperation.get(category="defaults.vm", key="vcpu_count") == 4
        )

    def test_get_invalid_category_raises_config_error(self) -> None:
        """Getting an invalid category raises ConfigError."""
        with pytest.raises(ConfigError):
            ConfigOperation.get(category="invalid.category")

    def test_set_nonexistent_key_raises_config_error(self) -> None:
        """Setting a non-overridable key raises ConfigError."""
        with pytest.raises(ConfigError):
            ConfigOperation.set(
                category="settings.vm",
                key="nonexistent",
                value=42,
            )

    def test_set_nonexistent_category_raises_config_error(self) -> None:
        """Setting a key in a non-overridable category raises ConfigError."""
        with pytest.raises(ConfigError):
            ConfigOperation.set(
                category="invalid.category",
                key="foo",
                value=42,
            )
