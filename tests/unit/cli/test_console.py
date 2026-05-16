"""Tests for CLI console commands."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models.result import OperationResult

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
        result = runner.invoke(app, ["console", "testvm", "--state"])
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
        result = runner.invoke(app, ["console", "testvm", "--state"])
        assert result.exit_code == 0
        assert "stopped" in result.output


class TestConsoleKill:
    """Tests for 'console --kill' command."""

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_success(self, mock_console_op):
        mock_console_op.kill.return_value = OperationResult(
            status="success", code="console.killed"
        )
        result = runner.invoke(app, ["console", "testvm", "--kill"])
        assert result.exit_code == 0
        assert "stopped" in result.output.lower()

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_not_running(self, mock_console_op):
        mock_console_op.kill.return_value = OperationResult(
            status="skipped", code="console.not_running"
        )
        result = runner.invoke(app, ["console", "testvm", "--kill"])
        assert result.exit_code == 1
        assert "Console relay not running" in result.output

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_not_found(self, mock_console_op):
        mock_console_op.kill.side_effect = MVMError("VM not found")
        result = runner.invoke(app, ["console", "nonexistent", "--kill"])
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
        self,
        mock_console_op,
        mock_connect,
        mock_interact,
        mock_tcsetattr,
        mock_tcgetattr,
        mock_setraw,
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
        mock_console_op.get_connection_info.side_effect = MVMError(
            "VM not found"
        )
        result = runner.invoke(app, ["console", "nonexistent"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    def test_console_no_identifier(self):
        """No identifier provided should show help (no_args_is_help=True)."""
        result = runner.invoke(app, ["console"])
        assert result.exit_code == 0

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


class TestConsoleCallbackEdgeCases:
    """Tests for callback edge cases."""

    def test_console_state_no_identifier(self):
        result = runner.invoke(app, ["console", "--state"])
        assert result.exit_code == 2

    def test_console_kill_no_identifier(self):
        result = runner.invoke(app, ["console", "--kill"])
        assert result.exit_code == 2

    @patch("mvmctl.cli.console._attach_to_console")
    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_by_ip(self, mock_console_op, mock_attach):
        result = runner.invoke(app, ["console", "10.0.0.5"])
        assert result.exit_code == 0
        mock_attach.assert_called_once_with("10.0.0.5")

    @patch("mvmctl.cli.console._attach_to_console")
    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_by_mac(self, mock_console_op, mock_attach):
        result = runner.invoke(app, ["console", "aa:bb:cc:dd:ee:ff"])
        assert result.exit_code == 0
        mock_attach.assert_called_once_with("aa:bb:cc:dd:ee:ff")

    @patch("mvmctl.cli.console._attach_to_console")
    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_positional(self, mock_console_op, mock_attach):
        result = runner.invoke(app, ["console", "testvm"])
        assert result.exit_code == 0
        mock_attach.assert_called_once_with("testvm")

    @patch("mvmctl.cli.console._attach_to_console")
    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_attach_via_name_cli(self, mock_console_op, mock_attach):
        runner.invoke(app, ["console", "testvm"])


class TestConsoleKillExtras:
    """Extended kill tests."""

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_console_kill_error_status(self, mock_console_op):
        mock_console_op.kill.return_value = OperationResult(
            status="error",
            code="console.kill_failed",
            message="Failed to stop console relay",
        )
        result = runner.invoke(app, ["console", "testvm", "--kill"])
        assert result.exit_code == 1
        assert "Failed" in result.output


class TestConsoleAttachErrorPaths:
    """Attach error paths."""

    @patch("mvmctl.cli.console.ConsoleOperation")
    def test_attach_socket_connect_fail(self, mock_console_op):
        mock_console_op.get_connection_info.return_value = MagicMock(
            socket_path="/tmp/test.sock",
            vm_name="testvm",
        )
        with patch("mvmctl.cli.console._connect_socket", return_value=None):
            result = runner.invoke(app, ["console", "testvm"])
        assert result.exit_code == 1
        assert "Console relay connection failed" in result.output


class TestConsoleSocketFunctions:
    """Tests for low-level socket functions."""

    def test_connect_socket_success(self):
        with patch("socket.socket") as mock_socket:
            mock_sock = MagicMock()
            mock_socket.return_value = mock_sock
            from mvmctl.cli.console import _connect_socket

            result = _connect_socket("/tmp/test.sock")
            assert result is mock_sock
            mock_sock.connect.assert_called_once_with("/tmp/test.sock")
            mock_sock.setblocking.assert_called_once_with(False)

    def test_connect_socket_oserror(self):
        with patch("socket.socket") as mock_socket:
            mock_socket.return_value.connect.side_effect = OSError("refused")
            from mvmctl.cli.console import _connect_socket

            result = _connect_socket("/tmp/test.sock")
            assert result is None

    def test_connect_socket_refused(self):
        with patch("socket.socket") as mock_socket:
            mock_socket.return_value.connect.side_effect = (
                ConnectionRefusedError()
            )
            from mvmctl.cli.console import _connect_socket

            result = _connect_socket("/tmp/test.sock")
            assert result is None

    def test_connect_socket_not_found(self):
        with patch("socket.socket") as mock_socket:
            mock_socket.return_value.connect.side_effect = FileNotFoundError()
            from mvmctl.cli.console import _connect_socket

            result = _connect_socket("/tmp/test.sock")
            assert result is None

    def test_try_send_success(self):
        mock_sock = MagicMock()
        from mvmctl.cli.console import _try_send

        _try_send(mock_sock, b"data")
        mock_sock.sendall.assert_called_once_with(b"data")

    def test_try_send_broken_pipe(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        from mvmctl.cli.console import _try_send

        _try_send(mock_sock, b"data")

    def test_try_send_oserror(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError()
        from mvmctl.cli.console import _try_send

        _try_send(mock_sock, b"data")

    def test_try_send_connection_reset(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        from mvmctl.cli.console import _try_send

        _try_send(mock_sock, b"data")
