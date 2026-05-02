"""Tests for CLI console commands."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app

runner = CliRunner()


class TestConsoleState:
    """Tests for 'console --state' command."""

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_state_running(self, mock_console_op):
        mock_console_op.get_state.return_value = {
            "running": True,
            "pid": 12345,
            "socket_path": "/tmp/test.sock",
        }
        result = runner.invoke(app, ["console", "--name", "testvm", "--state"])
        assert result.exit_code == 0
        assert "running" in result.output
        assert "12345" in result.output

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_state_stopped(self, mock_console_op):
        mock_console_op.get_state.return_value = {
            "running": False,
            "pid": None,
            "socket_path": None,
        }
        result = runner.invoke(app, ["console", "--name", "testvm", "--state"])
        assert result.exit_code == 0
        assert "stopped" in result.output


class TestConsoleKill:
    """Tests for 'console --kill' command."""

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_success(self, mock_console_op):
        mock_console_op.kill.return_value = True
        result = runner.invoke(app, ["console", "--name", "testvm", "--kill"])
        assert result.exit_code == 0
        assert "stopped" in result.output.lower()

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_not_running(self, mock_console_op):
        mock_console_op.kill.return_value = False
        result = runner.invoke(app, ["console", "--name", "testvm", "--kill"])
        assert result.exit_code == 1
        assert "No console relay" in result.output

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_not_found(self, mock_console_op):
        mock_console_op.kill.side_effect = MVMError("VM not found")
        result = runner.invoke(
            app, ["console", "--name", "nonexistent", "--kill"]
        )
        assert result.exit_code == 1


class TestConsoleAttach:
    """Tests for 'console' (attach) command."""

    @patch("mvmctl.cli.console.tty.setraw")
    @patch("mvmctl.cli.console.termios.tcgetattr")
    @patch("mvmctl.cli.console.termios.tcsetattr")
    @patch("mvmctl.cli.console._interact")
    @patch("mvmctl.cli.console._connect_socket")
    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_no_terminal(
        self, mock_console_op, mock_connect, mock_interact,
        mock_tcsetattr, mock_tcgetattr, mock_setraw
    ):
        mock_console_op.attach.return_value = MagicMock(
            socket_path="/tmp/test.sock",
            vm_name="testvm",
        )
        mock_connect.return_value = MagicMock()
        mock_tcgetattr.return_value = MagicMock()
        # Mock sys.stdin.fileno() for tty.setraw
        with patch("sys.stdin.fileno", return_value=0):
            from mvmctl.cli.console import _attach_to_console
            _attach_to_console("testvm")
        mock_connect.assert_called_once()

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_not_found(self, mock_console_op):
        mock_console_op.attach.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["console", "--name", "nonexistent"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_no_identifier(self, mock_console_op):
        """No identifier provided should show help (no_args_is_help=True)."""
        result = runner.invoke(app, ["console"])
        assert result.exit_code == 0
        assert "VM console access" in result.output

    def test_console_help(self):
        result = runner.invoke(app, ["console", "--help"])
        assert result.exit_code == 0
        assert "console" in result.output.lower()


class TestConsoleHelp:
    """Tests for console command group help."""

    def test_console_help(self):
        result = runner.invoke(app, ["console", "--help"])
        assert result.exit_code == 0
        assert "console" in result.output.lower()
