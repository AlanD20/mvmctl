"""Tests for CLI console command."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
import typer
from typer.testing import CliRunner

from mvmctl.cli.console import _do_attach, _do_kill, _show_state
from mvmctl.exceptions import VMNotFoundError, MVMError
from mvmctl.models import ConsoleInfo, ConsoleState

runner = CliRunner()


class TestShowStateFunction:
    @patch("mvmctl.cli.console.print_info")
    @patch("mvmctl.cli.console._get_console_state")
    def test_show_state_prints_running_status(self, mock_get_state, mock_print):
        mock_get_state.return_value = ConsoleState(
            running=True,
            pid=12345,
            socket_path="/tmp/test.sock",
        )

        _show_state("testvm")

        mock_print.assert_any_call("Console for 'testvm': running")
        mock_print.assert_any_call("  PID: 12345")

    @patch("mvmctl.cli.console.print_error")
    @patch("mvmctl.cli.console._get_console_state")
    def test_show_state_handles_vm_not_found(self, mock_get_state, mock_print):
        mock_get_state.side_effect = VMNotFoundError("VM 'nonexistent' not found")

        with pytest.raises(typer.Exit) as exc_info:
            _show_state("nonexistent")
        assert exc_info.value.exit_code == 1
        mock_print.assert_called_once_with("VM 'nonexistent' not found")


class TestDoKillFunction:
    @patch("mvmctl.cli.console.print_success")
    @patch("mvmctl.cli.console._kill_console")
    def test_do_kill_prints_success(self, mock_kill, mock_print):
        mock_kill.return_value = True

        _do_kill("testvm")

        mock_print.assert_called_once_with("Console relay stopped for 'testvm'")

    @patch("mvmctl.cli.console.print_error")
    @patch("mvmctl.cli.console._kill_console")
    def test_do_kill_handles_not_running(self, mock_kill, mock_print):
        mock_kill.return_value = False

        with pytest.raises(typer.Exit) as exc_info:
            _do_kill("testvm")
        assert exc_info.value.exit_code == 1
        mock_print.assert_called_once_with("No console relay running for 'testvm'")

    @patch("mvmctl.cli.console.print_error")
    @patch("mvmctl.cli.console._kill_console")
    def test_do_kill_handles_vm_not_found(self, mock_kill, mock_print):
        mock_kill.side_effect = VMNotFoundError("VM 'nonexistent' not found")

        with pytest.raises(typer.Exit) as exc_info:
            _do_kill("nonexistent")
        assert exc_info.value.exit_code == 1
        mock_print.assert_called_once_with("VM 'nonexistent' not found")


class TestDoAttachFunction:
    @patch("mvmctl.cli.console.disconnect_from_relay")
    @patch("mvmctl.cli.console.connect_to_relay")
    @patch("mvmctl.cli.console._attach_console")
    def test_do_attach_connects_to_socket(self, mock_attach, mock_connect, mock_disconnect):
        mock_attach.return_value = ConsoleInfo(
            socket_path=Path("/tmp/test.sock"),
            vm_name="testvm",
        )
        mock_sock = MagicMock()
        mock_connect.return_value = mock_sock

        with patch("mvmctl.cli.console.termios.tcgetattr", side_effect=Exception("no tty")):
            try:
                _do_attach("testvm")
            except Exception:
                pass

        mock_attach.assert_called_once_with("testvm")
        mock_connect.assert_called_once_with(Path("/tmp/test.sock"))

    @patch("mvmctl.cli.console.print_error")
    @patch("mvmctl.cli.console._attach_console")
    def test_do_attach_handles_vm_not_found(self, mock_attach, mock_print):
        mock_attach.side_effect = VMNotFoundError("VM 'nonexistent' not found")

        with pytest.raises(typer.Exit) as exc_info:
            _do_attach("nonexistent")
        assert exc_info.value.exit_code == 1
        mock_print.assert_called_once_with("VM 'nonexistent' not found")
