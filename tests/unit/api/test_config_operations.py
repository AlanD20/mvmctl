"""Tests for ConfigOperation — API layer user settings orchestration."""

from __future__ import annotations

from unittest.mock import patch

import pytest

from mvmctl.api.config_operations import ConfigOperation
from mvmctl.exceptions import ConfigError


@pytest.fixture(autouse=True)
def _mock_audit_log():
    """Prevent audit log writes during tests."""
    with patch("mvmctl.api.config_operations.AuditLog.log"):
        yield


class TestConfigOperationGet:
    """Tests for ConfigOperation.get()."""

    def test_get_existing_override(self) -> None:
        """get() returns a value that was previously set."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 4)
        result = ConfigOperation.get("defaults.vm", "vcpu_count")
        assert result == 4

    def test_get_none_for_missing(self) -> None:
        """get() returns None when no override exists."""
        result = ConfigOperation.get("defaults.vm", "vcpu_count")
        assert result is None

    def test_get_by_category_returns_dict(self) -> None:
        """get() without key returns dict for the category."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        ConfigOperation.set("defaults.vm", "mem_size_mib", 2048)
        result = ConfigOperation.get("defaults.vm")
        assert isinstance(result, dict)
        assert result["vcpu_count"]["override"] == 8
        assert result["mem_size_mib"]["override"] == 2048

    def test_get_returns_coerced_value(self) -> None:
        """get() returns values with proper type coercion."""
        ConfigOperation.set("defaults.vm", "enable_pci", True)
        result = ConfigOperation.get("defaults.vm", "enable_pci")
        assert result is True
        assert isinstance(result, bool)


class TestConfigOperationSet:
    """Tests for ConfigOperation.set()."""

    def test_set_and_get_string(self) -> None:
        """Setting a string value and retrieving works."""
        ConfigOperation.set("defaults.vm", "ssh_user", "admin")
        result = ConfigOperation.get("defaults.vm", "ssh_user")
        assert result == "admin"

    def test_set_and_get_int(self) -> None:
        """Setting an int value and retrieving works."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        result = ConfigOperation.get("defaults.vm", "vcpu_count")
        assert result == 8

    def test_set_and_get_bool(self) -> None:
        """Setting a bool value and retrieving works."""
        ConfigOperation.set("defaults.vm", "enable_pci", True)
        result = ConfigOperation.get("defaults.vm", "enable_pci")
        assert result is True

    def test_set_overwrites_existing(self) -> None:
        """Setting the same key twice overwrites the first value."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 2)
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        result = ConfigOperation.get("defaults.vm", "vcpu_count")
        assert result == 8

    def test_set_invalid_key_raises(self) -> None:
        """Setting a non-overridable key raises ConfigError."""
        with pytest.raises(ConfigError, match="not an overridable setting"):
            ConfigOperation.set("defaults.vm", "nonexistent", "value")

    def test_set_wrong_type_raises(self) -> None:
        """Setting a key with wrong type raises exception."""
        with pytest.raises((TypeError, ValueError)):
            ConfigOperation.set("defaults.vm", "vcpu_count", "not-a-number")

    def test_set_invalid_mac_prefix_raises(self) -> None:
        """Setting invalid MAC prefix raises ConfigError (constraint)."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            ConfigOperation.set("defaults.vm", "guest_mac_prefix", "invalid")

    def test_set_invalid_nocloud_range_raises(self) -> None:
        """Setting invalid nocloud port range raises ConfigError (constraint)."""
        # Defaults: start=8000, end=9000. Set start to 5000 (OK, end=9000 > 5000).
        ConfigOperation.set(
            "defaults.cloudinit", "nocloud_port_range_start", 5000
        )
        # Now end=9000 (default) > start=5000. Setting end=3000 makes end < start.
        with pytest.raises(ConfigError, match="nocloud_port_range_end"):
            ConfigOperation.set(
                "defaults.cloudinit", "nocloud_port_range_end", 3000
            )


class TestConfigOperationReset:
    """Tests for ConfigOperation.reset()."""

    def test_reset_single_key(self) -> None:
        """reset() with key removes the override for that key."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        result = ConfigOperation.reset("defaults.vm", "vcpu_count")
        assert result.item == 1
        assert ConfigOperation.get("defaults.vm", "vcpu_count") is None

    def test_reset_category(self) -> None:
        """reset() with category removes all overrides in that category."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        ConfigOperation.set("defaults.vm", "mem_size_mib", 1024)
        result = ConfigOperation.reset("defaults.vm")
        assert result.item == 2
        assert ConfigOperation.get("defaults.vm", "vcpu_count") is None
        assert ConfigOperation.get("defaults.vm", "mem_size_mib") is None

    def test_reset_all_overrides(self) -> None:
        """reset() with all_overrides removes every override."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        ConfigOperation.set("defaults.network", "name", "test")
        result = ConfigOperation.reset(all_overrides=True)
        assert result.item == 2
        assert ConfigOperation.get("defaults.vm", "vcpu_count") is None
        assert ConfigOperation.get("defaults.network", "name") is None

    def test_reset_missing_key_returns_zero(self) -> None:
        """reset() returns 0 when no override exists for the key."""
        result = ConfigOperation.reset("defaults.vm", "vcpu_count")
        assert result.item == 0

    def test_reset_preserves_other_categories(self) -> None:
        """reset() for one category does not affect other categories."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        ConfigOperation.set("defaults.network", "name", "test")
        ConfigOperation.reset("defaults.vm")
        assert ConfigOperation.get("defaults.network", "name") == "test"


class TestConfigOperationList:
    """Tests for ConfigOperation.list_all()."""

    def test_list_all_returns_categories(self) -> None:
        """list_all() returns all overridable categories."""
        result = ConfigOperation.list_all()
        assert "defaults.vm" in result
        assert "defaults.network" in result
        assert "defaults.cloudinit" in result

    def test_list_all_shows_overrides(self) -> None:
        """list_all() shows current overrides."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 16)
        result = ConfigOperation.list_all()
        assert result["defaults.vm"]["vcpu_count"]["override"] == 16

    def test_list_all_no_overrides(self) -> None:
        """list_all() shows None for overrides when none are set."""
        result = ConfigOperation.list_all()
        assert result["defaults.vm"]["vcpu_count"]["override"] is None


class TestConfigOperationFullWorkflow:
    """End-to-end workflow tests for ConfigOperation."""

    def test_set_get_reset_get(self) -> None:
        """Full cycle: set, verify, reset, verify gone."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 8)
        assert ConfigOperation.get("defaults.vm", "vcpu_count") == 8
        ConfigOperation.reset("defaults.vm", "vcpu_count")
        assert ConfigOperation.get("defaults.vm", "vcpu_count") is None

    def test_multiple_overrides_different_categories(self) -> None:
        """Multiple overrides across categories work independently."""
        ConfigOperation.set("defaults.vm", "vcpu_count", 4)
        ConfigOperation.set("defaults.network", "name", "custom")
        ConfigOperation.set("defaults.cloudinit", "iso_name", "custom.iso")

        assert ConfigOperation.get("defaults.vm", "vcpu_count") == 4
        assert ConfigOperation.get("defaults.network", "name") == "custom"
        assert (
            ConfigOperation.get("defaults.cloudinit", "iso_name")
            == "custom.iso"
        )

    def test_type_coercion_through_api(self) -> None:
        """Type coercion works through the API layer."""
        ConfigOperation.set("defaults.vm", "enable_pci", "true")
        result = ConfigOperation.get("defaults.vm", "enable_pci")
        assert result is True

    def test_bool_false_persists(self) -> None:
        """False boolean values persist correctly."""
        ConfigOperation.set("defaults.vm", "enable_pci", False)
        result = ConfigOperation.get("defaults.vm", "enable_pci")
        assert result is False

    def test_integer_zero_persists(self) -> None:
        """Integer 0 persists (not confused with None)."""
        ConfigOperation.set("defaults.vm", "root_uid", 0)
        result = ConfigOperation.get("defaults.vm", "root_uid")
        assert result == 0
        assert result is not None
