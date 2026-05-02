"""Tests for IPTablesTracker chain operations — uncovered paths."""

from __future__ import annotations

import subprocess
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core._shared._iptables_tracker import (
    IPTablesRuleRepository,
    IPTablesTracker,
)
from mvmctl.exceptions import IPTablesTrackerError
from mvmctl.models import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRuleItem,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
)

_VALID_RULE_TYPE = IPTablesRuleType.FORWARD_IN
_VALID_CHAIN = IPTablesChain.MVM_FORWARD


@pytest.fixture
def db() -> Database:
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def repo(db: Database) -> IPTablesRuleRepository:
    return IPTablesRuleRepository(db)


@pytest.fixture
def tracker(repo: IPTablesRuleRepository) -> IPTablesTracker:
    return IPTablesTracker(repo)


def _make_rule(**kwargs: object) -> IPTablesRuleItem:
    defaults: dict[str, object] = dict(
        table_name=IPTablesTable.FILTER,
        chain_name=_VALID_CHAIN,
        rule_type=_VALID_RULE_TYPE,
        protocol=IPTablesProtocol.TCP,
        source="10.0.0.10",
        destination="10.0.0.1",
        in_interface="tap0",
        out_interface=IPTablesWildcard.ANY_INTERFACE,
        target=IPTablesTarget.ACCEPT,
        sport=IPTablesPort.ANY,
        dport=8080,
        network_id="net-test",
        is_active=True,
        comment_tag="mvm:test:net-test",
    )
    defaults.update(kwargs)
    return IPTablesRuleItem(**defaults)


class TestEnsureChainWithJumpRule:
    """Uncovered paths for ensure_chain with auto_jump_from."""

    def test_creates_chain_with_jump_rule(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain creates chain and adds jump rule when auto_jump_from is set."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                # -L check fails (chain doesn't exist)
                return MagicMock(returncode=1)
            if call_count[0] == 2:
                # -N create succeeds
                return MagicMock(returncode=0)
            if call_count[0] == 3:
                # -C check for jump rule fails (doesn't exist)
                return MagicMock(returncode=1)
            # -I insert jump rule succeeds
            return MagicMock(returncode=0)

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker.ensure_chain(
                _VALID_CHAIN,
                table=IPTablesTable.FILTER,
                auto_jump_from="FORWARD",
                position=1,
            )
        assert result is True

    def test_raises_when_jump_rule_fails(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain raises IPTablesTrackerError when jump rule creation fails."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)  # -L check fails
            if call_count[0] == 2:
                return MagicMock(returncode=0)  # -N create succeeds
            if call_count[0] == 3:
                return MagicMock(returncode=1)  # -C check fails
            # -I insert jump rule fails
            raise subprocess.CalledProcessError(
                1, cmd, stderr=b"iptables: No chain/target/match by that name"
            )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            with pytest.raises(
                IPTablesTrackerError, match="Failed to add jump rule"
            ):
                tracker.ensure_chain(
                    _VALID_CHAIN,
                    table=IPTablesTable.FILTER,
                    auto_jump_from="FORWARD",
                    position=1,
                )

    def test_handles_called_process_error_with_str_stderr(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_chain handles CalledProcessError where stderr is already a str."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)  # Check fails
            # Create the error with string stderr (not bytes)
            error = subprocess.CalledProcessError(1, cmd)
            error.stderr = "Chain already exists"
            raise error

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker.ensure_chain(
                _VALID_CHAIN, table=IPTablesTable.FILTER
            )
        assert result is False


class TestEnsureJumpRuleFailure:
    """Uncovered failure paths for ensure_jump_rule."""

    def test_returns_failure_on_insert_error(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_jump_rule returns failure result when insert fails."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)  # -C check fails
            # -I insert fails
            raise subprocess.CalledProcessError(
                1, cmd, stderr=b"iptables: Resource temporarily unavailable"
            )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker.ensure_jump_rule(
                "INPUT",
                _VALID_CHAIN.value,
                table=IPTablesTable.FILTER,
                position=1,
            )
        assert result.success is False
        assert result.error_message is not None
        assert "Resource temporarily unavailable" in result.error_message

    def test_handles_string_stderr_on_insert_failure(
        self, tracker: IPTablesTracker
    ) -> None:
        """ensure_jump_rule handles CalledProcessError with string stderr."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(returncode=1)
            error = subprocess.CalledProcessError(1, cmd)
            error.stderr = "Permission denied (you must be root)"
            raise error

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker.ensure_jump_rule(
                "INPUT",
                _VALID_CHAIN.value,
                table=IPTablesTable.FILTER,
                position=1,
            )
        assert result.success is False


class TestRemoveChain:
    """Uncovered failure paths for remove_chain."""

    def test_raises_on_delete_failure(self, tracker: IPTablesTracker) -> None:
        """remove_chain raises IPTablesTrackerError when iptables -X fails."""
        with (
            patch(
                "mvmctl.utils.network.NetworkUtils.chain_exists",
                return_value=True,
            ),
            patch(
                "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
                side_effect=subprocess.CalledProcessError(
                    1,
                    ["iptables", "-t", "filter", "-X", "MVM-FORWARD"],
                    stderr=b"iptables: Permission denied",
                ),
            ),
        ):
            with pytest.raises(IPTablesTrackerError, match="Failed to delete"):
                tracker.remove_chain(_VALID_CHAIN)

    def test_handles_empty_stderr_on_delete_failure(
        self, tracker: IPTablesTracker
    ) -> None:
        """remove_chain handles CalledProcessError with empty stderr."""
        with (
            patch(
                "mvmctl.utils.network.NetworkUtils.chain_exists",
                return_value=True,
            ),
            patch(
                "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
                side_effect=subprocess.CalledProcessError(
                    1,
                    ["iptables", "-t", "filter", "-X", "MVM-FORWARD"],
                ),
            ),
        ):
            with pytest.raises(IPTablesTrackerError):
                tracker.remove_chain(_VALID_CHAIN)


class TestRemoveByLineNumber:
    """Direct tests for _remove_by_line_number."""

    IPTABLES_LIST_OUTPUT = (
        "Chain MVM-FORWARD (policy ACCEPT 0 packets, 0 bytes)\n"
        "num   pkts bytes target     prot opt in     out     source        destination\n"
        "1        0     0 ACCEPT     all  --  tap0   *       10.0.0.10     10.0.0.1\n"
        "2        0     0 ACCEPT     all  --  tap1   eth0   10.0.0.20     0.0.0.0/0\n"
        "3       42  1234 DROP       tcp  --  *      *      0.0.0.0/0     0.0.0.0/0\n"
    )

    def test_removes_by_line_number_success(
        self, tracker: IPTablesTracker
    ) -> None:
        """_remove_by_line_number finds matching rule and deletes by line number."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                # -L with line numbers returns our fixture output
                return MagicMock(
                    returncode=0,
                    stdout=self.IPTABLES_LIST_OUTPUT,
                    stderr="",
                )
            # -D by line number succeeds
            return MagicMock(returncode=0)

        rule = _make_rule(
            in_interface="tap0", out_interface=IPTablesWildcard.ANY_INTERFACE
        )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker._remove_by_line_number(rule)
        assert result is True

    def test_no_matching_line_number(self, tracker: IPTablesTracker) -> None:
        """_remove_by_line_number returns False when no rule matches."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(
                returncode=0,
                stdout=self.IPTABLES_LIST_OUTPUT,
                stderr="",
            ),
        ):
            rule = _make_rule(
                in_interface="tap99",
                out_interface=IPTablesWildcard.ANY_INTERFACE,
            )
            result = tracker._remove_by_line_number(rule)
        assert result is False

    def test_list_command_fails(self, tracker: IPTablesTracker) -> None:
        """_remove_by_line_number returns False when iptables -L fails."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(returncode=1, stdout="", stderr="error"),
        ):
            rule = _make_rule()
            result = tracker._remove_by_line_number(rule)
        assert result is False

    def test_line_num_delete_fails(self, tracker: IPTablesTracker) -> None:
        """_remove_by_line_number returns False when line-number delete fails."""
        call_count: list[int] = [0]

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            call_count[0] += 1
            if call_count[0] == 1:
                return MagicMock(
                    returncode=0,
                    stdout=self.IPTABLES_LIST_OUTPUT,
                    stderr="",
                )
            # line-number delete fails
            return MagicMock(returncode=1)

        rule = _make_rule(
            in_interface="tap0", out_interface=IPTablesWildcard.ANY_INTERFACE
        )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker._remove_by_line_number(rule)
        assert result is False

    def test_matches_wildcard_interfaces(
        self, tracker: IPTablesTracker
    ) -> None:
        """_remove_by_line_number matches wildcard ANY_INTERFACE as '*'."""
        output_with_wildcard = (
            "Chain MVM-FORWARD (policy ACCEPT 0 packets, 0 bytes)\n"
            "num   pkts bytes target     prot opt in     out     source        destination\n"
            "1        0     0 ACCEPT     all  --  *      *       0.0.0.0/0     0.0.0.0/0\n"
        )

        def _mock_run(cmd: list[str], **kwargs: object) -> MagicMock:
            if "--line-numbers" in cmd:
                return MagicMock(
                    returncode=0,
                    stdout=output_with_wildcard,
                    stderr="",
                )
            return MagicMock(returncode=0)

        rule = _make_rule(
            in_interface=IPTablesWildcard.ANY_INTERFACE,
            out_interface=IPTablesWildcard.ANY_INTERFACE,
        )

        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            side_effect=_mock_run,
        ):
            result = tracker._remove_by_line_number(rule)
        assert result is True


class TestRuleExistsByInterfaces:
    """Direct tests for _rule_exists_by_interfaces.

    Note: This method uses iptables -L -v (without --line-numbers).
    The column layout is: pkts bytes target prot opt in out source dest.
    The code indexes parts[6]=out, parts[7]=source (de-facto indexing).
    """

    def test_rule_exists(self, tracker: IPTablesTracker) -> None:
        """_rule_exists_by_interfaces returns True when matching rule found."""
        output = (
            "Chain MVM-FORWARD (policy ACCEPT 0 packets, 0 bytes)\n"
            "pkts bytes target     prot opt in     out     source        destination\n"
            "0     0 ACCEPT     all  --  eth0   tap0    *              10.0.0.1\n"
        )
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(
                returncode=0,
                stdout=output,
                stderr="",
            ),
        ):
            rule = _make_rule(
                in_interface="tap0",
                out_interface=IPTablesWildcard.ANY_INTERFACE,
            )
            result = tracker._rule_exists_by_interfaces(rule)
        assert result is True

    def test_rule_not_exists(self, tracker: IPTablesTracker) -> None:
        """_rule_exists_by_interfaces returns False when no match."""
        output = (
            "Chain MVM-FORWARD (policy ACCEPT 0 packets, 0 bytes)\n"
            "pkts bytes target     prot opt in     out     source        destination\n"
            "0     0 ACCEPT     all  --  eth0   tap0    *              10.0.0.1\n"
        )
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(
                returncode=0,
                stdout=output,
                stderr="",
            ),
        ):
            rule = _make_rule(
                in_interface="tap99",
                out_interface=IPTablesWildcard.ANY_INTERFACE,
            )
            result = tracker._rule_exists_by_interfaces(rule)
        assert result is False

    def test_list_command_fails(self, tracker: IPTablesTracker) -> None:
        """_rule_exists_by_interfaces returns False when iptables -L fails."""
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(returncode=1, stdout="", stderr=""),
        ):
            rule = _make_rule()
            result = tracker._rule_exists_by_interfaces(rule)
        assert result is False

    def test_matches_wildcard_interfaces(
        self, tracker: IPTablesTracker
    ) -> None:
        """_rule_exists_by_interfaces matches wildcard ANY_INTERFACE as '*'."""
        output = (
            "Chain MVM-FORWARD (policy ACCEPT 0 packets, 0 bytes)\n"
            "pkts bytes target     prot opt in     out     source        destination\n"
            "0     0 ACCEPT     all  --  eth0   *       *              0.0.0.0/0\n"
        )
        with patch(
            "mvmctl.core._shared._iptables_tracker._tracker.subprocess.run",
            return_value=MagicMock(returncode=0, stdout=output, stderr=""),
        ):
            rule = _make_rule(
                in_interface=IPTablesWildcard.ANY_INTERFACE,
                out_interface=IPTablesWildcard.ANY_INTERFACE,
            )
            result = tracker._rule_exists_by_interfaces(rule)
        assert result is True


class TestBuildIptablesArgsEdgeCases:
    """Edge cases for _build_iptables_args."""

    def test_excludes_wildcard_protocol(self, tracker: IPTablesTracker) -> None:
        """ALL protocol should not add -p flag."""
        rule = _make_rule(protocol=IPTablesProtocol.ALL)
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.CHECK
        )
        assert "-p" not in args

    def test_excludes_wildcard_source(self, tracker: IPTablesTracker) -> None:
        """ANY_CIDR source should not add -s flag."""
        rule = _make_rule(source=IPTablesWildcard.ANY_CIDR)
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.CHECK
        )
        assert "-s" not in args

    def test_excludes_wildcard_destination(
        self, tracker: IPTablesTracker
    ) -> None:
        """ANY_CIDR destination should not add -d flag."""
        rule = _make_rule(destination=IPTablesWildcard.ANY_CIDR)
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.CHECK
        )
        assert "-d" not in args

    def test_includes_all_specific_fields(
        self, tracker: IPTablesTracker
    ) -> None:
        """All specific fields should be included in args."""
        rule = _make_rule(
            protocol=IPTablesProtocol.TCP,
            source="10.0.0.1",
            destination="10.0.0.2",
            in_interface="eth0",
            out_interface="eth1",
            sport=1234,
            dport=5678,
            comment_tag="mvm:test:tag",
        )
        args = tracker._build_iptables_args(
            rule, IPTablesTracker.RuleAction.APPEND
        )
        assert "-p" in args and "tcp" in args
        assert "-s" in args and "10.0.0.1" in args
        assert "-d" in args and "10.0.0.2" in args
        assert "-i" in args and "eth0" in args
        assert "-o" in args and "eth1" in args
        assert "--sport" in args and "1234" in args
        assert "--dport" in args and "5678" in args
        assert "-j" in args and "ACCEPT" in args
        assert "--comment" in args and "mvm:test:tag" in args
