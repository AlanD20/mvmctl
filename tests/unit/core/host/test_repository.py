"""Tests for HostRepository — database operations for host state."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.host._repository import HostRepository
from mvmctl.models import HostStateChangeItem, HostStateItem


@pytest.fixture
def repo() -> HostRepository:
    return HostRepository()


@pytest.fixture
def db() -> Database:
    return Database()


class TestHostRepository:
    """Test suite for HostRepository database operations."""

    # ------------------------------------------------------------------
    # get_state / initialize_state
    # ------------------------------------------------------------------

    def test_get_state_not_initialized(self, repo: HostRepository) -> None:
        """get_state should return None when host_state table has no row."""
        assert repo.get_state() is None

    def test_initialize_state_creates_row(self, repo: HostRepository) -> None:
        """initialize_state should insert the singleton row (id=1)."""
        state = repo.initialize_state()
        assert isinstance(state, HostStateItem)
        assert state.id == 1
        # SQLite stores booleans as 0/1 integers
        assert state.initialized == 0
        assert state.mvm_group_created == 0
        assert state.sudoers_configured == 0
        assert state.default_network_created == 0

    def test_initialize_state_idempotent(self, repo: HostRepository) -> None:
        """initialize_state should be safe to call multiple times."""
        state1 = repo.initialize_state()
        state2 = repo.initialize_state()
        assert state1.id == 1
        assert state2.id == 1

    def test_get_state_after_initialize(self, repo: HostRepository) -> None:
        """get_state should return the HostStateItem after initialization."""
        repo.initialize_state()
        state = repo.get_state()
        assert state is not None
        assert isinstance(state, HostStateItem)
        assert state.initialized == 0

    # ------------------------------------------------------------------
    # set_initialized
    # ------------------------------------------------------------------

    def test_set_initialized(self, repo: HostRepository) -> None:
        """set_initialized should mark host as initialized with a timestamp."""
        repo.initialize_state()
        ts = datetime.now(UTC).isoformat()
        repo.set_initialized(ts)
        state = repo.get_state()
        assert state is not None
        assert state.initialized == 1
        assert state.initialized_at == ts

    def test_set_initialized_not_initialized_no_row(
        self, repo: HostRepository
    ) -> None:
        """set_initialized should not raise when no row exists (UPDATE is a no-op)."""
        ts = datetime.now(UTC).isoformat()
        repo.set_initialized(ts)
        # Should not raise; UPDATE on empty table is a no-op
        state = repo.get_state()
        # get_state returns None because no row exists
        assert state is None

    # ------------------------------------------------------------------
    # update_component
    # ------------------------------------------------------------------

    @pytest.mark.parametrize(
        "component",
        [
            "mvm_group_created",
            "sudoers_configured",
            "default_network_created",
        ],
    )
    def test_update_component(
        self, repo: HostRepository, component: str
    ) -> None:
        """update_component should set a specific boolean flag to 1."""
        repo.initialize_state()
        repo.update_component(component, True)
        state = repo.get_state()
        assert state is not None
        assert getattr(state, component) == 1

    def test_update_component_invalid(self, repo: HostRepository) -> None:
        """update_component should raise ValueError for unknown components."""
        with pytest.raises(ValueError, match="Unknown host state component"):
            repo.update_component("nonexistent", True)

    # ------------------------------------------------------------------
    # reset_state
    # ------------------------------------------------------------------

    def test_reset_state(self, repo: HostRepository) -> None:
        """reset_state should reset all flags to 0."""
        repo.initialize_state()
        repo.update_component("mvm_group_created", True)
        repo.update_component("sudoers_configured", True)
        repo.set_initialized(datetime.now(UTC).isoformat())
        repo.reset_state()
        state = repo.get_state()
        assert state is not None
        assert state.initialized == 0
        assert state.mvm_group_created == 0
        assert state.sudoers_configured == 0
        assert state.default_network_created == 0

    # ------------------------------------------------------------------
    # add_change
    # ------------------------------------------------------------------

    def test_add_change_single(self, repo: HostRepository) -> None:
        """add_change should insert a single host state change record."""
        change = HostStateChangeItem(
            session_id="sess-1",
            init_timestamp="2025-01-01T00:00:00",
            setting="net.ipv4.ip_forward",
            mechanism="sysctl",
            original_value="0",
            applied_value="1",
            reverted=False,
            change_order=0,
            created_at="2025-01-01T00:00:00",
        )
        repo.add_change(change)
        changes = repo.list_changes(include_reverted=True)
        assert len(changes) == 1
        assert changes[0].setting == "net.ipv4.ip_forward"
        assert changes[0].mechanism == "sysctl"
        assert changes[0].original_value == "0"
        assert changes[0].applied_value == "1"

    def test_add_change_with_reverted(self, repo: HostRepository) -> None:
        """add_change should persist the reverted flag and reverted_at."""
        change = HostStateChangeItem(
            session_id="sess-1",
            init_timestamp="2025-01-01T00:00:00",
            setting="test",
            mechanism="test",
            applied_value="v",
            reverted=True,
            reverted_at="2025-01-02T00:00:00",
            revert_mechanism="manual",
            change_order=0,
            created_at="2025-01-01T00:00:00",
        )
        repo.add_change(change)
        changes = repo.list_changes(include_reverted=True)
        assert len(changes) == 1
        # SQLite stores booleans as 0/1 integers
        assert changes[0].reverted == 1
        assert changes[0].reverted_at == "2025-01-02T00:00:00"
        assert changes[0].revert_mechanism == "manual"

    # ------------------------------------------------------------------
    # add_changes (bulk)
    # ------------------------------------------------------------------

    def test_add_changes_bulk(self, repo: HostRepository) -> None:
        """add_changes should atomically insert multiple changes."""
        changes = [
            HostStateChangeItem(
                session_id="sess-2",
                init_timestamp="2025-01-01T00:00:00",
                setting="a",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="2025-01-01T00:00:00",
            ),
            HostStateChangeItem(
                session_id="sess-2",
                init_timestamp="2025-01-01T00:00:00",
                setting="b",
                mechanism="file_create",
                applied_value="/path",
                reverted=False,
                change_order=1,
                created_at="2025-01-01T00:00:00",
            ),
        ]
        repo.add_changes(changes)
        all_changes = repo.list_changes(include_reverted=True)
        assert len(all_changes) == 2
        assert all_changes[0].setting == "a"
        assert all_changes[1].setting == "b"

    def test_add_changes_empty(self, repo: HostRepository) -> None:
        """add_changes with empty list should not raise."""
        repo.add_changes([])
        assert repo.list_changes(include_reverted=True) == []

    # ------------------------------------------------------------------
    # delete_changes_except_session
    # ------------------------------------------------------------------

    def test_delete_changes_except_session(self, repo: HostRepository) -> None:
        """delete_changes_except_session should remove changes from other sessions."""
        repo.add_change(
            HostStateChangeItem(
                session_id="keep",
                init_timestamp="",
                setting="a",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        repo.add_change(
            HostStateChangeItem(
                session_id="delete",
                init_timestamp="",
                setting="b",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=1,
                created_at="",
            )
        )
        repo.delete_changes_except_session("keep")
        remaining = repo.list_changes(include_reverted=True)
        assert len(remaining) == 1
        assert remaining[0].session_id == "keep"

    # ------------------------------------------------------------------
    # list_changes
    # ------------------------------------------------------------------

    def test_list_changes_empty(self, repo: HostRepository) -> None:
        """list_changes should return empty list when no changes exist."""
        assert repo.list_changes() == []

    def test_list_changes_exclude_reverted(self, repo: HostRepository) -> None:
        """list_changes with include_reverted=False should exclude reverted changes."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="a",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="b",
                mechanism="sysctl",
                applied_value="1",
                reverted=True,
                reverted_at="now",
                change_order=1,
                created_at="",
            )
        )
        changes = repo.list_changes(include_reverted=False)
        assert len(changes) == 1
        assert changes[0].setting == "a"

    def test_list_changes_filter_by_session(self, repo: HostRepository) -> None:
        """list_changes should filter by session_id when provided."""
        repo.add_change(
            HostStateChangeItem(
                session_id="s1",
                init_timestamp="",
                setting="a",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        repo.add_change(
            HostStateChangeItem(
                session_id="s2",
                init_timestamp="",
                setting="b",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=1,
                created_at="",
            )
        )
        changes = repo.list_changes(session_id="s1")
        assert len(changes) == 1
        assert changes[0].setting == "a"

    def test_list_changes_order(self, repo: HostRepository) -> None:
        """list_changes should return changes ordered by change_order ASC."""
        changes = [
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting=str(i),
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=i,
                created_at="",
            )
            for i in [2, 0, 1]
        ]
        repo.add_changes(changes)
        result = repo.list_changes(include_reverted=True)
        orders = [c.change_order for c in result]
        assert orders == [0, 1, 2]

    # ------------------------------------------------------------------
    # mark_change_reverted
    # ------------------------------------------------------------------

    def test_mark_change_reverted(self, repo: HostRepository) -> None:
        """mark_change_reverted should update a single change as reverted."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="test",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        change = repo.list_changes(include_reverted=True)[0]
        assert change.id is not None
        repo.mark_change_reverted(change.id, "2025-01-02T00:00:00", "manual")
        updated = repo.list_changes(include_reverted=True)[0]
        assert updated.reverted == 1
        assert updated.reverted_at == "2025-01-02T00:00:00"
        assert updated.revert_mechanism == "manual"

    # ------------------------------------------------------------------
    # revert_changes
    # ------------------------------------------------------------------

    def test_revert_changes(self, repo: HostRepository) -> None:
        """revert_changes should mark all unreverted changes for a session (LIFO order)."""
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="a",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=0,
                created_at="",
            )
        )
        repo.add_change(
            HostStateChangeItem(
                session_id="sess",
                init_timestamp="",
                setting="b",
                mechanism="sysctl",
                applied_value="1",
                reverted=False,
                change_order=1,
                created_at="",
            )
        )
        reverted = repo.revert_changes("sess", "2025-01-02T00:00:00")
        assert len(reverted) == 2
        # Returns reversed (LIFO) order: last change first
        assert reverted[0].setting == "b"
        assert reverted[1].setting == "a"
        # All should be marked reverted in DB
        for c in repo.list_changes(session_id="sess", include_reverted=True):
            assert c.reverted == 1
