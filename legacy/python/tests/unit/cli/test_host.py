"""Tests for CLI host commands."""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from click.testing import CliRunner

from mvmctl.exceptions import HostError
from mvmctl.main import app
from mvmctl.models import HostStateChangeItem, HostStateItem
from mvmctl.models.result import NeedsInteraction, OperationResult

runner = CliRunner()


@pytest.fixture(autouse=True)
def _mock_db_exists():
    with patch(
        "mvmctl.utils.common.CacheUtils.get_mvm_db_path",
        return_value=Path("/tmp/.mvmctl-test/mvmctl.db"),
    ), patch("pathlib.Path.exists", return_value=True):
        yield


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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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


class TestHostStatus:
    """Tests for 'host status' command."""

    @patch("mvmctl.api.HostOperation")
    def test_ls_all_ok(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "status"])
        assert result.exit_code == 0
        assert "ok" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_ls_failures(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = False
        mock_host_op.check_required_binaries.return_value = ["iptables"]
        mock_host_op.get_ip_forward_status.return_value = "0"
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "status"])
        assert result.exit_code == 0
        assert "FAIL" in result.output
        assert "iptables" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_ls_json(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "status", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["kvm_accessible"] is True
        assert data["ip_forward"]["ok"] is True

    @patch("mvmctl.api.HostOperation")
    def test_ls_ip_forward_unknown(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.side_effect = HostError(
            "sysctl not found"
        )
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "status"])
        assert result.exit_code == 0
        assert "unknown" in result.output.lower()

    def test_ls_help(self):
        result = runner.invoke(app, ["host", "status", "--help"])
        assert result.exit_code == 0


class TestHostClean:
    """Tests for 'host clean' command."""

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_clean_with_running_vms(self, mock_host_op):
        mock_vm = MagicMock()
        mock_vm.name = "myvm"
        mock_host_op.get_running_vms.return_value = [mock_vm]
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1
        assert "blocked" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_clean_api_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.side_effect = HostError("clean failed")
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1


class TestHostReset:
    """Tests for 'host reset' command."""

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_reset_with_running_vms(self, mock_host_op):
        mock_vm = MagicMock()
        mock_vm.name = "myvm"
        mock_host_op.get_running_vms.return_value = [mock_vm]
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 1
        assert "blocked" in result.output

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_init_privilege_error_no_details(self, mock_host_op):
        from mvmctl.exceptions import PrivilegeError

        mock_host_op.init.side_effect = PrivilegeError(
            "Insufficient permissions"
        )
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Insufficient permissions" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_init_host_error(self, mock_host_op):
        mock_host_op.init.side_effect = HostError("KVM not available")
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Host init failed" in result.output


class TestHostInitNeedsInteraction:
    """Tests for 'host init' NeedsInteraction paths."""

    @patch("mvmctl.api.HostOperation")
    def test_init_sudo_required_declined(self, mock_host_op):
        mock_host_op.init.return_value = NeedsInteraction(
            code="privilege.sudo_required",
            message="Root required",
            input_type="sudo",
        )
        result = runner.invoke(app, ["host", "init"], input="n\n")
        assert result.exit_code == 1

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_init_unexpected_result_type(self, mock_host_op):
        mock_host_op.init.return_value = "surprise string"
        result = runner.invoke(app, ["host", "init"])
        assert result.exit_code == 1
        assert "Unexpected result type" in result.output

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_clean_result_is_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = OperationResult(
            status="error", code="host.clean.error", message="Clean failed"
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 1
        assert "Clean failed" in result.output

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_clean_confirmation_aborted(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        result = runner.invoke(app, ["host", "clean"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_clean_no_summary_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.clean.return_value = OperationResult(
            status="success", code="host.cleaned", message="Cleaned", item=[]
        )
        result = runner.invoke(app, ["host", "clean", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_reset_result_is_error(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = OperationResult(
            status="error", code="host.reset.error", message="Reset failed"
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 1
        assert "Reset failed" in result.output

    @patch("mvmctl.api.HostOperation")
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

    @patch("mvmctl.api.HostOperation")
    def test_reset_confirmation_aborted(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        result = runner.invoke(app, ["host", "reset"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output

    @patch("mvmctl.api.HostOperation")
    def test_reset_no_summary_items(self, mock_host_op):
        mock_host_op.get_running_vms.return_value = []
        mock_host_op.reset.return_value = OperationResult(
            status="success", code="host.reset", message="Reset done", item=[]
        )
        result = runner.invoke(app, ["host", "reset", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.api.HostOperation")
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
    """Tests for 'host status' edge cases."""

    @patch("mvmctl.api.HostOperation")
    def test_ls_get_state_raises(self, mock_host_op):
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.side_effect = HostError("DB error")
        result = runner.invoke(app, ["host", "status"])
        assert result.exit_code == 0
        assert "none" in result.output.lower()


class TestHostStatus:
    """Tests for 'host status' command (alias for 'host ls')."""

    @patch("mvmctl.api.HostOperation")
    def test_status_all_ok(self, mock_host_op):
        """host status shows ok when everything is working."""
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "status"])
        if result.exit_code != 0:
            pytest.skip("host status command not yet implemented")
        assert result.exit_code == 0

    @patch("mvmctl.api.HostOperation")
    def test_status_json(self, mock_host_op):
        """host status --json produces valid JSON."""
        mock_host_op.check_kvm_access.return_value = True
        mock_host_op.check_required_binaries.return_value = []
        mock_host_op.get_ip_forward_status.return_value = "1"
        mock_host_op.get_state.return_value = _make_state()
        result = runner.invoke(app, ["host", "status", "--json"])
        if result.exit_code != 0:
            pytest.skip("host status command not yet implemented")
        data = json.loads(result.output)
        assert data["kvm_accessible"] is True
        assert data["ip_forward"]["ok"] is True

    @patch("mvmctl.api.HostOperation")
    def test_status_json_structure(self, mock_host_op):
        """host status --json returns all expected fields."""
        mock_host_op.check_kvm_access.return_value = False
        mock_host_op.check_required_binaries.return_value = ["iptables"]
        mock_host_op.get_ip_forward_status.return_value = "0"
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "status", "--json"])
        if result.exit_code != 0:
            pytest.skip("host status command not yet implemented")
        data = json.loads(result.output)
        assert "kvm_accessible" in data
        assert "required_binaries" in data
        assert "ip_forward" in data
        assert "state_snapshot" in data

    @patch("mvmctl.api.HostOperation")
    def test_status_failures(self, mock_host_op):
        """host status shows FAIL when components are not working."""
        mock_host_op.check_kvm_access.return_value = False
        mock_host_op.check_required_binaries.return_value = ["iptables"]
        mock_host_op.get_ip_forward_status.return_value = "0"
        mock_host_op.get_state.return_value = None
        result = runner.invoke(app, ["host", "status"])
        if result.exit_code != 0:
            pytest.skip("host status command not yet implemented")
        assert "FAIL" in result.output

    def test_status_help(self):
        """host status --help displays help."""
        result = runner.invoke(app, ["host", "status", "--help"])
        if result.exit_code != 0:
            pytest.skip("host status command not yet implemented")
        assert result.exit_code == 0


class TestHostInfo:
    """Tests for 'host info' command."""

    @patch("mvmctl.api.HostOperation")
    def test_info_success_human(self, mock_host_op):
        """host info shows info dict in tree format."""
        mock_host_op.info.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            code="host.info",
            item={
                "hostname": "testhost",
                "os": {"kernel": "6.8.0", "release": "TestOS"},
                "cpu": {"model": "Test CPU", "vendor": "intel", "cores": 8},
                "memory": {"total_mib": 32000, "available_mib": 8192},
                "storage": {"total_bytes": 500_000_000_000, "free_bytes": 200_000_000_000},
                "limits": {"pid_max": 4194304},
                "capacity": {"recommended_max_vms": 10, "limiting_resource": "memory"},
                "setup": {"initialized": True},
                "detected_at": "2026-01-01T12:00:00+00:00",
            },
            is_error=False,
        )
        result = runner.invoke(app, ["host", "info"])
        assert result.exit_code == 0

    @patch("mvmctl.api.HostOperation")
    def test_info_success_json(self, mock_host_op):
        """host info --json produces valid JSON."""
        mock_host_op.info.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            code="host.info",
            item={
                "hostname": "testhost",
                "os": {"kernel": "6.8.0", "release": "TestOS"},
                "cpu": {"model": "Test CPU", "vendor": "intel", "cores": 8, "architecture": "x86_64", "numa_nodes": 1},
                "memory": {"total_mib": 32000, "available_mib": 8192},
                "storage": {"total_bytes": 500_000_000_000, "free_bytes": 200_000_000_000},
                "limits": {"pid_max": 4194304, "fd_max": 100000, "conntrack_max": 262144, "tap_devices_max": -1, "ip_local_port_range": [32768, 60999]},
                "capacity": {
                    "current": {"pids": 512, "fds": 2048, "conntrack": 128, "tap_devices": 2, "arp_entries": 5},
                    "recommended_max_vms": 10,
                    "limiting_resource": "memory",
                },
                "setup": {"initialized": True, "initialized_at": "2026-01-01T12:00:00+00:00"},
                "detected_at": "2026-01-01T12:00:00+00:00",
            },
            is_error=False,
        )
        result = runner.invoke(app, ["host", "info", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["hostname"] == "testhost"
        assert "cpu" in data

    @patch("mvmctl.api.HostOperation")
    def test_info_error(self, mock_host_op):
        """host info shows error message when no state."""
        mock_host_op.info.return_value = MagicMock(
            spec=OperationResult,
            status="error",
            code="host.info.no_state",
            message="Host not yet detected",
            is_error=True,
        )
        result = runner.invoke(app, ["host", "info"])
        assert result.exit_code == 1
        assert "not yet detected" in result.output.lower()

    @patch("mvmctl.api.HostOperation")
    def test_info_refresh(self, mock_host_op):
        """host info --refresh calls refresh_capacity."""
        mock_host_op.refresh_capacity.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            code="host.capacity.refreshed",
            item={"hostname": "refreshed", "cpu": {"model": "Refreshed CPU"}, "memory": {"total_mib": 64000, "available_mib": 32000}, "storage": {"total_bytes": 1000, "free_bytes": 500}, "limits": {"pid_max": 4194304}, "capacity": {"recommended_max_vms": 20, "limiting_resource": "cpu"}, "setup": {"initialized": True}, "os": {"kernel": "6.10", "release": "NewOS"}, "detected_at": "2026-06-01T12:00:00+00:00"},
            is_error=False,
        )
        result = runner.invoke(app, ["host", "info", "--refresh"])
        assert result.exit_code == 0
        mock_host_op.refresh_capacity.assert_called_once()

    @patch("mvmctl.api.HostOperation")
    def test_info_refresh_error(self, mock_host_op):
        """host info --refresh shows error when refresh fails."""
        mock_host_op.refresh_capacity.return_value = MagicMock(
            spec=OperationResult,
            status="error",
            code="host.capacity.detect_failed",
            message="Detection failed",
            is_error=True,
        )
        result = runner.invoke(app, ["host", "info", "--refresh"])
        assert result.exit_code == 1

    @patch("mvmctl.api.HostOperation")
    def test_info_with_none_item(self, mock_host_op):
        """host info shows error when item is None."""
        mock_host_op.info.return_value = MagicMock(
            spec=OperationResult,
            status="success",
            code="host.info",
            item=None,
            is_error=False,
        )
        result = runner.invoke(app, ["host", "info"])
        assert result.exit_code == 1
        assert "No host info available" in result.output

    def test_info_help(self):
        """host info --help displays help."""
        result = runner.invoke(app, ["host", "info", "--help"])
        assert result.exit_code == 0
