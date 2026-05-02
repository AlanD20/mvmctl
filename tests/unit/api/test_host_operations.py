"""Tests for HostOperation — host management orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.host_operations import HostOperation
from mvmctl.exceptions import HostError
from mvmctl.models import VMStatus


class TestHostOperationDelegations:
    """Tests for simple delegation methods — verifies API boundary."""

    def test_get_state(self, mocker):
        """get_state() delegates to HostRepository.get_state()."""
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = {"initialized": True}
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_state() == {"initialized": True}
        mock_repo.get_state.assert_called_once()

    def test_get_state_none(self, mocker):
        """get_state() returns None when no state exists."""
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = None
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_state() is None

    def test_check_required_binaries(self, mocker):
        """check_required_binaries() delegates to HostService."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_required_binaries",
            return_value=[],
        )
        assert HostOperation.check_required_binaries() == []

    def test_check_required_binaries_missing(self, mocker):
        """check_required_binaries() returns missing binary list."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_required_binaries",
            return_value=["missing_bin"],
        )
        assert HostOperation.check_required_binaries() == ["missing_bin"]

    def test_get_ip_forward_status(self, mocker):
        """get_ip_forward_status() delegates to HostService."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService._get_ip_forward_status",
            return_value="1",
        )
        assert HostOperation.get_ip_forward_status() == "1"

    def test_get_ip_forward_status_disabled(self, mocker):
        """get_ip_forward_status() returns 0 when disabled."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService._get_ip_forward_status",
            return_value="0",
        )
        assert HostOperation.get_ip_forward_status() == "0"

    def test_get_running_vms(self, mocker):
        """get_running_vms() delegates to VMRepository."""
        mock_vms = [MagicMock(), MagicMock()]
        mock_repo = MagicMock()
        mock_repo.list_by_status.return_value = mock_vms
        mocker.patch(
            "mvmctl.api.host_operations.VMRepository",
            return_value=mock_repo,
        )
        result = HostOperation.get_running_vms()
        assert result == mock_vms
        mock_repo.list_by_status.assert_called_once_with(VMStatus.RUNNING)

    def test_get_running_vms_empty(self, mocker):
        """get_running_vms() returns empty list when none running."""
        mock_repo = MagicMock()
        mock_repo.list_by_status.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.VMRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_running_vms() == []


class TestHostOperationInit:
    """Tests for HostOperation.init() — the core host initialization flow."""

    def test_init_returns_needs_interaction_on_privilege_error(self, mocker):
        """init() returns NeedsInteraction when not root."""
        from mvmctl.exceptions import PrivilegeError

        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges",
            side_effect=PrivilegeError("not root"),
        )

        from mvmctl.models.result import NeedsInteraction

        result = HostOperation.init(Path("/tmp"))
        assert isinstance(result, NeedsInteraction)

    def test_init_calls_host_privilege_check(self, mocker):
        """init() checks privileges on /usr/sbin/ip."""
        mock_check = mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch("mvmctl.api.host_operations.Database")
        mocker.patch("mvmctl.api.host_operations.HostRepository")
        mocker.patch("mvmctl.api.host_operations.HostService")
        mocker.patch("mvmctl.api.host_operations.HostController")
        mocker.patch("mvmctl.api.host_operations.NetworkRepository")
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.detect_iptables_backend_conflict",
            return_value=(False, ""),
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_physical_interfaces",
            return_value=["eth0"],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.detect_outbound_interface",
            return_value="eth0",
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch("mvmctl.api.host_operations.FsUtils")
        mocker.patch("mvmctl.api.host_operations.subprocess")
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch("mvmctl.api.host_operations.AuditLog")
        mocker.patch("mvmctl.api.host_operations.HostService")

        HostOperation.init(Path("/tmp"))
        mock_check.assert_called_once_with(
            "/usr/sbin/ip", "initialize host"
        )


class TestHostOperationClean:
    """Tests for HostOperation.clean() — network cleanup orchestration."""

    def test_clean_calls_privilege_check(self, mocker):
        """clean() checks privileges before cleaning."""
        mock_check = mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch("mvmctl.api.host_operations.NetworkUtils")
        mocker.patch("mvmctl.api.host_operations.NetworkRepository")
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch("mvmctl.api.host_operations.SettingsService.resolve")
        mocker.patch("mvmctl.api.host_operations.AuditLog")
        mocker.patch("mvmctl.api.host_operations.subprocess")

        HostOperation.clean(Path("/tmp"))
        mock_check.assert_called_once_with("/usr/sbin/ip", "clean host")

    def test_clean_returns_summary_list(self, mocker):
        """clean() returns a summary list of actions."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.clean(Path("/tmp"))
        assert isinstance(result, list)

    def test_clean_handles_no_networks(self, mocker):
        """clean() works when no networks exist."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mock_audit = mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.clean(Path("/tmp"))
        assert isinstance(result, list)
        mock_audit.log.assert_called_once()

    def test_clean_handles_network_exception(self, mocker):
        """clean() handles NetworkRepository.list_all() exception gracefully."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mock_repo = MagicMock()
        mock_repo.list_all.side_effect = Exception("DB error")
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        # NetworkRepository.list_all() exception should not crash clean()
        result = HostOperation.clean(Path("/tmp"))
        assert isinstance(result, list)


class TestHostOperationReset:
    """Tests for HostOperation.reset() — full host reset."""

    def test_reset_calls_clean_then_restore(self, mocker):
        """reset() calls clean() then restores state."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=["Cleaned TAPs"],
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )

        result = HostOperation.reset(Path("/tmp"))
        assert "Cleaned TAPs" in result
        mock_service.restore_state.assert_called_once()


class TestHostOperationPrune:
    """Tests for HostOperation.prune() — teardown-only."""

    def test_prune_calls_clean_then_restore(self, mocker):
        """prune() calls clean() then restores state (no group/sudoers)."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=["Cleaned"],
        )
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = [MagicMock(setting="ip_forward")]
        mocker.patch(
            "mvmctl.api.host_operations.HostService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.prune(Path("/tmp"))
        assert "Cleaned" in result
        assert "ip_forward" in str(result)
        mock_service.restore_state.assert_called_once()

    def test_prune_logs_when_no_state(self, mocker):
        """prune() logs warning when restore_state fails."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=[],
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
        )
        mock_service = MagicMock()
        mock_service.restore_state.side_effect = HostError("No state saved")
        mocker.patch(
            "mvmctl.api.host_operations.HostService",
            return_value=mock_service,
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger")
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        HostOperation.prune(Path("/tmp"))
        mock_logger.warning.assert_called_once()
