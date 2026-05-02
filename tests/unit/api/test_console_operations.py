"""Tests for ConsoleOperation class — VM console relay orchestration."""

from __future__ import annotations

import pytest

from mvmctl.api.console_operations import ConsoleConnectionInfo, ConsoleOperation
from mvmctl.models.result import OperationResult
from mvmctl.exceptions import MVMError


class TestConsoleGetState:
    """Tests for ConsoleOperation.get_state()."""

    def test_returns_running_state(self, mocker):
        """get_state() returns running state when relay is active."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = True
        mock_resolved.relay.get_pid.return_value = 12345
        mock_resolved.relay.socket_path = "/tmp/console.sock"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved

        # Patch at the point of use
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        result = ConsoleOperation.get_state("test-vm")

        assert result["running"] is True
        assert result["pid"] == 12345
        assert result["socket_path"] == "/tmp/console.sock"

    def test_returns_not_running_state(self, mocker):
        """get_state() returns not-running state when relay is inactive."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = False
        mock_resolved.relay.get_pid.return_value = None
        mock_resolved.relay.socket_path = "/tmp/console.sock"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        result = ConsoleOperation.get_state("stopped-vm")

        assert result["running"] is False
        assert result["pid"] is None


class TestConsoleGetConnectionInfo:
    """Tests for ConsoleOperation.get_connection_info()."""

    def test_get_connection_info_success(self, mocker):
        """get_connection_info() returns ConsoleConnectionInfo when relay is running."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = True
        mock_resolved.relay.socket_path = "/tmp/console.sock"
        mock_resolved.vm.name = "test-vm"
        mock_resolved.vm.id = "test-vm-id"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        result = ConsoleOperation.get_connection_info("test-vm")

        assert isinstance(result, ConsoleConnectionInfo)
        assert result.socket_path == "/tmp/console.sock"
        assert result.vm_name == "test-vm"
        assert result.vm_id == "test-vm-id"

    def test_get_connection_info_raises_when_relay_not_running(self, mocker):
        """get_connection_info() raises MVMError when relay is not running."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = False
        mock_resolved.vm.name = "test-vm"
        mock_resolved.vm.id = "test-vm-id"

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        with pytest.raises(MVMError, match="No console relay running"):
            ConsoleOperation.get_connection_info("test-vm")


class TestConsoleKill:
    """Tests for ConsoleOperation.kill()."""

    def test_kill_success(self, mocker):
        """kill() returns True when relay is stopped."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = True
        mock_resolved.relay.terminate.return_value = True

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        result = ConsoleOperation.kill("test-vm")

        assert result.status == "success"
        mock_resolved.relay.terminate.assert_called_once()

    def test_kill_returns_false_when_not_running(self, mocker):
        """kill() returns False when relay is not running."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.relay.is_running.return_value = False

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.console_operations.ConsoleRequest",
            return_value=mock_request,
        )

        result = ConsoleOperation.kill("test-vm")

        assert result.status == "skipped"
        mock_resolved.relay.terminate.assert_not_called()
