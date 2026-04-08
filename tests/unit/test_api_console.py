"""Tests for API layer console functions in api/vms.py."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api import vms
from mvmctl.exceptions import MVMError, VMNotFoundError
from mvmctl.models.vm import ConsoleInfo, ConsoleState, VMInstance, VMStatus


class TestAttachConsole:
    """Tests for attach_console function."""

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_attach_console_success(self, mock_get_manager, mock_console_mgr_cls):
        """attach_console returns socket_path when VM exists and relay is running."""
        # Setup mock VM manager
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.RUNNING)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        # Setup mock ConsoleRelayManager
        mock_mgr = MagicMock()
        mock_console_mgr_cls.return_value = mock_mgr
        mock_mgr.is_relay_running.return_value = True
        mock_socket_path = Path("/tmp/mvm-testvm/consoles/console.sock")
        mock_mgr.get_socket_path.return_value = mock_socket_path

        # Call function
        result = vms.attach_console("testvm")

        # Assertions
        assert result.vm_name == "testvm"
        assert result.socket_path == mock_socket_path
        mock_manager.get.assert_called_once_with("testvm")
        mock_mgr.is_relay_running.assert_called_once_with("testvm", None)
        mock_mgr.get_socket_path.assert_called_once_with("testvm")

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_attach_console_vm_not_found(self, mock_get_manager, mock_console_mgr_cls):
        """attach_console raises VMNotFoundError when VM does not exist."""
        mock_manager = MagicMock()
        mock_manager.get.return_value = None
        mock_get_manager.return_value = mock_manager

        with pytest.raises(VMNotFoundError) as exc_info:
            vms.attach_console("nonexistent")

        assert "nonexistent" in str(exc_info.value)

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_attach_console_relay_not_running(self, mock_get_manager, mock_console_mgr_cls):
        """attach_console raises MVMError when no relay is running."""
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.RUNNING)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        mock_mgr = MagicMock()
        mock_console_mgr_cls.return_value = mock_mgr
        mock_mgr.is_relay_running.return_value = False

        with pytest.raises(MVMError) as exc_info:
            vms.attach_console("testvm")

        assert "No console relay running" in str(exc_info.value)


class TestKillConsole:
    """Tests for kill_console function."""

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_kill_console_success(self, mock_get_manager, mock_console_mgr_cls):
        """kill_console returns True when VM exists and relay is killed."""
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.RUNNING)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        mock_mgr = MagicMock()
        mock_console_mgr_cls.return_value = mock_mgr
        mock_mgr.kill_relay.return_value = True

        result = vms.kill_console("testvm")

        assert result is True
        mock_manager.get.assert_called_once_with("testvm")
        mock_mgr.kill_relay.assert_called_once_with("testvm", None)

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_kill_console_no_relay_running(self, mock_get_manager, mock_console_mgr_cls):
        """kill_console returns False when VM exists but no relay is running."""
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.RUNNING)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        mock_mgr = MagicMock()
        mock_console_mgr_cls.return_value = mock_mgr
        mock_mgr.kill_relay.return_value = False

        result = vms.kill_console("testvm")

        assert result is False

    @patch("mvmctl.api.vms.ConsoleRelayManager")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_kill_console_vm_not_found(self, mock_get_manager, mock_console_mgr_cls):
        """kill_console raises VMNotFoundError when VM does not exist."""
        mock_manager = MagicMock()
        mock_manager.get.return_value = None
        mock_get_manager.return_value = mock_manager

        with pytest.raises(VMNotFoundError) as exc_info:
            vms.kill_console("nonexistent")

        assert "nonexistent" in str(exc_info.value)


class TestGetConsoleState:
    """Tests for get_console_state function."""

    @patch("mvmctl.api.vms._get_console_state")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_get_console_state_relay_running(self, mock_get_manager, mock_core_get_state):
        """get_console_state returns state when VM exists and relay is running."""
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.RUNNING)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        mock_core_get_state.return_value = {
            "running": True,
            "pid": 12345,
            "socket_path": "/tmp/mvm-testvm/console.sock",
        }

        result = vms.get_console_state("testvm")

        assert result.running is True
        assert result.pid == 12345
        assert result.socket_path == "/tmp/mvm-testvm/console.sock"
        mock_manager.get.assert_called_once_with("testvm")
        mock_core_get_state.assert_called_once_with("testvm", None)

    @patch("mvmctl.api.vms._get_console_state")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_get_console_state_relay_not_running(self, mock_get_manager, mock_core_get_state):
        """get_console_state returns state when VM exists but relay is not running."""
        mock_manager = MagicMock()
        mock_vm = VMInstance(name="testvm", status=VMStatus.STOPPED)
        mock_manager.get.return_value = mock_vm
        mock_get_manager.return_value = mock_manager

        mock_core_get_state.return_value = {
            "running": False,
            "pid": None,
            "socket_path": "/tmp/mvm-testvm/console.sock",
        }

        result = vms.get_console_state("testvm")

        assert result.running is False
        assert result.pid is None

    @patch("mvmctl.api.vms._get_console_state")
    @patch("mvmctl.api.vms.get_vm_manager")
    def test_get_console_state_vm_not_found(self, mock_get_manager, mock_core_get_state):
        """get_console_state raises VMNotFoundError when VM does not exist."""
        mock_manager = MagicMock()
        mock_manager.get.return_value = None
        mock_get_manager.return_value = mock_manager

        with pytest.raises(VMNotFoundError) as exc_info:
            vms.get_console_state("nonexistent")

        assert "nonexistent" in str(exc_info.value)
        mock_core_get_state.assert_not_called()


class TestReExports:
    """Tests to verify core console functions are properly re-exported."""

    def test_api_exports_check_escape_sequence(self):
        """check_escape_sequence should be exported from api.vms."""
        assert hasattr(vms, "check_escape_sequence")
        assert vms.check_escape_sequence is not None

    def test_api_exports_connect_to_relay(self):
        """connect_to_relay should be exported from api.vms."""
        assert hasattr(vms, "connect_to_relay")
        assert vms.connect_to_relay is not None

    def test_api_exports_disconnect_from_relay(self):
        """disconnect_from_relay should be exported from api.vms."""
        assert hasattr(vms, "disconnect_from_relay")
        assert vms.disconnect_from_relay is not None

    def test_api_exports_read_console_output(self):
        """read_console_output should be exported from api.vms."""
        assert hasattr(vms, "read_console_output")
        assert vms.read_console_output is not None

    def test_api_exports_send_console_input(self):
        """send_console_input should be exported from api.vms."""
        assert hasattr(vms, "send_console_input")
        assert vms.send_console_input is not None

    def test_api_all_includes_console_functions(self):
        """All console functions should be in __all__."""
        expected_exports = [
            "attach_console",
            "kill_console",
            "get_console_state",
            "check_escape_sequence",
            "connect_to_relay",
            "disconnect_from_relay",
            "read_console_output",
            "send_console_input",
        ]
        for func_name in expected_exports:
            assert func_name in vms.__all__, f"{func_name} should be in __all__"

    def test_re_exports_are_correct_functions(self):
        """Verify re-exported functions are the actual core functions."""
        from mvmctl.core.console import (
            check_escape_sequence as core_check_escape,
        )
        from mvmctl.core.console import (
            connect_to_relay as core_connect,
        )
        from mvmctl.core.console import (
            disconnect_from_relay as core_disconnect,
        )
        from mvmctl.core.console import (
            read_console_output as core_read,
        )
        from mvmctl.core.console import (
            send_console_input as core_send,
        )

        assert vms.check_escape_sequence is core_check_escape
        assert vms.connect_to_relay is core_connect
        assert vms.disconnect_from_relay is core_disconnect
        assert vms.read_console_output is core_read
        assert vms.send_console_input is core_send
