"""Tests for host state CRUD methods in MVMDatabase.

Tests cover:
- get_host_state — returns None before init, returns HostState after
- initialize_host_state — creates singleton, idempotent
- set_host_initialized — sets initialized=True, initialized_at
- update_host_component — valid components, invalid raises ValueError
- reset_host_state — resets all flags
- add_host_change — inserts record correctly
- list_host_changes — all, by session, exclude reverted
- mark_change_reverted — marks single change
- revert_host_changes — returns LIFO order, marks all reverted
"""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import HostState, HostStateChange


def make_change(session_id: str = "sess1", order: int = 1) -> HostStateChange:
    """Factory helper for creating HostStateChange instances."""
    return HostStateChange(
        session_id=session_id,
        init_timestamp="2026-04-01T00:00:00",
        setting="bridge",
        mechanism="ip link add",
        applied_value="mvm-default",
        change_order=order,
        reverted=False,
        created_at="2024-01-01T00:00:00+00:00",
    )


@pytest.fixture
def db(tmp_path: Path) -> MVMDatabase:
    """Create a temporary database with migrations applied."""
    db_path = tmp_path / "test.db"
    database = MVMDatabase(db_path=db_path)
    database.migrate()
    return database


class TestHostState:
    """Tests for host_state singleton CRUD operations."""

    def test_get_host_state_returns_none_before_init(self, db: MVMDatabase) -> None:
        """get_host_state should return None when no row exists."""
        result = db.get_host_state()
        assert result is None

    def test_initialize_host_state_creates_singleton(self, db: MVMDatabase) -> None:
        """initialize_host_state should create the singleton row."""
        state = db.initialize_host_state()

        assert state is not None
        assert state.id == 1
        assert state.initialized == False  # noqa: E712
        assert state.mvm_group_created == False  # noqa: E712
        assert state.sudoers_configured == False  # noqa: E712
        assert state.default_network_created == False  # noqa: E712
        assert state.initialized_at is not None
        assert state.updated_at is not None

    def test_initialize_host_state_is_idempotent(self, db: MVMDatabase) -> None:
        """initialize_host_state should be idempotent (no error on second call)."""
        state1 = db.initialize_host_state()
        state2 = db.initialize_host_state()

        assert state1 is not None
        assert state2 is not None
        assert state1.id == state2.id == 1
        assert state1.initialized == state2.initialized == False  # noqa: E712

    def test_get_host_state_returns_state_after_init(self, db: MVMDatabase) -> None:
        """get_host_state should return HostState after initialization."""
        db.initialize_host_state()

        result = db.get_host_state()

        assert result is not None
        assert result.id == 1
        assert isinstance(result, HostState)

    def test_set_host_initialized_marks_initialized(self, db: MVMDatabase) -> None:
        """set_host_initialized should set initialized=True and initialized_at."""
        db.initialize_host_state()
        timestamp = "2026-04-01T12:00:00"

        db.set_host_initialized(timestamp)

        state = db.get_host_state()
        assert state is not None
        assert state.initialized == True  # noqa: E712
        assert state.initialized_at == timestamp

    def test_update_host_component_valid_components(self, db: MVMDatabase) -> None:
        """update_host_component should update valid component flags."""
        db.initialize_host_state()

        components = ["mvm_group_created", "sudoers_configured", "default_network_created"]
        for component in components:
            db.update_host_component(component, True)

        state = db.get_host_state()
        assert state is not None
        assert state.mvm_group_created == True  # noqa: E712
        assert state.sudoers_configured == True  # noqa: E712
        assert state.default_network_created == True  # noqa: E712

    def test_update_host_component_invalid_raises(self, db: MVMDatabase) -> None:
        """update_host_component should raise ValueError for invalid component."""
        db.initialize_host_state()

        with pytest.raises(ValueError, match="Unknown host state component"):
            db.update_host_component("invalid_component", True)

    def test_update_host_component_false_value(self, db: MVMDatabase) -> None:
        """update_host_component should handle False values correctly."""
        db.initialize_host_state()
        db.update_host_component("mvm_group_created", True)

        db.update_host_component("mvm_group_created", False)

        state = db.get_host_state()
        assert state is not None
        assert state.mvm_group_created == False  # noqa: E712

    def test_reset_host_state_clears_all_flags(self, db: MVMDatabase) -> None:
        """reset_host_state should reset all flags to False/None."""
        db.initialize_host_state()
        db.set_host_initialized("2026-04-01T12:00:00")
        db.update_host_component("mvm_group_created", True)
        db.update_host_component("sudoers_configured", True)
        db.update_host_component("default_network_created", True)

        db.reset_host_state()

        state = db.get_host_state()
        assert state is not None
        assert state.initialized == False  # noqa: E712
        assert state.mvm_group_created == False  # noqa: E712
        assert state.sudoers_configured == False  # noqa: E712
        assert state.default_network_created == False  # noqa: E712
        assert state.initialized_at is not None
        assert state.updated_at is not None


class TestHostStateChanges:
    """Tests for host_state_changes CRUD operations."""

    def test_add_host_change_inserts_record(self, db: MVMDatabase) -> None:
        """add_host_change should insert a change record."""
        change = make_change(session_id="sess1", order=1)

        db.add_host_change(change)

        changes = db.list_host_changes()
        assert len(changes) == 1
        assert changes[0].session_id == "sess1"
        assert changes[0].setting == "bridge"
        assert changes[0].mechanism == "ip link add"
        assert changes[0].applied_value == "mvm-default"
        assert changes[0].change_order == 1
        assert changes[0].reverted == False  # noqa: E712
        assert changes[0].id is not None  # AUTOINCREMENT assigned

    def test_add_host_change_with_optional_fields(self, db: MVMDatabase) -> None:
        """add_host_change should handle optional fields."""
        change = HostStateChange(
            session_id="sess2",
            init_timestamp="2026-04-02T00:00:00",
            setting="iptables",
            mechanism="iptables -t nat -A POSTROUTING",
            applied_value="MASQUERADE",
            change_order=2,
            reverted=True,
            created_at="2024-01-01T00:00:00+00:00",
            original_value="none",
            reverted_at="2026-04-02T01:00:00",
            revert_mechanism="iptables -t nat -D POSTROUTING",
        )

        db.add_host_change(change)

        changes = db.list_host_changes()
        assert len(changes) == 1
        assert changes[0].original_value == "none"
        assert changes[0].reverted == True  # noqa: E712
        assert changes[0].reverted_at == "2026-04-02T01:00:00"
        assert changes[0].revert_mechanism == "iptables -t nat -D POSTROUTING"

    def test_list_host_changes_all(self, db: MVMDatabase) -> None:
        """list_host_changes should return all changes when no filters."""
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess2", order=1))

        changes = db.list_host_changes()

        assert len(changes) == 2

    def test_list_host_changes_by_session(self, db: MVMDatabase) -> None:
        """list_host_changes should filter by session_id."""
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess1", order=2))
        db.add_host_change(make_change(session_id="sess2", order=1))

        changes = db.list_host_changes(session_id="sess1")

        assert len(changes) == 2
        assert all(c.session_id == "sess1" for c in changes)

    def test_list_host_changes_exclude_reverted(self, db: MVMDatabase) -> None:
        """list_host_changes should exclude reverted when include_reverted=False."""
        change1 = make_change(session_id="sess1", order=1)
        change2 = make_change(session_id="sess1", order=2)
        db.add_host_change(change1)
        db.add_host_change(change2)

        # Mark first change as reverted
        db.mark_change_reverted(1, "2026-04-01T01:00:00")

        changes = db.list_host_changes(session_id="sess1", include_reverted=False)

        assert len(changes) == 1
        assert changes[0].change_order == 2
        assert changes[0].reverted == False  # noqa: E712

    def test_list_host_changes_ordered_by_change_order(self, db: MVMDatabase) -> None:
        """list_host_changes should return changes ordered by change_order ASC."""
        db.add_host_change(make_change(session_id="sess1", order=3))
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess1", order=2))

        changes = db.list_host_changes(session_id="sess1")

        orders = [c.change_order for c in changes]
        assert orders == [1, 2, 3]

    def test_mark_change_reverted_updates_record(self, db: MVMDatabase) -> None:
        """mark_change_reverted should update reverted status."""
        change = make_change(session_id="sess1", order=1)
        db.add_host_change(change)

        db.mark_change_reverted(1, "2026-04-01T02:00:00", "manual revert")

        changes = db.list_host_changes()
        assert len(changes) == 1
        assert changes[0].reverted == True  # noqa: E712
        assert changes[0].reverted_at == "2026-04-01T02:00:00"
        assert changes[0].revert_mechanism == "manual revert"

    def test_mark_change_reverted_without_mechanism(self, db: MVMDatabase) -> None:
        """mark_change_reverted should work without revert_mechanism."""
        change = make_change(session_id="sess1", order=1)
        db.add_host_change(change)

        db.mark_change_reverted(1, "2026-04-01T02:00:00")

        changes = db.list_host_changes()
        assert changes[0].reverted == True  # noqa: E712
        assert changes[0].revert_mechanism is None

    def test_revert_host_changes_returns_lifo_order(self, db: MVMDatabase) -> None:
        """revert_host_changes should return changes in LIFO order."""
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess1", order=2))
        db.add_host_change(make_change(session_id="sess1", order=3))

        reverted = db.revert_host_changes("sess1", "2026-04-01T03:00:00")

        # Should be in LIFO order (change_order DESC)
        orders = [c.change_order for c in reverted]
        assert orders == [3, 2, 1]

    def test_revert_host_changes_marks_all_reverted(self, db: MVMDatabase) -> None:
        """revert_host_changes should mark all changes as reverted."""
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess1", order=2))

        db.revert_host_changes("sess1", "2026-04-01T03:00:00")

        changes = db.list_host_changes(session_id="sess1", include_reverted=False)
        assert len(changes) == 0

        all_changes = db.list_host_changes(session_id="sess1", include_reverted=True)
        assert len(all_changes) == 2
        assert all(c.reverted for c in all_changes)

    def test_revert_host_changes_only_affects_session(self, db: MVMDatabase) -> None:
        """revert_host_changes should only affect the specified session."""
        db.add_host_change(make_change(session_id="sess1", order=1))
        db.add_host_change(make_change(session_id="sess2", order=1))

        db.revert_host_changes("sess1", "2026-04-01T03:00:00")

        sess1_changes = db.list_host_changes(session_id="sess1", include_reverted=False)
        sess2_changes = db.list_host_changes(session_id="sess2", include_reverted=False)

        assert len(sess1_changes) == 0  # All reverted
        assert len(sess2_changes) == 1  # Not affected

    def test_revert_host_changes_empty_session(self, db: MVMDatabase) -> None:
        """revert_host_changes should return empty list for session with no changes."""
        result = db.revert_host_changes("nonexistent", "2026-04-01T03:00:00")

        assert result == []
