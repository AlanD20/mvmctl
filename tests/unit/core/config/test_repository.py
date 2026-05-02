"""Tests for SettingsRepository — database operations for user_settings table."""

from __future__ import annotations

from typing import Any

import pytest

from mvmctl.core._shared._db import Database
from mvmctl.core.config._repository import SettingsRepository


@pytest.fixture
def repo() -> SettingsRepository:
    """Create a SettingsRepository backed by a fresh, migrated test DB."""
    db = Database()
    db.migrate()
    return SettingsRepository(db)


class TestSettingsRepositoryGet:
    """Tests for SettingsRepository.get()."""

    def test_get_returns_parsed_json(self, repo: SettingsRepository) -> None:
        """get() returns parsed JSON values."""
        repo.set("defaults.vm", "vcpu_count", 4)
        result = repo.get("defaults.vm", "vcpu_count")
        assert result == 4
        assert isinstance(result, int)

    def test_get_returns_none_for_missing_key(
        self, repo: SettingsRepository
    ) -> None:
        """get() returns None when the key does not exist."""
        result = repo.get("defaults.vm", "nonexistent")
        assert result is None

    def test_get_returns_none_for_missing_category(
        self, repo: SettingsRepository
    ) -> None:
        """get() returns None when the category does not exist."""
        result = repo.get("nonexistent.category", "key")
        assert result is None

    def test_get_string_value(self, repo: SettingsRepository) -> None:
        """get() returns string values correctly."""
        repo.set("defaults.vm", "ssh_user", "testuser")
        result = repo.get("defaults.vm", "ssh_user")
        assert result == "testuser"
        assert isinstance(result, str)

    def test_get_bool_value(self, repo: SettingsRepository) -> None:
        """get() returns boolean values correctly."""
        repo.set("defaults.vm", "enable_pci", True)
        result = repo.get("defaults.vm", "enable_pci")
        assert result is True
        assert isinstance(result, bool)

    def test_get_list_value(self, repo: SettingsRepository) -> None:
        """get() returns list values correctly (JSON roundtrip)."""
        repo.set("defaults.vm", "boot_args_list", ["arg1", "arg2"])
        result = repo.get("defaults.vm", "boot_args_list")
        assert result == ["arg1", "arg2"]
        assert isinstance(result, list)

    def test_get_dict_value(self, repo: SettingsRepository) -> None:
        """get() returns dict values correctly (JSON roundtrip)."""
        repo.set("defaults.vm", "metadata", {"key": "value", "count": 42})
        result = repo.get("defaults.vm", "metadata")
        assert result == {"key": "value", "count": 42}
        assert isinstance(result, dict)

    def test_get_integer_zero(self, repo: SettingsRepository) -> None:
        """get() returns integer 0 (falsy) correctly, not None."""
        repo.set("defaults.vm", "root_uid", 0)
        result = repo.get("defaults.vm", "root_uid")
        assert result == 0
        assert result is not None

    def test_get_float_value(self, repo: SettingsRepository) -> None:
        """get() returns float values correctly."""
        repo.set("defaults.image", "compression_ratio", 1.5)
        result = repo.get("defaults.image", "compression_ratio")
        assert result == 1.5
        assert isinstance(result, float)


class TestSettingsRepositorySet:
    """Tests for SettingsRepository.set()."""

    def test_set_inserts_new_row(self, repo: SettingsRepository) -> None:
        """set() inserts a new setting."""
        repo.set("defaults.vm", "vcpu_count", 2)
        result = repo.get("defaults.vm", "vcpu_count")
        assert result == 2

    def test_set_updates_existing_row(self, repo: SettingsRepository) -> None:
        """set() updates an existing setting."""
        repo.set("defaults.vm", "vcpu_count", 2)
        repo.set("defaults.vm", "vcpu_count", 8)
        result = repo.get("defaults.vm", "vcpu_count")
        assert result == 8

    def test_set_multiple_categories(self, repo: SettingsRepository) -> None:
        """set() isolates values across different categories."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.network", "name", "test-net")
        assert repo.get("defaults.vm", "vcpu_count") == 4
        assert repo.get("defaults.network", "name") == "test-net"

    def test_set_bool_false(self, repo: SettingsRepository) -> None:
        """set() stores False boolean correctly."""
        repo.set("defaults.vm", "enable_pci", False)
        result = repo.get("defaults.vm", "enable_pci")
        assert result is False

    def test_set_none_value(self, repo: SettingsRepository) -> None:
        """set() stores None as JSON null."""
        repo.set("defaults.vm", "some_key", None)
        result = repo.get("defaults.vm", "some_key")
        assert result is None

    def test_set_string_with_special_chars(
        self, repo: SettingsRepository
    ) -> None:
        """set() handles strings with special characters."""
        special = 'hello\nworld\ttab\\"quotes"'
        repo.set("defaults.vm", "boot_args", special)
        result = repo.get("defaults.vm", "boot_args")
        assert result == special


class TestSettingsRepositoryDelete:
    """Tests for SettingsRepository.delete()."""

    def test_delete_existing_returns_true(
        self, repo: SettingsRepository
    ) -> None:
        """delete() returns True when a row is deleted."""
        repo.set("defaults.vm", "vcpu_count", 4)
        result = repo.delete("defaults.vm", "vcpu_count")
        assert result is True

    def test_delete_missing_returns_false(
        self, repo: SettingsRepository
    ) -> None:
        """delete() returns False when no row exists."""
        result = repo.delete("defaults.vm", "nonexistent")
        assert result is False

    def test_delete_removes_value(self, repo: SettingsRepository) -> None:
        """After delete(), get() returns None."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.delete("defaults.vm", "vcpu_count")
        assert repo.get("defaults.vm", "vcpu_count") is None

    def test_delete_only_removes_specific_key(
        self, repo: SettingsRepository
    ) -> None:
        """delete() only removes the specified key, not others."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.vm", "mem_size_mib", 1024)
        repo.delete("defaults.vm", "vcpu_count")
        assert repo.get("defaults.vm", "vcpu_count") is None
        assert repo.get("defaults.vm", "mem_size_mib") == 1024


class TestSettingsRepositoryListAll:
    """Tests for SettingsRepository.list_by_category()."""

    def test_list_all_empty(self, repo: SettingsRepository) -> None:
        """list_by_category() with no args returns empty dict when no settings exist."""
        result = repo.list_by_category()
        assert result == {}

    def test_list_all_with_settings(self, repo: SettingsRepository) -> None:
        """list_by_category() returns all settings in nested dict."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.network", "name", "test-net")
        result = repo.list_by_category()
        assert result["defaults.vm"]["vcpu_count"] == 4
        assert result["defaults.network"]["name"] == "test-net"

    def test_list_all_multiple_keys_per_category(
        self, repo: SettingsRepository
    ) -> None:
        """list_by_category() returns all keys within a category."""
        repo.set("defaults.vm", "vcpu_count", 2)
        repo.set("defaults.vm", "mem_size_mib", 1024)
        result = repo.list_by_category()
        vm_settings = result["defaults.vm"]
        assert vm_settings["vcpu_count"] == 2
        assert vm_settings["mem_size_mib"] == 1024

    def test_list_by_category_filter(self, repo: SettingsRepository) -> None:
        """list_by_category(category) returns only settings in that category."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.network", "name", "test-net")
        result = repo.list_by_category("defaults.vm")
        assert "defaults.vm" in result
        assert "defaults.network" not in result

    def test_list_by_category_empty(self, repo: SettingsRepository) -> None:
        """list_by_category(category) returns empty dict when category has no settings."""
        result = repo.list_by_category("defaults.vm")
        assert result == {}

    def test_list_all_returns_latest_values(
        self, repo: SettingsRepository
    ) -> None:
        """list_by_category() returns the most recent values after updates."""
        repo.set("defaults.vm", "vcpu_count", 2)
        repo.set("defaults.vm", "vcpu_count", 8)
        result = repo.list_by_category()
        assert result["defaults.vm"]["vcpu_count"] == 8


class TestSettingsRepositoryDeleteByCategory:
    """Tests for SettingsRepository.delete_by_category()."""

    def test_delete_by_category_existing(
        self, repo: SettingsRepository
    ) -> None:
        """delete_by_category() returns the number of deleted rows."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.vm", "mem_size_mib", 1024)
        count = repo.delete_by_category("defaults.vm")
        assert count == 2

    def test_delete_by_category_empty(self, repo: SettingsRepository) -> None:
        """delete_by_category() returns 0 when category has no settings."""
        count = repo.delete_by_category("defaults.vm")
        assert count == 0

    def test_delete_by_category_only_removes_target(
        self, repo: SettingsRepository
    ) -> None:
        """delete_by_category() only removes settings in the specified category."""
        repo.set("defaults.vm", "vcpu_count", 4)
        repo.set("defaults.network", "name", "test-net")
        repo.delete_by_category("defaults.vm")
        assert repo.get("defaults.vm", "vcpu_count") is None
        assert repo.get("defaults.network", "name") == "test-net"


class TestSettingsRepositoryDeleteAll:
    """Tests for SettingsRepository.delete_all()."""

    def test_delete_all_returns_count(self, repo: SettingsRepository) -> None:
        """delete_all() returns the total number of deleted rows."""
        repo.set("a", "k1", 1)
        repo.set("b", "k2", 2)
        repo.set("c", "k3", 3)
        count = repo.delete_all()
        assert count == 3

    def test_delete_all_empty(self, repo: SettingsRepository) -> None:
        """delete_all() returns 0 when no settings exist."""
        count = repo.delete_all()
        assert count == 0

    def test_delete_all_removes_everything(
        self, repo: SettingsRepository
    ) -> None:
        """After delete_all(), list_by_category() returns empty."""
        repo.set("a", "k1", 1)
        repo.set("b", "k2", 2)
        repo.delete_all()
        assert repo.list_by_category() == {}


class TestSettingsRepositoryTypes:
    """Tests for SettingsRepository with various JSON types."""

    @pytest.mark.parametrize(
        ("value", "expected_type"),
        [
            (42, int),
            ("hello", str),
            (True, bool),
            (False, bool),
            (3.14, float),
            ([1, 2, 3], list),
            ({"a": 1}, dict),
            (None, type(None)),
        ],
    )
    def test_roundtrip_all_types(
        self,
        repo: SettingsRepository,
        value: Any,
        expected_type: type,
    ) -> None:
        """All supported JSON types survive a get/set roundtrip."""
        repo.set("test.cat", "test_key", value)
        result = repo.get("test.cat", "test_key")
        assert result == value
        assert isinstance(result, expected_type)

    def test_nested_dict(self, repo: SettingsRepository) -> None:
        """Deeply nested dicts roundtrip correctly."""
        nested = {"level1": {"level2": {"level3": "deep"}}}
        repo.set("test", "nested", nested)
        result = repo.get("test", "nested")
        assert result == nested

    def test_mixed_list(self, repo: SettingsRepository) -> None:
        """Lists with mixed types roundtrip correctly."""
        mixed = [1, "two", 3.0, True, None]
        repo.set("test", "mixed", mixed)
        result = repo.get("test", "mixed")
        assert result == mixed
