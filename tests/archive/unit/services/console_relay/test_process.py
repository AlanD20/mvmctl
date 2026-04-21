"""Comprehensive tests for console relay process.

Tests all functions in services/console_relay/process.py:
- _signal_handler
- _setup_signal_handlers
- _write_pid_file
- _cleanup_pid_file
- _cleanup_socket
- _read_from_pty
- _write_to_log
- _accept_client
- _forward_to_client
- _read_from_client
- _forward_to_pty
- main
"""

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.services.console_relay import process as relay_process

# ============================================================================
# Test _signal_handler
# ============================================================================


class TestSignalHandler:
    """Tests for _signal_handler function."""

    def test_signal_handler_sets_shutdown_on_sigterm(self):
        """SIGTERM should set shutdown flag to True."""
        relay_process._shutdown_state["requested"] = False
        relay_process._signal_handler(signal.SIGTERM, None)
        assert relay_process._shutdown_state["requested"] is True

    def test_signal_handler_sets_shutdown_on_sigint(self):
        """SIGINT should set shutdown flag to True."""
        relay_process._shutdown_state["requested"] = False
        relay_process._signal_handler(signal.SIGINT, None)
        assert relay_process._shutdown_state["requested"] is True

    def test_signal_handler_ignores_frame(self):
        """Frame parameter should be ignored."""
        relay_process._shutdown_state["requested"] = False
        mock_frame = MagicMock()
        relay_process._signal_handler(signal.SIGTERM, mock_frame)
        assert relay_process._shutdown_state["requested"] is True


# ============================================================================
# Test _setup_signal_handlers
# ============================================================================


class TestSetupSignalHandlers:
    """Tests for _setup_signal_handlers function."""

    def test_setup_signal_handlers_registers_both_signals(self):
        """Should register both SIGTERM and SIGINT handlers."""
        with patch("signal.signal") as mock_signal:
            relay_process._setup_signal_handlers()
            assert mock_signal.call_count == 2
            mock_signal.assert_any_call(signal.SIGTERM, relay_process._signal_handler)
            mock_signal.assert_any_call(signal.SIGINT, relay_process._signal_handler)

    def test_setup_signal_handlers_uses_correct_handler(self):
        """Registered handlers should reference _signal_handler."""
        with patch("signal.signal") as mock_signal:
            relay_process._setup_signal_handlers()
            for call_args in mock_signal.call_args_list:
                assert call_args[0][1] == relay_process._signal_handler


# ============================================================================
# Test _write_pid_file
# ============================================================================


class TestWritePidFile:
    """Tests for _write_pid_file function."""

    def test_write_pid_file_creates_parent_directories(self, tmp_path: Path):
        """Should create nested parent directories."""
        pid_file = tmp_path / "nested" / "deep" / "path" / "console.pid"
        with patch("os.getpid", return_value=12345):
            relay_process._write_pid_file(pid_file)
        assert pid_file.exists()
        assert pid_file.parent.exists()

    def test_write_pid_file_writes_correct_pid(self, tmp_path: Path):
        """Should write the current process PID."""
        pid_file = tmp_path / "console.pid"
        with patch("os.getpid", return_value=99999):
            relay_process._write_pid_file(pid_file)
        assert pid_file.read_text() == "99999"

    def test_write_pid_file_overwrites_existing(self, tmp_path: Path):
        """Should overwrite existing pid file with new PID."""
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("old_pid")
        with patch("os.getpid", return_value=77777):
            relay_process._write_pid_file(pid_file)
        assert pid_file.read_text() == "77777"

    def test_write_pid_file_creates_in_existing_dir(self, tmp_path: Path):
        """Should create file in existing parent directory."""
        pid_file = tmp_path / "console.pid"
        relay_process._write_pid_file(pid_file)
        assert pid_file.exists()


# ============================================================================
# Test _cleanup_pid_file
# ============================================================================


class TestCleanupPidFile:
    """Tests for _cleanup_pid_file function."""

    def test_cleanup_pid_file_removes_existing_file(self, tmp_path: Path):
        """Should remove existing pid file."""
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("12345")
        relay_process._cleanup_pid_file(pid_file)
        assert not pid_file.exists()

    def test_cleanup_pid_file_handles_missing_file(self, tmp_path: Path):
        """Should not raise when file doesn't exist."""
        pid_file = tmp_path / "nonexistent.pid"
        # Should not raise
        relay_process._cleanup_pid_file(pid_file)
        assert not pid_file.exists()

    def test_cleanup_pid_file_handles_os_error(self, tmp_path: Path):
        """Should handle OSError gracefully."""
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("12345")
        with patch("pathlib.Path.unlink", side_effect=OSError("permission denied")):
            # Should not raise
            relay_process._cleanup_pid_file(pid_file)


# ============================================================================
# Test _cleanup_socket
# ============================================================================


class TestCleanupSocket:
    """Tests for _cleanup_socket function."""

    def test_cleanup_socket_removes_existing_socket(self, tmp_path: Path):
        """Should remove existing socket file."""
        sock_path = tmp_path / "console.sock"
        sock_path.write_text("")
        relay_process._cleanup_socket(sock_path)
        assert not sock_path.exists()

    def test_cleanup_socket_handles_missing_socket(self, tmp_path: Path):
        """Should not raise when socket doesn't exist."""
        sock_path = tmp_path / "nonexistent.sock"
        # Should not raise
        relay_process._cleanup_socket(sock_path)
        assert not sock_path.exists()

    def test_cleanup_socket_handles_os_error(self, tmp_path: Path):
        """Should handle OSError gracefully."""
        sock_path = tmp_path / "console.sock"
        sock_path.write_text("")
        with patch("pathlib.Path.unlink", side_effect=OSError("permission denied")):
            # Should not raise
            relay_process._cleanup_socket(sock_path)


# ============================================================================
# Test _read_from_pty
# ============================================================================


class TestReadFromPty:
    """Tests for _read_from_pty function."""

    def test_read_from_pty_returns_data(self):
        """Should return bytes read from PTY."""
        with patch("os.read", return_value=b"test output") as mock_read:
            result = relay_process._read_from_pty(10, 1024)
            assert result == b"test output"
            mock_read.assert_called_once_with(10, 1024)

    def test_read_from_pty_uses_correct_fd(self):
        """Should use provided file descriptor."""
        with patch("os.read", return_value=b"data") as mock_read:
            relay_process._read_from_pty(42, 4096)
            mock_read.assert_called_once_with(42, 4096)

    def test_read_from_pty_uses_correct_buffer_size(self):
        """Should use provided buffer size."""
        with patch("os.read", return_value=b"data") as mock_read:
            relay_process._read_from_pty(10, 8192)
            mock_read.assert_called_once_with(10, 8192)

    def test_read_from_pty_handles_os_error(self):
        """Should return empty bytes on OSError."""
        with patch("os.read", side_effect=OSError("read error")):
            result = relay_process._read_from_pty(10, 1024)
            assert result == b""

    def test_read_from_pty_handles_eof(self):
        """Should return empty bytes on EOF (empty read)."""
        with patch("os.read", return_value=b""):
            result = relay_process._read_from_pty(10, 1024)
            assert result == b""


# ============================================================================
# Test _write_to_log
# ============================================================================


class TestWriteToLog:
    """Tests for _write_to_log function."""

    def test_write_to_log_appends_data(self, tmp_path: Path):
        """Should append data to log file in binary mode."""
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"test output")
        assert log_file.read_bytes() == b"test output"

    def test_write_to_log_appends_multiple_times(self, tmp_path: Path):
        """Should append data without overwriting."""
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"first")
        relay_process._write_to_log(log_file, b"second")
        assert log_file.read_bytes() == b"firstsecond"

    def test_write_to_log_handles_empty_data(self, tmp_path: Path):
        """Should handle empty data gracefully."""
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"")
        assert log_file.read_bytes() == b""

    def test_write_to_log_handles_os_error(self, tmp_path: Path):
        """Should handle OSError gracefully."""
        log_file = tmp_path / "console.log"
        with patch("builtins.open", side_effect=OSError("write error")):
            # Should not raise
            relay_process._write_to_log(log_file, b"test")


# ============================================================================
# Test _accept_client
# ============================================================================


class TestAcceptClient:
    """Tests for _accept_client function."""

    def test_accept_client_returns_client_socket(self):
        """Should return client socket on successful accept."""
        mock_server = MagicMock()
        mock_client = MagicMock()
        mock_server.accept.return_value = (mock_client, ("127.0.0.1", 12345))
        result = relay_process._accept_client(mock_server)
        assert result == mock_client

    def test_accept_client_sets_non_blocking(self):
        """Should set socket to non-blocking mode."""
        mock_server = MagicMock()
        mock_client = MagicMock()
        mock_server.accept.return_value = (mock_client, ("", 0))
        relay_process._accept_client(mock_server)
        mock_server.setblocking.assert_called_once_with(False)

    def test_accept_client_handles_blocking_error(self):
        """Should return None on BlockingIOError."""
        mock_server = MagicMock()
        mock_server.accept.side_effect = BlockingIOError()
        result = relay_process._accept_client(mock_server)
        assert result is None

    def test_accept_client_handles_os_error(self):
        """Should return None on OSError."""
        mock_server = MagicMock()
        mock_server.accept.side_effect = OSError("accept error")
        result = relay_process._accept_client(mock_server)
        assert result is None

    def test_accept_client_handles_connection_refused(self):
        """Should handle connection refused errors."""
        mock_server = MagicMock()
        mock_server.accept.side_effect = ConnectionRefusedError()
        result = relay_process._accept_client(mock_server)
        assert result is None


# ============================================================================
# Test _forward_to_client
# ============================================================================


class TestForwardToClient:
    """Tests for _forward_to_client function."""

    def test_forward_to_client_sends_data(self):
        """Should send data to client socket."""
        mock_sock = MagicMock()
        result = relay_process._forward_to_client(mock_sock, b"test data")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"test data")

    def test_forward_to_client_handles_none_socket(self):
        """Should return True when socket is None."""
        result = relay_process._forward_to_client(None, b"test data")
        assert result is True

    def test_forward_to_client_handles_broken_pipe(self):
        """Should return False on BrokenPipeError."""
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_forward_to_client_handles_connection_reset(self):
        """Should return False on ConnectionResetError."""
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_forward_to_client_handles_os_error(self):
        """Should return False on OSError."""
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError("send error")
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_forward_to_client_sends_empty_data(self):
        """Should send data even when empty (not skipped)."""
        mock_sock = MagicMock()
        result = relay_process._forward_to_client(mock_sock, b"")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"")


# ============================================================================
# Test _read_from_client
# ============================================================================


class TestReadFromClient:
    """Tests for _read_from_client function."""

    def test_read_from_client_returns_data(self):
        """Should return data received from client."""
        mock_sock = MagicMock()
        mock_sock.recv.return_value = b"input data"
        result = relay_process._read_from_client(mock_sock)
        assert result == b"input data"

    def test_read_from_client_sets_non_blocking(self):
        """Should set socket to non-blocking mode."""
        mock_sock = MagicMock()
        mock_sock.recv.return_value = b"data"
        relay_process._read_from_client(mock_sock)
        mock_sock.setblocking.assert_called_once_with(False)

    def test_read_from_client_handles_none_socket(self):
        """Should return empty bytes when socket is None."""
        result = relay_process._read_from_client(None)
        assert result == b""

    def test_read_from_client_handles_blocking_error(self):
        """Should return empty bytes on BlockingIOError."""
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = BlockingIOError()
        result = relay_process._read_from_client(mock_sock)
        assert result == b""

    def test_read_from_client_handles_os_error(self):
        """Should return empty bytes on OSError."""
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = OSError("recv error")
        result = relay_process._read_from_client(mock_sock)
        assert result == b""

    def test_read_from_client_handles_connection_reset(self):
        """Should handle ConnectionResetError as empty read."""
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = ConnectionResetError()
        result = relay_process._read_from_client(mock_sock)
        assert result == b""


# ============================================================================
# Test _forward_to_pty
# ============================================================================


class TestForwardToPty:
    """Tests for _forward_to_pty function."""

    def test_forward_to_pty_writes_data(self):
        """Should write data to PTY file descriptor."""
        with patch("os.write", return_value=9) as mock_write:
            result = relay_process._forward_to_pty(10, b"test data")
            assert result is True
            mock_write.assert_called_once_with(10, b"test data")

    def test_forward_to_pty_handles_empty_data(self):
        """Should return True without writing for empty data."""
        with patch("os.write") as mock_write:
            result = relay_process._forward_to_pty(10, b"")
            assert result is True
            mock_write.assert_not_called()

    def test_forward_to_pty_handles_os_error(self):
        """Should return False on OSError."""
        with patch("os.write", side_effect=OSError("write error")):
            result = relay_process._forward_to_pty(10, b"test")
            assert result is False

    def test_forward_to_pty_handles_broken_pipe(self):
        """Should return False on BrokenPipeError."""
        with patch("os.write", side_effect=BrokenPipeError()):
            result = relay_process._forward_to_pty(10, b"test")
            assert result is False

    def test_forward_to_pty_uses_correct_fd(self):
        """Should use provided file descriptor."""
        with patch("os.write", return_value=5) as mock_write:
            relay_process._forward_to_pty(42, b"data")
            mock_write.assert_called_once_with(42, b"data")


# ============================================================================
# Test main - integration tests for the main entry point
# ============================================================================


class TestMain:
    """Tests for main() function."""

    def _make_mock_args(self, tmp_path: Path, extra_args: list[str] | None = None):
        """Helper to create mock sys.argv."""
        pid_file = tmp_path / "console.pid"
        sock_path = tmp_path / "console.sock"
        log_file = tmp_path / "console.log"

        args = [
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
        ]
        if extra_args:
            args.extend(extra_args)
        return args, pid_file, sock_path, log_file

    def test_main_exits_immediately_on_shutdown(self, tmp_path: Path):
        """Should exit immediately when shutdown is requested."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(*args, **kwargs):
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(relay_process, "_setup_signal_handlers"):
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch("sys.argv", argv_args):
                                    result = relay_process.main()
                                    assert result == 0

    def test_main_reads_from_pty_and_forwards(self, tmp_path: Path):
        """Should read from PTY and forward to client."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}

        def select_side_effect(rlist, wlist, xlist, timeout=None):
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                return ([rlist[0]], [], [])  # PTY is readable
            else:
                relay_process._shutdown_state["requested"] = True
                return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server

            with patch.object(relay_process, "_read_from_pty", return_value=b"test output"):
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(relay_process, "_setup_signal_handlers"):
                                with patch.object(relay_process, "_write_pid_file"):
                                    with patch.object(relay_process, "_write_to_log") as mock_log:
                                        with patch.object(
                                            relay_process,
                                            "_forward_to_client",
                                            return_value=True,
                                        ):
                                            with patch("sys.argv", argv_args):
                                                result = relay_process.main()
                                                assert result == 0
                                                mock_log.assert_called()

    def test_main_closes_pty_on_eof(self, tmp_path: Path):
        """Should shutdown when PTY returns empty data (EOF)."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(rlist, wlist, xlist, timeout=None):
            return ([rlist[0]], [], [])  # PTY is readable

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server

            with patch.object(relay_process, "_read_from_pty", return_value=b""):
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(relay_process, "_setup_signal_handlers"):
                                with patch.object(relay_process, "_write_pid_file"):
                                    with patch("sys.argv", argv_args):
                                        result = relay_process.main()
                                        assert result == 0
                                        assert relay_process._shutdown_state["requested"] is True

    def test_main_calls_setup_signal_handlers(self, tmp_path: Path):
        """Should call _setup_signal_handlers on startup."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(*args, **kwargs):
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(relay_process, "_setup_signal_handlers") as mock_setup:
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch("sys.argv", argv_args):
                                    relay_process.main()
                                    mock_setup.assert_called_once()

    def test_main_calls_write_pid_file(self, tmp_path: Path):
        """Should call _write_pid_file on startup."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(*args, **kwargs):
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(relay_process, "_setup_signal_handlers"):
                            with patch.object(relay_process, "_write_pid_file") as mock_write_pid:
                                with patch("sys.argv", argv_args):
                                    relay_process.main()
                                    mock_write_pid.assert_called_once_with(pid_file)

    def test_main_uses_default_buffer_size(self, tmp_path: Path):
        """Should use default buffer size of 4096."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}

        def select_side_effect(rlist, wlist, xlist, timeout=None):
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                # First call: PTY has data, then shutdown
                relay_process._shutdown_state["requested"] = True
                return ([rlist[0]], [], [])  # PTY is readable
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server

            with patch.object(relay_process, "_read_from_pty", return_value=b"") as mock_read:
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(relay_process, "_setup_signal_handlers"):
                                with patch.object(relay_process, "_write_pid_file"):
                                    with patch("sys.argv", argv_args):
                                        relay_process.main()
                                        mock_read.assert_called_with(10, 4096)

    def test_main_uses_custom_buffer_size(self, tmp_path: Path):
        """Should use custom buffer size from arguments."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(
            tmp_path, ["--buffer-size", "8192"]
        )
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}

        def select_side_effect(rlist, wlist, xlist, timeout=None):
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                # First call: PTY has data, then shutdown
                relay_process._shutdown_state["requested"] = True
                return ([rlist[0]], [], [])  # PTY is readable
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server

            with patch.object(relay_process, "_read_from_pty", return_value=b"") as mock_read:
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(relay_process, "_setup_signal_handlers"):
                                with patch.object(relay_process, "_write_pid_file"):
                                    with patch("sys.argv", argv_args):
                                        relay_process.main()
                                        mock_read.assert_called_with(10, 8192)

    def test_main_returns_zero_on_success(self, tmp_path: Path):
        """Should return 0 on successful completion."""
        argv_args, pid_file, sock_path, log_file = self._make_mock_args(tmp_path)
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(*args, **kwargs):
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(relay_process, "_setup_signal_handlers"):
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch("sys.argv", argv_args):
                                    result = relay_process.main()
                                    assert result == 0


# ============================================================================
# Summary
# ============================================================================

# Total test count:
# - TestSignalHandler: 3 tests
# - TestSetupSignalHandlers: 2 tests
# - TestWritePidFile: 4 tests
# - TestCleanupPidFile: 3 tests
# - TestCleanupSocket: 3 tests
# - TestReadFromPty: 5 tests
# - TestWriteToLog: 4 tests
# - TestAcceptClient: 5 tests
# - TestForwardToClient: 6 tests
# - TestReadFromClient: 6 tests
# - TestForwardToPty: 5 tests
# - TestMain: 8 tests
# Total: 54 tests
