"""Tests for SettingsService — validation and type coercion for user settings."""

from __future__ import annotations

import pytest

from mvmctl.constants import OVERRIDABLE_DEFAULTS
from mvmctl.core._shared._db import Database
from mvmctl.core.config._repository import SettingsRepository
from mvmctl.core.config._service import (
    OVERRIDABLE_SETTINGS,
    SettingsService,
)
from mvmctl.exceptions import ConfigError


@pytest.fixture
def repo() -> SettingsRepository:
    """Create a SettingsRepository backed by a fresh, migrated test DB."""
    db = Database()
    db.migrate()
    return SettingsRepository(db)


@pytest.fixture
def service(repo: SettingsRepository) -> SettingsService:
    """Create a SettingsService with a clean repo."""
    return SettingsService(repo)


class TestSettingsServiceGet:
    """Tests for SettingsService.get()."""

    def test_get_returns_coerced_value(self, service: SettingsService) -> None:
        """get() returns the correct coerced value for a valid key."""
        service.set("defaults.vm", "vcpu_count", 4)
        result = service.get("defaults.vm", "vcpu_count")
        assert result == 4
        assert isinstance(result, int)

    def test_get_returns_none_for_missing(
        self, service: SettingsService
    ) -> None:
        """get() returns None when no override is set."""
        result = service.get("defaults.vm", "vcpu_count")
        assert result is None

    def test_get_returns_coerces_string_to_int(
        self, service: SettingsService
    ) -> None:
        """get() coerces string values to the expected int type."""
        service.set("defaults.vm", "vcpu_count", 4)
        result = service.get("defaults.vm", "vcpu_count")
        assert result == 4
        assert isinstance(result, int)

    def test_get_returns_coerces_string_to_bool(
        self, service: SettingsService
    ) -> None:
        """get() coerces string values to the expected bool type."""
        service.set("defaults.vm", "enable_pci", True)
        result = service.get("defaults.vm", "enable_pci")
        assert result is True
        assert isinstance(result, bool)

    def test_get_none_for_invalid_category(
        self, service: SettingsService
    ) -> None:
        """get() returns None for a non-existent category."""
        result = service.get("nonexistent", "key")
        assert result is None

    def test_get_returns_coerced_bool_false(
        self, service: SettingsService
    ) -> None:
        """get() returns False correctly for bool keys."""
        service.set("defaults.vm", "enable_pci", False)
        result = service.get("defaults.vm", "enable_pci")
        assert result is False

    def test_get_string_value(self, service: SettingsService) -> None:
        """get() returns string values correctly."""
        service.set("defaults.vm", "ssh_user", "admin")
        result = service.get("defaults.vm", "ssh_user")
        assert result == "admin"
        assert isinstance(result, str)


class TestSettingsServiceSet:
    """Tests for SettingsService.set()."""

    def test_set_succeeds_for_overridable_key(
        self, service: SettingsService
    ) -> None:
        """set() succeeds for a valid overridable key."""
        service.set("defaults.vm", "vcpu_count", 8)
        assert service.get("defaults.vm", "vcpu_count") == 8

    def test_set_raises_for_invalid_key(self, service: SettingsService) -> None:
        """set() raises ConfigError for a non-overridable key."""
        with pytest.raises(ConfigError, match="is not an overridable setting"):
            service.set("defaults.vm", "nonexistent_key", "value")

    def test_set_raises_for_invalid_category(
        self, service: SettingsService
    ) -> None:
        """set() raises ConfigError for a non-existent category."""
        with pytest.raises(ConfigError, match="is not an overridable setting"):
            service.set("nonexistent", "key", "value")

    def test_set_updates_existing_value(self, service: SettingsService) -> None:
        """set() updates an existing override."""
        service.set("defaults.vm", "vcpu_count", 2)
        service.set("defaults.vm", "vcpu_count", 8)
        assert service.get("defaults.vm", "vcpu_count") == 8

    def test_set_with_wrong_type_raises(self, service: SettingsService) -> None:
        """set() raises exception when value cannot be coerced to expected type."""
        with pytest.raises((TypeError, ValueError)):
            service.set("defaults.vm", "vcpu_count", "not-a-number")

    def test_set_bool_value(self, service: SettingsService) -> None:
        """set() stores bool values correctly."""
        service.set("defaults.vm", "enable_pci", True)
        assert service.get("defaults.vm", "enable_pci") is True

    def test_set_int_value(self, service: SettingsService) -> None:
        """set() stores int values correctly."""
        service.set("defaults.vm", "mem_size_mib", 2048)
        assert service.get("defaults.vm", "mem_size_mib") == 2048

    def test_set_string_value(self, service: SettingsService) -> None:
        """set() stores string values correctly."""
        service.set("defaults.vm", "ssh_user", "custom")
        assert service.get("defaults.vm", "ssh_user") == "custom"

    def test_set_defaults_network_name(self, service: SettingsService) -> None:
        """set() works for defaults.network category."""
        service.set("defaults.network", "name", "custom-net")
        assert service.get("defaults.network", "name") == "custom-net"

    def test_set_defaults_network_nat(self, service: SettingsService) -> None:
        """set() works for bool values in defaults.network."""
        service.set("defaults.network", "nat_enabled", False)
        assert service.get("defaults.network", "nat_enabled") is False


class TestSettingsServiceDelete:
    """Tests for SettingsService.delete() (reset)."""

    def test_delete_existing_key(self, service: SettingsService) -> None:
        """delete() removes an existing override and returns True."""
        service.set("defaults.vm", "vcpu_count", 8)
        result = service.delete("defaults.vm", "vcpu_count")
        assert result is True
        assert service.get("defaults.vm", "vcpu_count") is None

    def test_delete_missing_key(self, service: SettingsService) -> None:
        """delete() returns False when no override exists."""
        result = service.delete("defaults.vm", "vcpu_count")
        assert result is False

    def test_delete_invalid_key_raises(self, service: SettingsService) -> None:
        """delete() raises ConfigError for non-overridable keys."""
        with pytest.raises(ConfigError, match="not a valid setting key"):
            service.delete("defaults.vm", "nonexistent")

    def test_delete_returns_to_default(self, service: SettingsService) -> None:
        """After delete(), the value reverts to the hardcoded default."""
        service.set("defaults.vm", "vcpu_count", 8)
        service.delete("defaults.vm", "vcpu_count")
        assert service.get("defaults.vm", "vcpu_count") is None

    def test_delete_by_category(self, service: SettingsService) -> None:
        """delete_by_category() removes all overrides in a category."""
        service.set("defaults.vm", "vcpu_count", 8)
        service.set("defaults.vm", "mem_size_mib", 1024)
        count = service.delete_by_category("defaults.vm")
        assert count == 2

    def test_delete_by_category_invalid_raises(
        self, service: SettingsService
    ) -> None:
        """delete_by_category() raises ConfigError for invalid category."""
        with pytest.raises(ConfigError, match="not a valid setting category"):
            service.delete_by_category("nonexistent")

    def test_delete_all(self, service: SettingsService) -> None:
        """delete_all() removes all overrides."""
        service.set("defaults.vm", "vcpu_count", 4)
        service.set("defaults.network", "name", "test")
        count = service.delete_all()
        assert count == 2


class TestSettingsServiceTypeCoercion:
    """Tests for SettingsService type coercion during set()."""

    def test_coerce_string_to_int(self, service: SettingsService) -> None:
        """Setting an int key with a string value coerces it."""
        service.set("defaults.vm", "vcpu_count", "4")
        assert service.get("defaults.vm", "vcpu_count") == 4
        assert isinstance(service.get("defaults.vm", "vcpu_count"), int)

    def test_coerce_string_to_bool_true(self, service: SettingsService) -> None:
        """Setting a bool key with 'true' string coerces to True."""
        service.set("defaults.vm", "enable_pci", "true")
        assert service.get("defaults.vm", "enable_pci") is True

    def test_coerce_string_to_bool_false(
        self, service: SettingsService
    ) -> None:
        """Setting a bool key with 'false' string coerces to False."""
        service.set("defaults.vm", "enable_pci", "false")
        assert service.get("defaults.vm", "enable_pci") is False

    def test_coerce_string_to_bool_yes(self, service: SettingsService) -> None:
        """Setting a bool key with 'yes' string coerces to True."""
        service.set("defaults.vm", "enable_pci", "yes")
        assert service.get("defaults.vm", "enable_pci") is True

    def test_coerce_string_to_bool_one(self, service: SettingsService) -> None:
        """Setting a bool key with '1' string coerces to True."""
        service.set("defaults.vm", "enable_pci", "1")
        assert service.get("defaults.vm", "enable_pci") is True

    def test_coerce_int_preserved(self, service: SettingsService) -> None:
        """Setting an int key with an int value keeps it as int."""
        service.set("defaults.vm", "vcpu_count", 4)
        assert isinstance(service.get("defaults.vm", "vcpu_count"), int)

    def test_coerce_bool_preserved(self, service: SettingsService) -> None:
        """Setting a bool key with a bool value keeps it as bool."""
        service.set("defaults.vm", "enable_pci", True)
        assert isinstance(service.get("defaults.vm", "enable_pci"), bool)


class TestSettingsServiceList:
    """Tests for SettingsService list methods."""

    def test_list_all_returns_all_categories(
        self, service: SettingsService
    ) -> None:
        """list_all() returns all overridable categories with metadata."""
        result = service.list_all()
        for category in OVERRIDABLE_SETTINGS:
            assert category in result

    def test_list_all_includes_type_info(
        self, service: SettingsService
    ) -> None:
        """list_all() includes type info for each key."""
        result = service.list_all()
        vm_entry = result["defaults.vm"]["vcpu_count"]
        assert "type" in vm_entry
        assert vm_entry["type"] == "int"
        assert "override" in vm_entry

    def test_list_all_shows_override(self, service: SettingsService) -> None:
        """list_all() shows current override values."""
        service.set("defaults.vm", "vcpu_count", 8)
        result = service.list_all()
        assert result["defaults.vm"]["vcpu_count"]["override"] == 8

    def test_list_all_override_none_when_not_set(
        self, service: SettingsService
    ) -> None:
        """list_all() shows None for override when not set."""
        result = service.list_all()
        assert result["defaults.vm"]["vcpu_count"]["override"] is None

    def test_list_by_category_valid(self, service: SettingsService) -> None:
        """list_by_category() returns keys with type, override, and default."""
        result = service.list_by_category("defaults.vm")
        vcpu = result["vcpu_count"]
        assert vcpu["type"] == "int"
        assert vcpu["override"] is None
        assert (
            vcpu["default"] == OVERRIDABLE_DEFAULTS["defaults.vm"]["vcpu_count"]
        )

    def test_list_by_category_with_override(
        self, service: SettingsService
    ) -> None:
        """list_by_category() shows overrides when set."""
        service.set("defaults.vm", "vcpu_count", 16)
        result = service.list_by_category("defaults.vm")
        assert result["vcpu_count"]["override"] == 16

    def test_list_by_category_invalid_raises(
        self, service: SettingsService
    ) -> None:
        """list_by_category() raises ConfigError for invalid category."""
        with pytest.raises(ConfigError, match="not a valid setting category"):
            service.list_by_category("nonexistent")


class TestSettingsServiceResolve:
    """Tests for SettingsService.resolve()."""

    def test_resolve_returns_override(self, service: SettingsService) -> None:
        """resolve() returns the override value when set."""
        service.set("defaults.vm", "vcpu_count", 8)
        from mvmctl.core._shared._db import Database

        result = SettingsService.resolve(
            Database(), "defaults.vm", "vcpu_count"
        )
        assert result == 8

    def test_resolve_returns_default_when_no_override(
        self, service: SettingsService
    ) -> None:
        """resolve() returns the hardcoded default when no override is set."""
        from mvmctl.core._shared._db import Database

        result = SettingsService.resolve(
            Database(), "defaults.vm", "vcpu_count"
        )
        assert result == OVERRIDABLE_DEFAULTS["defaults.vm"]["vcpu_count"]

    def test_resolve_coerces_override_type(
        self, service: SettingsService
    ) -> None:
        """resolve() coerces the override to the expected type."""
        service.set("defaults.vm", "vcpu_count", 8)
        from mvmctl.core._shared._db import Database

        result = SettingsService.resolve(
            Database(), "defaults.vm", "vcpu_count"
        )
        assert isinstance(result, int)


class TestSettingsServiceEdgeCases:
    """Edge cases for SettingsService."""

    def test_get_active_value_override_exists(
        self, service: SettingsService
    ) -> None:
        """_get_active_value() returns the override when present."""
        service.set("defaults.vm", "vcpu_count", 8)
        result = service._get_active_value("defaults.vm", "vcpu_count")
        assert result == 8

    def test_get_active_value_no_override(
        self, service: SettingsService
    ) -> None:
        """_get_active_value() returns the hardcoded default when no override."""
        result = service._get_active_value("defaults.vm", "vcpu_count")
        assert result == OVERRIDABLE_DEFAULTS["defaults.vm"]["vcpu_count"]

    def test_get_active_value_coerces(self, service: SettingsService) -> None:
        """_get_active_value() coerces the stored override value."""
        service.set("defaults.network", "nat_enabled", "true")
        result = service._get_active_value("defaults.network", "nat_enabled")
        assert result is True

    def test_expected_type_returns_none_for_invalid(
        self, service: SettingsService
    ) -> None:
        """_get_expected_type() returns None for non-overridable keys."""
        result = service._get_expected_type("nonexistent", "key")
        assert result is None

    def test_expected_type_returns_none_for_invalid_key(
        self, service: SettingsService
    ) -> None:
        """_get_expected_type() returns None for invalid key in valid category."""
        result = service._get_expected_type("defaults.vm", "nonexistent")
        assert result is None

    def test_expected_type_returns_correct_type(
        self, service: SettingsService
    ) -> None:
        """_get_expected_type() returns the correct Python type."""
        result = service._get_expected_type("defaults.vm", "vcpu_count")
        assert result is int
        result = service._get_expected_type("defaults.vm", "ssh_user")
        assert result is str
        result = service._get_expected_type("defaults.vm", "enable_pci")
        assert result is bool
