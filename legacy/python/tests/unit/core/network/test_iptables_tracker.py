"""Tests for IPTablesTracker with real SQLite DB and mocked subprocess."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core._shared._iptables_tracker import (
    IPTablesRuleRepository,
    IPTablesTracker,
)
from mvmctl.exceptions import IPTablesTrackerError, ProcessError
from mvmctl.models import (
    FirewallChain,
    FirewallPort,
    FirewallProtocol,
    FirewallRule,
    FirewallRuleType,
    FirewallTable,
    FirewallTarget,
    FirewallWildcard,
)

# DB CHECK constraint only allows: 'masquerade', 'forward_in', 'forward_out', 'nocloud_input'
_VALID_RULE_TYPE = FirewallRuleType.FORWARD_IN
_VALID_CHAIN = FirewallChain.MVM_FORWARD


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied."""
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def repo(db: Database) -> IPTablesRuleRepository:
    """Create a fresh IPTablesRuleRepository."""
    return IPTablesRuleRepository(db)


@pytest.fixture
def tracker(repo: IPTablesRuleRepository) -> IPTablesTracker:
    """Create an IPTablesTracker with mocked subprocess."""
    return IPTablesTracker(repo)


def _seed_network(
    db: Database, nid: str = "net-test", name: str | None = None
) -> str:
    """Insert a network row and return its ID."""
    from datetime import UTC, datetime

    now = datetime.now(tz=UTC).isoformat()
    effective_name = name or nid
    with db.connect() as conn:
        conn.execute(
            """
            INSERT OR IGNORE INTO networks (id, name, subnet, bridge, ipv4_gateway,
                                            bridge_active, nat_enabled, is_default,
                                            is_present, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                nid,
                effective_name,
                "10.0.0.0/24",
                "mvmbr0",
                "10.0.0.1",
                1,
                1,
                1,
                1,
                now,
                now,
            ),
        )
    return nid


def _make_rule(
    network_id: str = "net-test",
    chain_name: FirewallChain = _VALID_CHAIN,
    rule_type: FirewallRuleType = _VALID_RULE_TYPE,
    protocol: FirewallProtocol = FirewallProtocol.TCP,
    target: FirewallTarget = FirewallTarget.ACCEPT,
    source: str = "10.0.0.10",
    destination: str = "10.0.0.1",
    in_interface: str = "tap0",
    out_interface: str = "*",
    sport: int = FirewallPort.ANY,
    dport: int = 8080,
    comment_tag: str | None = None,
) -> FirewallRule:
    """Create a standard FirewallRule for testing (DB-valid by default)."""
    return FirewallRule(
        table_name=FirewallTable.FILTER,
        chain_name=chain_name,
        rule_type=rule_type,
        protocol=protocol,
        source=source,
        destination=destination,
        in_interface=in_interface,
        out_interface=out_interface,
        target=target,
        sport=sport,
        dport=dport,
        network_id=network_id,
        is_active=True,
        comment_tag=comment_tag or f"mvm:test:net-test:{dport}",
    )


class TestBuildIptablesArgs:
    """Tests for _build_iptables_args."""

    def test_build_check_args(self, tracker: IPTablesTracker) -> None:
        """_build_iptables_args builds correct -C arguments."""
        rule = _make_rule()
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.CHECK
        )
        assert args[0] == "iptables"
        assert args[1] == "-t"
        assert FirewallTable.FILTER.value in args
        assert "-C" in args
        assert _VALID_CHAIN.value in args
        assert "-p" in args
        assert FirewallProtocol.TCP.value in args
        assert "-s" in args
        assert "10.0.0.10" in args

    def test_build_append_args(self, tracker: IPTablesTracker) -> None:
        """_build_iptables_args builds correct -A arguments."""
        rule = _make_rule()
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.APPEND
        )
        assert "-A" in args

    def test_build_delete_args(self, tracker: IPTablesTracker) -> None:
        """_build_iptables_args builds correct -D arguments."""
        rule = _make_rule()
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.DELETE
        )
        assert "-D" in args


class TestBuildComment:
    """Tests for _build_comment."""

    def test_build_comment_basic(self, tracker: IPTablesTracker) -> None:
        """_build_comment creates standard comment format."""
        comment = tracker._build_comment(
            FirewallRuleType.NOCLOUDNET_INPUT, "test-net", ""
        )
        assert comment.startswith("mvm:")
        assert "nocloudnet_input" in comment
        assert "test-net" in comment

    def test_build_comment_with_context(self, tracker: IPTablesTracker) -> None:
        """_build_comment appends context."""
        comment = tracker._build_comment(
            FirewallRuleType.NOCLOUDNET_INPUT, "test-net", "vm123"
        )
        assert "vm123" in comment


class TestEnsureRule:
    """Tests for ensure_rule."""

    def test_ensure_rule_creates_new(
        self, tracker: IPTablesTracker, db: Database
    ) -> None:
        """ensure_rule creates a new rule when iptables check fails and DB is empty."""
        _seed_network(db)

        def _mock_subprocess_run(cmd, **kwargs):
            # -C check fails (rule doesn't exist)
            if "-C" in cmd:
                raise ProcessError("Command failed (exit 1): iptables")
            # -A succeeds
            return MagicMock(returncode=0, stdout=b"", stderr=b"")

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            rule = _make_rule(dport=9090)
            result = tracker.ensure_rule(rule)

        assert result.success is True
        assert result.rule is not None
        assert result.rule.dport == 9090
        # Verify it's in the database
        db_rules = tracker._repo.list_all()
        assert len(db_rules) == 1

    def test_ensure_rule_already_exists(
        self, tracker: IPTablesTracker, db: Database
    ) -> None:
        """ensure_rule returns success when rule already exists in iptables."""
        _seed_network(db)

        def _mock_subprocess_run(cmd, **kwargs):
            # -C check succeeds (rule exists)
            if "-C" in cmd:
                return MagicMock(returncode=0)
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            rule = _make_rule(dport=7070)
            result = tracker.ensure_rule(rule)

        assert result.success is True
        # Rule was created in DB since it didn't exist there
        db_rules = tracker._repo.list_all()
        assert len(db_rules) == 1

    def test_ensure_rule_failure(
        self, tracker: IPTablesTracker, db: Database
    ) -> None:
        """ensure_rule returns failure when iptables command fails."""
        _seed_network(db)

        def _mock_subprocess_run(cmd, **kwargs):
            if "-C" in cmd:
                raise ProcessError("Command failed (exit 1): iptables")
            raise ProcessError(
                "Command failed (exit 1): iptables: Resource temporarily unavailable"
            )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            rule = _make_rule(dport=6060)
            result = tracker.ensure_rule(rule)

        assert result.success is False
        assert result.error_message is not None


class TestRemoveRule:
    """Tests for remove_rule."""

    def test_remove_rule_success(
        self, tracker: IPTablesTracker, db: Database
    ) -> None:
        """remove_rule removes a rule successfully."""
        _seed_network(db)
        # First insert a rule into DB
        db_rule = tracker._repo.insert(_make_rule(dport=5050))

        def _mock_subprocess_run(cmd, **kwargs):
            # remove_rule uses check=False for -D
            if "-D" in cmd:
                return MagicMock(returncode=0)
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.remove_rule(db_rule)

        assert result.success is True

    def test_remove_rule_idempotent(self, tracker: IPTablesTracker) -> None:
        """remove_rule is idempotent when rule doesn't exist."""
        rule = _make_rule(dport=4040)

        def _mock_subprocess_run(cmd, **kwargs):
            # -D might fail (check=False), triggers the line-number fallback
            if "-D" in cmd:
                return MagicMock(returncode=1, stderr=b"Bad rule")
            # Then -L (check=False) for line-number fallback
            return MagicMock(returncode=1)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.remove_rule(rule)

        # Best-effort: even if iptables fails, we try fallback
        assert result.success is True


class TestEnsureChain:
    """Tests for ensure_chain."""

    def test_ensure_chain_creates(self, tracker: IPTablesTracker) -> None:
        """ensure_chain creates a new chain."""
        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            # First call is -L check (check=False -> return code 1 for "not found")
            if call_count[0] == 1:
                return MagicMock(returncode=1)
            # Second call is -N create (check=True -> succeeds)
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.ensure_chain(_VALID_CHAIN)
        assert result is True

    def test_ensure_chain_already_exists(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain returns False when chain already exists."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            return_value=MagicMock(returncode=0),
        ):
            result = tracker.ensure_chain(_VALID_CHAIN)
        assert result is False

    def test_ensure_chain_handles_already_exists_error(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain returns False on 'Chain already exists' error."""
        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)  # Check fails
            # Chain creation with check=True raises ProcessError
            raise ProcessError("Chain already exists")

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.ensure_chain(_VALID_CHAIN)
        assert result is False

    def test_ensure_chain_raises_on_other_error(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain raises IPTablesTrackerError on unexpected error."""
        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)  # Check fails
            # Other error with check=True
            raise ProcessError("Permission denied")

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            with pytest.raises(IPTablesTrackerError, match="Failed to create"):
                tracker.ensure_chain(_VALID_CHAIN)


class TestEnsureJumpRule:
    """Tests for ensure_jump_rule."""

    def test_ensure_jump_rule_creates(self, tracker: IPTablesTracker) -> None:
        """ensure_jump_rule creates a new jump rule."""
        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                # -C check (check=False) returns code 1 for "not found"
                return MagicMock(returncode=1)
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.ensure_jump_rule("INPUT", _VALID_CHAIN.value)
        assert result.success is True

    def test_ensure_jump_rule_already_exists(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_jump_rule returns success when jump rule exists."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            return_value=MagicMock(returncode=0),
        ):
            result = tracker.ensure_jump_rule("INPUT", _VALID_CHAIN.value)
        assert result.success is True


class TestFlushChain:
    """Tests for flush_chain."""

    def test_flush_chain_success(
        self, tracker: IPTablesTracker, db: Database
    ) -> None:
        """flush_chain flushes a chain and marks DB rules deleted."""
        _seed_network(db)
        # Insert a DB rule first
        rule = _make_rule(dport=3030)
        tracker._repo.insert(rule)

        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                # Chain exists check passes
                return MagicMock(returncode=0)
            # Flush succeeds
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            result = tracker.flush_chain(_VALID_CHAIN)
        assert result is True

    def test_flush_chain_not_exists(self, tracker: IPTablesTracker) -> None:
        """flush_chain returns False when chain doesn't exist."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            return_value=MagicMock(returncode=1),
        ):
            result = tracker.flush_chain(_VALID_CHAIN)
        assert result is False

    def test_flush_chain_raises_on_failure(
        self, tracker: IPTablesTracker
    ) -> None:
        """flush_chain raises IPTablesTrackerError on flush failure."""
        call_count = [0]

        def _mock_subprocess_run(cmd, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=0)  # Chain exists
            raise ProcessError("Command failed (exit 1): iptables -F")

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
            side_effect=_mock_subprocess_run,
        ):
            with pytest.raises(IPTablesTrackerError, match="Failed to flush"):
                tracker.flush_chain(_VALID_CHAIN)


class TestRemoveChain:
    """Tests for remove_chain."""

    def test_remove_chain_success(
        self,
        tracker: IPTablesTracker,
        repo: IPTablesRuleRepository,
        db: Database,
    ) -> None:
        """remove_chain deletes the chain and marks DB rules deleted."""
        _seed_network(db)
        # Insert a DB rule first
        repo.insert(_make_rule(dport=2020))

        with (
            patch(
                "mvmctl.utils.network.NetworkUtils.chain_exists",
                return_value=True,
            ),
            patch(
                "mvmctl.core._shared._iptables_tracker._tracker.run_cmd",
                return_value=MagicMock(returncode=0),
            ),
        ):
            result = tracker.remove_chain(_VALID_CHAIN)
        assert result is True
        # DB rules should be marked inactive
        remaining = repo.list_all()
        assert all(not r.is_active for r in remaining)

    def test_remove_chain_not_exists(self, tracker: IPTablesTracker) -> None:
        """remove_chain returns False when chain doesn't exist."""
        with patch(
            "mvmctl.utils.network.NetworkUtils.chain_exists", return_value=False
        ):
            result = tracker.remove_chain(_VALID_CHAIN)
        assert result is False


class TestRepositoryIntegration:
    """Integration tests for IPTablesRuleRepository with real DB."""

    def test_insert_and_retrieve(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """insert stores a rule and assigns an ID."""
        _seed_network(db)
        rule = _make_rule(dport=1111)
        inserted = repo.insert(rule)
        assert inserted.id is not None

        retrieved = repo.get(inserted.id)
        assert retrieved is not None
        assert retrieved.dport == 1111
        assert retrieved.is_active is True

    def test_find_by_attributes(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """find_by_attributes locates a rule by its attributes."""
        rule = _make_rule(
            dport=2222,
            network_id=_seed_network(db, "net-abc"),
            protocol=FirewallProtocol.TCP,
            source="10.0.0.20",
            destination="10.0.0.1",
            in_interface="tap1",
        )
        repo.insert(rule)

        found = repo.find_by_attributes(
            table_name=FirewallTable.FILTER,
            chain_name=_VALID_CHAIN,
            rule_type=_VALID_RULE_TYPE,
            network_id="net-abc",
            protocol=FirewallProtocol.TCP,
            source="10.0.0.20",
            destination="10.0.0.1",
            in_interface="tap1",
            out_interface=FirewallWildcard.ANY_INTERFACE,
            sport=FirewallPort.ANY,
            dport=2222,
        )
        assert found is not None
        assert found.network_id == "net-abc"

    def test_mark_deleted(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """mark_deleted sets is_active to 0."""
        _seed_network(db)
        rule = repo.insert(_make_rule(dport=3333))
        repo.mark_deleted(rule.id)
        retrieved = repo.get(rule.id)
        assert retrieved is not None
        assert retrieved.is_active is False or retrieved.is_active == 0

    def test_list_by_network_id(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """list_by_network_id returns rules for a specific network."""
        _seed_network(db, "net-a")
        _seed_network(db, "net-b")
        repo.insert(_make_rule(dport=4444, network_id="net-a"))
        repo.insert(_make_rule(dport=5555, network_id="net-a"))
        repo.insert(_make_rule(dport=6666, network_id="net-b"))

        rules = repo.list_by_network_id("net-a")
        assert len(rules) == 2

    def test_mark_deleted_by_table_chain_name(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """mark_deleted_by_table_chain_name marks all rules in a chain as deleted."""
        _seed_network(db)
        repo.insert(_make_rule(dport=7777))
        repo.insert(_make_rule(dport=8888))

        count = repo.mark_deleted_by_table_chain_name(
            FirewallTable.FILTER, _VALID_CHAIN
        )
        assert count == 2
        remaining = repo.get_by_table_chain_name(
            FirewallTable.FILTER.value, _VALID_CHAIN.value, active_only=True
        )
        assert len(remaining) == 0

    def test_delete_inactive(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """delete_inactive permanently removes inactive records."""
        _seed_network(db)
        rule = repo.insert(_make_rule(dport=9999))
        repo.mark_deleted(rule.id)
        deleted = repo.delete_inactive()
        assert deleted == 1
        assert repo.get(rule.id) is None

    def test_list_by_network_id_batch(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """list_by_network_id_batch returns rules for multiple networks."""
        _seed_network(db, "net-a")
        _seed_network(db, "net-b")
        repo.insert(_make_rule(dport=1111, network_id="net-a"))
        repo.insert(_make_rule(dport=2222, network_id="net-b"))
        results = repo.list_by_network_id_batch(["net-a", "net-b"])
        assert len(results) == 2

    def test_list_by_network_id_batch_empty(
        self, repo: IPTablesRuleRepository
    ) -> None:
        """list_by_network_id_batch returns empty list for empty input."""
        assert repo.list_by_network_id_batch([]) == []

    def test_delete_by_network_id(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """delete_by_network_id removes all rules for a network."""
        _seed_network(db, "net-del")
        repo.insert(_make_rule(dport=1111, network_id="net-del"))
        repo.insert(_make_rule(dport=2222, network_id="net-del"))
        count = repo.delete_by_network_id("net-del")
        assert count == 2
        assert repo.list_by_network_id("net-del") == []

    def test_update_verified_at(
        self, repo: IPTablesRuleRepository, db: Database
    ) -> None:
        """update_verified_at sets the last_verified_at timestamp."""
        _seed_network(db)
        rule = repo.insert(_make_rule(dport=3333))
        repo.update_verified_at(rule.id)
        retrieved = repo.get(rule.id)
        assert retrieved is not None
        assert retrieved.last_verified_at is not None
