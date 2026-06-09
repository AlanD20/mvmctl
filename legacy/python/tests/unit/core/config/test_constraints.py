"""Tests for constraint validation — cross-key constraints for overridable settings."""

from __future__ import annotations

import pytest

from mvmctl.core._shared._db import Database
from mvmctl.core.config._constraints import ConstraintRegistry, constraints
from mvmctl.core.config._repository import SettingsRepository
from mvmctl.core.config._service import SettingsService
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


class TestConstraintRegistry:
    """Tests for ConstraintRegistry itself."""

    def test_register_and_get(self) -> None:
        """A registered constraint can be retrieved."""
        reg = ConstraintRegistry()

        def dummy(_key: str, _resolve: object) -> None:
            pass

        reg.register("test.cat", frozenset({"key1"}), dummy)
        result = reg.get("test.cat", "key1")
        assert len(result) == 1
        assert result[0] is dummy

    def test_get_returns_empty_for_unregistered(
        self,
    ) -> None:
        """get() returns empty list for unregistered (category, key)."""
        reg = ConstraintRegistry()
        result = reg.get("nonexistent", "key")
        assert result == []

    def test_register_multiple_keys_same_constraint(
        self,
    ) -> None:
        """A constraint registered for multiple keys fires for each."""
        reg = ConstraintRegistry()

        def dummy(_key: str, _resolve: object) -> None:
            pass

        reg.register("test.cat", frozenset({"k1", "k2"}), dummy)
        assert len(reg.get("test.cat", "k1")) == 1
        assert len(reg.get("test.cat", "k2")) == 1

    def test_register_multiple_constraints_same_key(
        self,
    ) -> None:
        """Multiple constraints can be registered for the same key."""
        reg = ConstraintRegistry()

        def dummy1(_key: str, _resolve: object) -> None:
            pass

        def dummy2(_key: str, _resolve: object) -> None:
            pass

        reg.register("test.cat", frozenset({"k1"}), dummy1)
        reg.register("test.cat", frozenset({"k1"}), dummy2)
        assert len(reg.get("test.cat", "k1")) == 2

    def test_get_requires_exact_category(
        self,
    ) -> None:
        """get() requires exact (category, key) match."""
        reg = ConstraintRegistry()

        def dummy(_key: str, _resolve: object) -> None:
            pass

        reg.register("cat1", frozenset({"k1"}), dummy)
        assert reg.get("cat2", "k1") == []


class TestNocloudPortRangeConstraint:
    """Constraint: nocloud_port_range_end must be > nocloud_port_range_start."""

    def test_valid_range(self, service: SettingsService) -> None:
        """Setting both ends with end > start succeeds."""
        service.set("defaults.cloudinit", "nocloud_port_range_start", 8000)
        service.set("defaults.cloudinit", "nocloud_port_range_end", 9000)
        assert (
            service.get("defaults.cloudinit", "nocloud_port_range_start")
            == 8000
        )
        assert (
            service.get("defaults.cloudinit", "nocloud_port_range_end") == 9000
        )

    def test_end_less_than_start_raises(self, service: SettingsService) -> None:
        """Setting nocloud_port_range_end < start raises ConfigError."""
        # Defaults: start=8000, end=9000. Set start to 5000 (OK, end=9000 > 5000).
        service.set("defaults.cloudinit", "nocloud_port_range_start", 5000)
        # Now end=9000 (default) > start=5000. Setting end=3000 makes end < start.
        with pytest.raises(ConfigError, match="nocloud_port_range_end"):
            service.set("defaults.cloudinit", "nocloud_port_range_end", 3000)

    def test_end_equal_to_start_raises(self, service: SettingsService) -> None:
        """Setting nocloud_port_range_end == start raises ConfigError."""
        service.set("defaults.cloudinit", "nocloud_port_range_start", 5000)
        with pytest.raises(ConfigError, match="nocloud_port_range_end"):
            service.set("defaults.cloudinit", "nocloud_port_range_end", 5000)

    def test_start_greater_than_end_raises(
        self, service: SettingsService
    ) -> None:
        """Setting nocloud_port_range_start > end raises ConfigError."""
        # Default end is 9000. Set end to 12000 (OK, 12000 > 8000 default start).
        service.set("defaults.cloudinit", "nocloud_port_range_end", 12000)
        # Now start=8000 (default) < end=12000. Setting start=13000 makes start > end.
        with pytest.raises(ConfigError, match="nocloud_port_range_end"):
            service.set(
                "defaults.cloudinit",
                "nocloud_port_range_start",
                13000,
            )

    def test_start_equal_to_end_raises(self, service: SettingsService) -> None:
        """Setting nocloud_port_range_start == end raises ConfigError."""
        # Set end to 11000 (OK, 11000 > 8000 default start).
        service.set("defaults.cloudinit", "nocloud_port_range_end", 11000)
        # Setting start=11000 makes start == end, which is invalid.
        with pytest.raises(ConfigError, match="nocloud_port_range_end"):
            service.set(
                "defaults.cloudinit",
                "nocloud_port_range_start",
                11000,
            )

    def test_wide_range_succeeds(self, service: SettingsService) -> None:
        """A wide valid range succeeds."""
        service.set("defaults.cloudinit", "nocloud_port_range_start", 1024)
        service.set("defaults.cloudinit", "nocloud_port_range_end", 65535)
        assert (
            service.get("defaults.cloudinit", "nocloud_port_range_start")
            == 1024
        )
        assert (
            service.get("defaults.cloudinit", "nocloud_port_range_end") == 65535
        )


class TestMacPrefixConstraint:
    """Constraint: guest_mac_prefix must be valid 2-byte hex MAC prefix."""

    def test_valid_prefix(self, service: SettingsService) -> None:
        """Setting a valid MAC prefix succeeds."""
        service.set("defaults.vm", "guest_mac_prefix", "02:FC")
        assert service.get("defaults.vm", "guest_mac_prefix") == "02:FC"

    def test_invalid_prefix_raises(self, service: SettingsService) -> None:
        """Setting an invalid MAC prefix raises ConfigError."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            service.set("defaults.vm", "guest_mac_prefix", "invalid")

    def test_single_byte_raises(self, service: SettingsService) -> None:
        """A single hex byte raises ConfigError."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            service.set("defaults.vm", "guest_mac_prefix", "02")

    def test_three_bytes_raises(self, service: SettingsService) -> None:
        """Three hex bytes raises ConfigError."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            service.set("defaults.vm", "guest_mac_prefix", "02:FC:00")

    def test_lowercase_valid(self, service: SettingsService) -> None:
        """Lowercase hex prefix succeeds."""
        service.set("defaults.vm", "guest_mac_prefix", "aa:bb")
        assert service.get("defaults.vm", "guest_mac_prefix") == "aa:bb"

    def test_uppercase_valid(self, service: SettingsService) -> None:
        """Uppercase hex prefix succeeds."""
        service.set("defaults.vm", "guest_mac_prefix", "AA:BB")
        assert service.get("defaults.vm", "guest_mac_prefix") == "AA:BB"

    def test_mixed_case_valid(self, service: SettingsService) -> None:
        """Mixed-case hex prefix succeeds."""
        service.set("defaults.vm", "guest_mac_prefix", "aA:bB")
        assert service.get("defaults.vm", "guest_mac_prefix") == "aA:bB"

    def test_colon_prefix_raises(self, service: SettingsService) -> None:
        """Prefix starting with colon raises ConfigError."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            service.set("defaults.vm", "guest_mac_prefix", ":02:FC")

    def test_trailing_colon_raises(self, service: SettingsService) -> None:
        """Prefix ending with colon raises ConfigError."""
        with pytest.raises(ConfigError, match="Invalid MAC prefix"):
            service.set("defaults.vm", "guest_mac_prefix", "02:FC:")


class TestConstraintsWithDefaults:
    """Constraints interact correctly with defaults from constants."""

    def test_default_values_valid(self, service: SettingsService) -> None:
        """The hardcoded default MAC prefix is valid."""
        from mvmctl.constants import OVERRIDABLE_DEFAULTS

        prefix = OVERRIDABLE_DEFAULTS["defaults.vm"]["guest_mac_prefix"]
        assert prefix == "02:FC"

    def test_default_nocloud_range_valid(
        self, service: SettingsService
    ) -> None:
        """The hardcoded default nocloud range is valid."""
        from mvmctl.constants import OVERRIDABLE_DEFAULTS

        start = OVERRIDABLE_DEFAULTS["defaults.cloudinit"][
            "nocloud_port_range_start"
        ]
        end = OVERRIDABLE_DEFAULTS["defaults.cloudinit"][
            "nocloud_port_range_end"
        ]
        assert end > start

    def test_set_unrelated_key_does_not_trigger_constraint(
        self, service: SettingsService
    ) -> None:
        """Setting an unrelated key does not trigger constraints."""
        # vcpu_count is in defaults.vm but not guarded by any constraint
        service.set("defaults.vm", "vcpu_count", 4)
        assert service.get("defaults.vm", "vcpu_count") == 4


class TestPreRegisteredConstraints:
    """Verify the singleton `constraints` instance has expected registrations."""

    def test_nocloud_constraints_registered(self) -> None:
        """nocloud port range constraint is registered for both keys."""
        result = constraints.get(
            "defaults.cloudinit", "nocloud_port_range_start"
        )
        assert len(result) >= 1
        result = constraints.get("defaults.cloudinit", "nocloud_port_range_end")
        assert len(result) >= 1

    def test_mac_prefix_constraint_registered(self) -> None:
        """MAC prefix constraint is registered for guest_mac_prefix."""
        result = constraints.get("defaults.vm", "guest_mac_prefix")
        assert len(result) >= 1

    def test_unregistered_key_has_no_constraints(self) -> None:
        """Unregistered keys have no constraints."""
        result = constraints.get("defaults.vm", "vcpu_count")
        assert result == []
