"""Tests for core console client."""

import socket
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.console import (
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    get_console_state,
    read_console_output,
    send_console_input,
)


class TestConnectToRelay:
    @patch("socket.socket")
    def test_connect_to_relay_creates_unix_socket(self, mock_socket_class):
        mock_sock = MagicMock()
        mock_socket_class.return_value = mock_sock

        socket_path = Path("/tmp/test.sock")
        result = connect_to_relay(socket_path)

        assert result == mock_sock
        mock_socket_class.assert_called_once_with(socket.AF_UNIX, socket.SOCK_STREAM)
        mock_sock.settimeout.assert_called_once()
        mock_sock.connect.assert_called_once_with(str(socket_path))
        mock_sock.setblocking.assert_called_once_with(False)

    @patch("socket.socket")
    def test_connect_to_relay_raises_connection_refused(self, mock_socket_class):
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = ConnectionRefusedError()
        mock_socket_class.return_value = mock_sock

        with pytest.raises(ConnectionRefusedError):
            connect_to_relay(Path("/tmp/test.sock"))

    @patch("socket.socket")
    def test_connect_to_relay_raises_file_not_found(self, mock_socket_class):
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = FileNotFoundError()
        mock_socket_class.return_value = mock_sock

        with pytest.raises(FileNotFoundError):
            connect_to_relay(Path("/tmp/test.sock"))

    @patch("socket.socket")
    def test_connect_to_relay_raises_timeout(self, mock_socket_class):
        mock_sock = MagicMock()
        mock_sock.connect.side_effect = TimeoutError()
        mock_socket_class.return_value = mock_sock

        with pytest.raises(TimeoutError):
            connect_to_relay(Path("/tmp/test.sock"))


class TestDisconnectFromRelay:
    def test_disconnect_from_relay_closes_socket(self):
        mock_sock = MagicMock()
        disconnect_from_relay(mock_sock)
        mock_sock.close.assert_called_once()

    def test_disconnect_from_relay_handles_error(self):
        mock_sock = MagicMock()
        mock_sock.close.side_effect = OSError("close error")
        disconnect_from_relay(mock_sock)


class TestSendConsoleInput:
    def test_send_console_input_sends_data(self):
        mock_sock = MagicMock()
        result = send_console_input(mock_sock, b"test data")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"test data")

    def test_send_console_input_handles_empty_data(self):
        mock_sock = MagicMock()
        result = send_console_input(mock_sock, b"")
        assert result is True
        mock_sock.sendall.assert_not_called()

    def test_send_console_input_handles_broken_pipe(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        result = send_console_input(mock_sock, b"test")
        assert result is False

    def test_send_console_input_handles_connection_reset(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        result = send_console_input(mock_sock, b"test")
        assert result is False

    def test_send_console_input_handles_os_error(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError("send error")
        result = send_console_input(mock_sock, b"test")
        assert result is False


class TestReadConsoleOutput:
    @patch("select.select")
    def test_read_console_output_yields_data(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.return_value = b"output data"

        generator = read_console_output(mock_sock)
        result = next(generator)

        assert result == b"output data"

    @patch("select.select")
    def test_read_console_output_returns_on_empty_data(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.return_value = b""

        generator = read_console_output(mock_sock)
        with pytest.raises(StopIteration):
            next(generator)

    @patch("select.select")
    def test_read_console_output_handles_blocking_error(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.side_effect = [BlockingIOError(), b"data"]

        generator = read_console_output(mock_sock)
        result = next(generator)
        assert result == b"data"

    @patch("select.select")
    def test_read_console_output_handles_interrupted_error(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.side_effect = [InterruptedError(), b"data"]

        generator = read_console_output(mock_sock)
        result = next(generator)
        assert result == b"data"

    @patch("select.select")
    def test_read_console_output_handles_os_error(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.side_effect = OSError("recv error")

        generator = read_console_output(mock_sock)
        with pytest.raises(StopIteration):
            next(generator)

    @patch("select.select")
    def test_read_console_output_handles_connection_reset(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10
        mock_select.return_value = ([10], [], [])
        mock_sock.recv.side_effect = ConnectionResetError()

        generator = read_console_output(mock_sock)
        with pytest.raises(StopIteration):
            next(generator)

    @patch("select.select")
    def test_read_console_output_skips_when_not_ready(self, mock_select):
        mock_sock = MagicMock()
        mock_sock.fileno.return_value = 10

        select_calls = [([], [], []), ([10], [], [])]
        mock_select.side_effect = select_calls
        mock_sock.recv.return_value = b"data"

        generator = read_console_output(mock_sock)
        result = next(generator)
        assert result == b"data"


class TestCheckEscapeSequence:
    def test_check_escape_sequence_detects_ctrl_x_d(self):
        buffer = bytearray(b"\x18d")
        result = check_escape_sequence(buffer)
        assert result == (True, "detach")

    def test_check_escape_sequence_no_match(self):
        buffer = bytearray(b"ab")
        matched, action = check_escape_sequence(buffer)
        assert matched is False
        assert action == ""

    def test_check_escape_sequence_partial_ctrl_x(self):
        buffer = bytearray(b"\x18")
        matched, action = check_escape_sequence(buffer)
        assert matched is False
        assert action == ""

    def test_check_escape_sequence_with_content_before(self):
        buffer = bytearray(b"some text\x18d")
        result = check_escape_sequence(buffer)
        assert result == (False, "")


class TestGetConsoleState:
    @patch("mvmctl.core.console.ConsoleRelayManager")
    def test_get_console_state_returns_dict(self, mock_mgr_class):
        mock_mgr = MagicMock()
        mock_mgr_class.return_value = mock_mgr
        mock_mgr.is_relay_running.return_value = True
        mock_mgr.get_relay_pid.return_value = 12345
        mock_mgr.get_socket_path.return_value = Path("/tmp/test.sock")

        result = get_console_state("testvm")

        assert result["running"] is True
        assert result["pid"] == 12345
        assert result["socket_path"] == "/tmp/test.sock"

    @patch("mvmctl.core.console.ConsoleRelayManager")
    def test_get_console_state_when_not_running(self, mock_mgr_class):
        mock_mgr = MagicMock()
        mock_mgr_class.return_value = mock_mgr
        mock_mgr.is_relay_running.return_value = False
        mock_mgr.get_relay_pid.return_value = None
        mock_mgr.get_socket_path.return_value = Path("/tmp/test.sock")

        result = get_console_state("testvm")

        assert result["running"] is False
        assert result["pid"] is None
        assert result["socket_path"] == "/tmp/test.sock"

    @patch("mvmctl.core.console.ConsoleRelayManager")
    def test_get_console_state_with_none_socket(self, mock_mgr_class):
        mock_mgr = MagicMock()
        mock_mgr_class.return_value = mock_mgr
        mock_mgr.is_relay_running.return_value = False
        mock_mgr.get_relay_pid.return_value = None
        mock_mgr.get_socket_path.return_value = None

        result = get_console_state("testvm")

        assert result["socket_path"] is None
