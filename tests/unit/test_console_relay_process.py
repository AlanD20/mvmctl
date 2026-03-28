"""Tests for console relay process."""

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.services.console_relay import process as relay_process


class TestSignalHandler:
    def test_signal_handler_sets_shutdown_state(self):
        relay_process._shutdown_state["requested"] = False
        relay_process._signal_handler(signal.SIGTERM, None)
        assert relay_process._shutdown_state["requested"] is True


class TestSetupSignalHandlers:
    def test_setup_signal_handlers_registers_signals(self):
        with patch("signal.signal") as mock_signal:
            relay_process._setup_signal_handlers()
            assert mock_signal.call_count == 2
            mock_signal.assert_any_call(signal.SIGTERM, relay_process._signal_handler)
            mock_signal.assert_any_call(signal.SIGINT, relay_process._signal_handler)


class TestWritePidFile:
    def test_write_pid_file_creates_parent_dirs(self, tmp_path: Path):
        pid_file = tmp_path / "nested" / "dir" / "console.pid"
        with patch("os.getpid", return_value=12345):
            relay_process._write_pid_file(pid_file)
        assert pid_file.exists()
        assert pid_file.read_text() == "12345"

    def test_write_pid_file_overwrites_existing(self, tmp_path: Path):
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("old_pid")
        with patch("os.getpid", return_value=99999):
            relay_process._write_pid_file(pid_file)
        assert pid_file.read_text() == "99999"


class TestCleanupPidFile:
    def test_cleanup_pid_file_removes_file(self, tmp_path: Path):
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("12345")
        relay_process._cleanup_pid_file(pid_file)
        assert not pid_file.exists()

    def test_cleanup_pid_file_handles_missing_file(self, tmp_path: Path):
        pid_file = tmp_path / "nonexistent.pid"
        relay_process._cleanup_pid_file(pid_file)
        assert not pid_file.exists()


class TestCleanupSocket:
    def test_cleanup_socket_removes_socket(self, tmp_path: Path):
        sock_path = tmp_path / "console.sock"
        sock_path.write_text("")
        relay_process._cleanup_socket(sock_path)
        assert not sock_path.exists()

    def test_cleanup_socket_handles_missing_socket(self, tmp_path: Path):
        sock_path = tmp_path / "nonexistent.sock"
        relay_process._cleanup_socket(sock_path)
        assert not sock_path.exists()


class TestReadFromPty:
    def test_read_from_pty_returns_data(self):
        with patch("os.read", return_value=b"test data") as mock_read:
            result = relay_process._read_from_pty(10, 1024)
            assert result == b"test data"
            mock_read.assert_called_once_with(10, 1024)

    def test_read_from_pty_handles_error(self):
        with patch("os.read", side_effect=OSError("read error")):
            result = relay_process._read_from_pty(10, 1024)
            assert result == b""


class TestWriteToLog:
    def test_write_to_log_appends_data(self, tmp_path: Path):
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"test output")
        assert log_file.read_bytes() == b"test output"

    def test_write_to_log_handles_error(self, tmp_path: Path):
        log_file = tmp_path / "console.log"
        with patch("builtins.open", side_effect=OSError("write error")):
            relay_process._write_to_log(log_file, b"test")

    def test_write_to_log_appends_multiple_writes(self, tmp_path: Path):
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"first")
        relay_process._write_to_log(log_file, b"second")
        assert log_file.read_bytes() == b"firstsecond"


class TestAcceptClient:
    def test_accept_client_returns_socket(self):
        mock_server = MagicMock()
        mock_client = MagicMock()
        mock_server.accept.return_value = (mock_client, ("", 0))
        result = relay_process._accept_client(mock_server)
        assert result == mock_client
        mock_server.setblocking.assert_called_once_with(False)

    def test_accept_client_handles_blocking_error(self):
        mock_server = MagicMock()
        mock_server.accept.side_effect = BlockingIOError()
        result = relay_process._accept_client(mock_server)
        assert result is None

    def test_accept_client_handles_os_error(self):
        mock_server = MagicMock()
        mock_server.accept.side_effect = OSError("accept error")
        result = relay_process._accept_client(mock_server)
        assert result is None


class TestForwardToClient:
    def test_forward_to_client_sends_data(self):
        mock_sock = MagicMock()
        result = relay_process._forward_to_client(mock_sock, b"test data")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"test data")

    def test_forward_to_client_handles_none_socket(self):
        result = relay_process._forward_to_client(None, b"test data")
        assert result is True

    def test_forward_to_client_handles_broken_pipe(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_forward_to_client_handles_connection_reset(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_forward_to_client_handles_os_error(self):
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError("send error")
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False


class TestReadFromClient:
    def test_read_from_client_returns_data(self):
        mock_sock = MagicMock()
        mock_sock.recv.return_value = b"input data"
        result = relay_process._read_from_client(mock_sock)
        assert result == b"input data"
        mock_sock.setblocking.assert_called_once_with(False)

    def test_read_from_client_handles_none_socket(self):
        result = relay_process._read_from_client(None)
        assert result == b""

    def test_read_from_client_handles_blocking_error(self):
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = BlockingIOError()
        result = relay_process._read_from_client(mock_sock)
        assert result == b""

    def test_read_from_client_handles_os_error(self):
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = OSError("recv error")
        result = relay_process._read_from_client(mock_sock)
        assert result == b""


class TestForwardToPty:
    def test_forward_to_pty_writes_data(self):
        with patch("os.write") as mock_write:
            result = relay_process._forward_to_pty(10, b"test data")
            assert result is True
            mock_write.assert_called_once_with(10, b"test data")

    def test_forward_to_pty_handles_empty_data(self):
        with patch("os.write") as mock_write:
            result = relay_process._forward_to_pty(10, b"")
            assert result is True
            mock_write.assert_not_called()

    def test_forward_to_pty_handles_error(self):
        with patch("os.write", side_effect=OSError("write error")):
            result = relay_process._forward_to_pty(10, b"test")
            assert result is False


class TestMain:
    def test_main_exits_on_shutdown(self, tmp_path: Path):
        pid_file = tmp_path / "console.pid"
        sock_path = tmp_path / "console.sock"
        log_file = tmp_path / "console.log"

        relay_process._shutdown_state["requested"] = False

        def side_effect(*args, **kwargs):
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch(
                            "sys.argv",
                            [
                                "process.py",
                                "--vm-name",
                                "testvm",
                                "--pty-master-fd",
                                "10",
                                "--socket-path",
                                str(sock_path),
                                "--pid-file",
                                str(pid_file),
                                "--log-file",
                                str(log_file),
                            ],
                        ):
                            result = relay_process.main()
                            assert result == 0
