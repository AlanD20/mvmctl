"""Tests for CLI host commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import HostError
from mvmctl.main import app
from mvmctl.models import HostStateChangeItem, HostStateItem
from mvmctl.models.result import NeedsInteraction, OperationResult

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
        mock_host_op.clean.return_value = OperationResult(
            status="success",
            code="host.cleaned",
            message="host cleaned",
            item=["Removed network 'default'"],
        )
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
        assert "blocked" in result.output

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
        mock_host_op.reset.return_value = OperationResult(
            status="success",
            code="host.reset",
            message="Host reset",
            item=[
                "Removed network 'default'",
                "Reverted ip_forward",
            ],
        )
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
        assert "blocked" in result.output

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


class TestHostInitPrivilege:
    """Tests for 'host init' privilege error paths."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_privilege_error_with_details(self, mock_host_op):
        from mvmctl.exceptions import PrivilegeError

        mock_host_op.init.side_effect = PrivilegeError(
            "Root privileges required",
            details={
                "message": "This command requires root",
                "suggestions": ["Run with sudo"],
            },
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Root privileges required" in result.output
        assert "This command requires root" in result.output
        assert "Run with sudo" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_privilege_error_no_details(self, mock_host_op):
        from mvmctl.exceptions import PrivilegeError

        mock_host_op.init.side_effect = PrivilegeError(
            "Insufficient permissions"
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Insufficient permissions" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_host_error(self, mock_host_op):
        mock_host_op.init.side_effect = HostError("KVM not available")
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Host init failed" in result.output


class TestHostInitNeedsInteraction:
    """Tests for 'host init' NeedsInteraction paths."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_sudo_required_declined(self, mock_host_op):
        mock_host_op.init.return_value = NeedsInteraction(
            code="privilege.sudo_required",
            message="Root required",
            input_type="sudo",
        )
        result = runner.invoke(app, ["host", "init"], input="n\n")
        assert result.exit_code == 1

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_sudo_confirm_recursion(self, mock_host_op, monkeypatch):
        mock_host_op.init.return_value = NeedsInteraction(
            code="privilege.sudo_required",
            message="Root required",
            input_type="sudo",
        )
        monkeypatch.setenv("MVM_SUDO_RESTART", "1")
        result = runner.invoke(app, ["host", "init"], input="y\n")
        assert result.exit_code == 1
        assert "Recursive sudo restart" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_needs_interaction_unknown_code(self, mock_host_op):
        mock_host_op.init.return_value = NeedsInteraction(
            code="some.other.code",
            message="Custom interaction needed",
            input_type="input",
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Custom interaction needed" in result.output


class TestHostInitEdgeCases:
    """Tests for 'host init' edge cases."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_unexpected_result_type(self, mock_host_op):
        mock_host_op.init.return_value = "surprise string"
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Unexpected result type" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_noop_changes_only(self, mock_host_op):
        noop_change = _make_change(
            setting="iptables_chains",
            mechanism="noop",
            applied_value="MVM chains already exist",
        )
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            metadata={"changes": [noop_change]},
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 0
        assert "already configured" in result.output.lower()

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_user_added_to_group(self, mock_host_op):
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            metadata={
                "changes": [_make_change()],
                "user_added_to_group": True,
            },
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 0
        assert "Log out and back in" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_failure_status(self, mock_host_op):
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="failure",
            message="Host init failed",
            metadata={},
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Host init failed" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_init_format_various_changes(self, mock_host_op):
        changes = [
            _make_change(
                mechanism="iptables_save",
                setting="iptables_rules",
                applied_value="/tmp/rules.v4",
            ),
            _make_change(
                mechanism="file_create",
                setting="sudoers",
                applied_value="/etc/sudoers.d/mvm",
            ),
            _make_change(
                mechanism="groupadd", setting="group", applied_value="mvm"
            ),
            _make_change(
                mechanism="modprobe",
                setting="kernel_module_load",
                applied_value="kvm",
            ),
            _make_change(
                mechanism="network_create",
                setting="default_network",
                applied_value="default",
            ),
        ]
        mock_host_op.init.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            metadata={"changes": changes},
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 0
        assert "iptables rules" in result.output
        assert "created" in result.output
        assert "group" in result.output
        assert "loaded kernel" in result.output
        assert "ready" in result.output


class TestHostCleanEdgeCases:
    """Tests for 'host clean' edge cases."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_result_is_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = OperationResult(
            status="error", code="host.clean.error", message="Clean failed"
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1
        assert "Clean failed" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_with_warning_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = OperationResult(
            status="success",
            code="host.cleaned",
            message="host cleaned",
            item=["Warning: bridge still in use", "Removed network 'default'"],
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 0
        assert "bridge still in use" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_confirmation_aborted(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        result = runner.invoke(app, ["host", "clean"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_no_summary_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = OperationResult(
            status="success", code="host.cleaned", message="Cleaned", item=[]
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.host.HostOperation")
    def test_clean_db_not_initialized(self, mock_host_op):
        mock_host_op.get_running_vms.side_effect = Exception(
            "DB not initialized"
        )
        mock_host_op.clean.return_value = OperationResult(
            status="success", code="host.cleaned", message="Cleaned"
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 0


class TestHostResetEdgeCases:
    """Tests for 'host reset' edge cases."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_result_is_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = OperationResult(
            status="error", code="host.reset.error", message="Reset failed"
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 1
        assert "Reset failed" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_with_warning_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = OperationResult(
            status="success",
            code="host.reset",
            message="Host reset",
            item=["Warning: iptables rules may remain"],
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 0
        assert "iptables rules may remain" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_confirmation_aborted(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        result = runner.invoke(app, ["host", "reset"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_no_summary_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = OperationResult(
            status="success", code="host.reset", message="Reset done", item=[]
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.host.HostOperation")
    def test_reset_db_not_initialized(self, mock_host_op):
        mock_host_op.get_running_vms.side_effect = Exception(
            "DB not initialized"
        )
        mock_host_op.reset.return_value = OperationResult(
            status="success", code="host.reset", message="Reset done"
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 0


class TestHostLsEdgeCases:
    """Tests for 'host ls' edge cases."""

    @patch("mvmctl.cli.host.HostOperation")
    def test_ls_get_state_raises(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.side_effect = HostError("DB error")
        result = runner.invoke(app, ["host", "ls"])
        assert result.exit_code == 0
        assert "none" in result.output.lower()
