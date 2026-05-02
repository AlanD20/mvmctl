"""Tests for CLI host commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import HostError
from mvmctl.main import app
from mvmctl.models import HostStateChangeItem, HostStateItem

runner = CliRunner()


def _make_state(initialized: bool = True) -> HostStateItem:
    return HostStateItem(
        id=1,
        initialized=initialized,
        mvm_group_created=True,
        sudoers_configured=True,
        default_network_created=True,
        initialized_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )


def _make_change(
    setting: str = "net.ipv4.ip_forward",
    mechanism: str = "sysctl",
    applied_value: str = "1",
) -> HostStateChangeItem:
    return HostStateChangeItem(
        session_id="sess-1",
        init_timestamp="2026-01-01T12:00:00+00:00",
        setting=setting,
        mechanism=mechanism,
        applied_value=applied_value,
        reverted=False,
        change_order=1,
        created_at="2026-01-01T12:00:00+00:00",
        original_value="0",
    )


class TestHostInit:
    """Tests for 'host init' command."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_with_changes(self, mock_host_op):
        from mvmctl.models.result import OperationResult
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            message="Host configured",
            metadata={
                "changes": [
                    _make_change(),
                ],
            },
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_already_configured(self, mock_host_op):
        from mvmctl.models.result import OperationResult
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="skipped",
            message="Host is already configured",
            metadata={},
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 0
        assert "already configured" in result.output.lower()

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_error(self, mock_host_op):
        from mvmctl.models.result import OperationResult
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="error",
            message="/dev/kvm not accessible",
            metadata={},
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "not accessible" in result.output

    def test_init_help(self):
        result = runner.invoke(app, ["host", "init", "--help"])
        assert result.exit_code == 0
        assert "host init" in result.output.lower()


class TestHostLs:
    """Tests for 'host ls' command."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_ls_all_ok(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "ls"])
        assert result.exit_code == 0
        assert "ok" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_ls_failures(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = False
        mock_host_op.check_required_binaries.return_value = ["iptables"]
        mock_host_op.get_ip_forward_status.return_value = "0"
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "ls"])
        assert result.exit_code == 0
        assert "FAIL" in result.output
        assert "iptables" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_ls_json(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["kvm_accessible"] is True
        assert data["ip_forward"]["ok"] is True

    @patch("mvmctl.cli.host.HostOperation")
    def test_ls_ip_forward_unknown(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.side_effect = HostError(
            "sysctl not found"
        )
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "ls"])
        assert result.exit_code == 0
        assert "unknown" in result.output.lower()

    def test_ls_help(self):
        result = runner.invoke(app, ["host", "ls", "--help"])
        assert result.exit_code == 0


class TestHostClean:
    """Tests for 'host clean' command."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_success(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = ["Removed network 'default'"]
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 0
        assert "cleaned" in result.output.lower()

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_with_running_vms(self, mock_host_op):
        mock_vm = MagicMock()
        mock_vm.name = "myvm"
        mock_host_op.get_running_vms.return_value = [mock_vm]
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1
        assert "Cannot clean" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_api_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.side_effect = HostError("clean failed")
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1


class TestHostReset:
    """Tests for 'host reset' command."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_success(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = [
            "Removed network 'default'",
            "Reverted ip_forward",
        ]
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 0
        assert "reset" in result.output.lower()

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_with_running_vms(self, mock_host_op):
        mock_vm = MagicMock()
        mock_vm.name = "myvm"
        mock_host_op.get_running_vms.return_value = [mock_vm]
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 1
        assert "Cannot reset" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_api_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.side_effect = HostError("reset failed")
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 1


class TestHostHelp:
    """Tests for host command group help."""

    def test_host_help(self):
        result = runner.invoke(app, ["host", "--help"])
        assert result.exit_code == 0
        assert "Host configuration" in result.output

    def test_host_help_command(self):
        result = runner.invoke(app, ["host", "help"])
        assert result.exit_code == 0
