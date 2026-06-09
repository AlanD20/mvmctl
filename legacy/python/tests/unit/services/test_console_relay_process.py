"""Tests for console relay process module (process.py).

Tests all standalone functions:
- _signal_handler and _setup_signal_handlers
- _write_pid_file and _cleanup_pid_file
- _cleanup_socket
- _read_from_pty, _write_to_log
- _accept_client, _forward_to_client, _read_from_client, _forward_to_pty
- main() entry point with argument parsing
"""

from __future__ import annotations

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.services.console_relay import process as relay_process


class TestSignalHandler:
    """Tests for _signal_handler."""

    def setup_method(self) -> None:
        relay_process._shutdown_state["requested"] = False

    def test_sigterm_sets_shutdown_flag(self) -> None:
        relay_process._signal_handler(signal.SIGTERM, None)
        assert relay_process._shutdown_state["requested"] is True

    def test_sigint_sets_shutdown_flag(self) -> None:
        relay_process._signal_handler(signal.SIGINT, None)
        assert relay_process._shutdown_state["requested"] is True

    def test_ignores_frame_parameter(self) -> None:
        mock_frame = MagicMock()
        relay_process._signal_handler(signal.SIGTERM, mock_frame)
        assert relay_process._shutdown_state["requested"] is True


class TestSetupSignalHandlers:
    """Tests for _setup_signal_handlers."""

    def test_registers_both_signals(self) -> None:
        with patch("signal.signal") as mock_signal:
            relay_process._setup_signal_handlers()
            assert mock_signal.call_count == 2
            mock_signal.assert_any_call(
                signal.SIGTERM, relay_process._signal_handler
            )
            mock_signal.assert_any_call(
                signal.SIGINT, relay_process._signal_handler
            )


class TestWritePidFile:
    """Tests for _write_pid_file."""

    def test_creates_parent_directories(self, tmp_path: Path) -> None:
        pid_file = tmp_path / "nested" / "deep" / "console.pid"
        with patch("os.getpid", return_value=12345):
            relay_process._write_pid_file(pid_file)
        assert pid_file.exists()

    def test_writes_correct_pid(self, tmp_path: Path) -> None:
        pid_file = tmp_path / "console.pid"
        with patch("os.getpid", return_value=99999):
            relay_process._write_pid_file(pid_file)
        assert pid_file.read_text() == "99999"


class TestCleanupPidFile:
    """Tests for _cleanup_pid_file."""

    def test_removes_existing_file(self, tmp_path: Path) -> None:
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("12345")
        relay_process._cleanup_pid_file(pid_file)
        assert not pid_file.exists()

    def test_handles_missing_file(self, tmp_path: Path) -> None:
        pid_file = tmp_path / "nonexistent.pid"
        relay_process._cleanup_pid_file(pid_file)

    def test_handles_os_error(self, tmp_path: Path) -> None:
        pid_file = tmp_path / "console.pid"
        pid_file.write_text("12345")
        with patch(
            "pathlib.Path.unlink", side_effect=OSError("permission denied")
        ):
            relay_process._cleanup_pid_file(pid_file)


class TestCleanupSocket:
    """Tests for _cleanup_socket."""

    def test_removes_existing_socket(self, tmp_path: Path) -> None:
        sock_path = tmp_path / "console.sock"
        sock_path.write_text("")
        relay_process._cleanup_socket(sock_path)
        assert not sock_path.exists()

    def test_handles_missing_socket(self, tmp_path: Path) -> None:
        sock_path = tmp_path / "nonexistent.sock"
        relay_process._cleanup_socket(sock_path)

    def test_handles_os_error(self, tmp_path: Path) -> None:
        sock_path = tmp_path / "console.sock"
        sock_path.write_text("")
        with patch(
            "pathlib.Path.unlink", side_effect=OSError("permission denied")
        ):
            relay_process._cleanup_socket(sock_path)


class TestReadFromPty:
    """Tests for _read_from_pty."""

    def test_returns_data(self) -> None:
        with patch("os.read", return_value=b"test output") as mock_read:
            result = relay_process._read_from_pty(10, 1024)
            assert result == b"test output"
            mock_read.assert_called_once_with(10, 1024)

    def test_uses_correct_fd(self) -> None:
        with patch("os.read", return_value=b"data") as mock_read:
            relay_process._read_from_pty(42, 4096)
            mock_read.assert_called_once_with(42, 4096)

    def test_handles_os_error(self) -> None:
        with patch("os.read", side_effect=OSError("read error")):
            result = relay_process._read_from_pty(10, 1024)
            assert result == b""

    def test_handles_eof(self) -> None:
        with patch("os.read", return_value=b""):
            result = relay_process._read_from_pty(10, 1024)
            assert result == b""


class TestWriteToLog:
    """Tests for _write_to_log."""

    def test_appends_data(self, tmp_path: Path) -> None:
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"test output")
        assert log_file.read_bytes() == b"test output"

    def test_appends_multiple_times(self, tmp_path: Path) -> None:
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"first")
        relay_process._write_to_log(log_file, b"second")
        assert log_file.read_bytes() == b"firstsecond"

    def test_handles_empty_data(self, tmp_path: Path) -> None:
        log_file = tmp_path / "console.log"
        relay_process._write_to_log(log_file, b"")
        assert log_file.read_bytes() == b""

    def test_handles_os_error(self, tmp_path: Path) -> None:
        log_file = tmp_path / "console.log"
        with patch("builtins.open", side_effect=OSError("write error")):
            relay_process._write_to_log(log_file, b"test")


class TestAcceptClient:
    """Tests for _accept_client."""

    def test_returns_client_socket(self) -> None:
        mock_server = MagicMock()
        mock_client = MagicMock()
        mock_server.accept.return_value = (mock_client, ("127.0.0.1", 12345))
        result = relay_process._accept_client(mock_server)
        assert result == mock_client

    def test_sets_non_blocking(self) -> None:
        mock_server = MagicMock()
        mock_client = MagicMock()
        mock_server.accept.return_value = (mock_client, ("", 0))
        relay_process._accept_client(mock_server)
        mock_server.setblocking.assert_called_once_with(False)

    def test_handles_blocking_error(self) -> None:
        mock_server = MagicMock()
        mock_server.accept.side_effect = BlockingIOError()
        result = relay_process._accept_client(mock_server)
        assert result is None

    def test_handles_os_error(self) -> None:
        mock_server = MagicMock()
        mock_server.accept.side_effect = OSError("accept error")
        result = relay_process._accept_client(mock_server)
        assert result is None


class TestForwardToClient:
    """Tests for _forward_to_client."""

    def test_sends_data(self) -> None:
        mock_sock = MagicMock()
        result = relay_process._forward_to_client(mock_sock, b"test data")
        assert result is True
        mock_sock.sendall.assert_called_once_with(b"test data")

    def test_handles_none_socket(self) -> None:
        result = relay_process._forward_to_client(None, b"test data")
        assert result is True

    def test_handles_broken_pipe(self) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = BrokenPipeError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_handles_connection_reset(self) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = ConnectionResetError()
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False

    def test_handles_os_error(self) -> None:
        mock_sock = MagicMock()
        mock_sock.sendall.side_effect = OSError("send error")
        result = relay_process._forward_to_client(mock_sock, b"test")
        assert result is False


class TestReadFromClient:
    """Tests for _read_from_client."""

    def test_returns_data(self) -> None:
        mock_sock = MagicMock()
        mock_sock.recv.return_value = b"input data"
        result = relay_process._read_from_client(mock_sock)
        assert result == b"input data"

    def test_sets_non_blocking(self) -> None:
        mock_sock = MagicMock()
        mock_sock.recv.return_value = b"data"
        relay_process._read_from_client(mock_sock)
        mock_sock.setblocking.assert_called_once_with(False)

    def test_handles_none_socket(self) -> None:
        result = relay_process._read_from_client(None)
        assert result == b""

    def test_handles_blocking_error(self) -> None:
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = BlockingIOError()
        result = relay_process._read_from_client(mock_sock)
        assert result == b""

    def test_handles_os_error(self) -> None:
        mock_sock = MagicMock()
        mock_sock.recv.side_effect = OSError("recv error")
        result = relay_process._read_from_client(mock_sock)
        assert result == b""


class TestForwardToPty:
    """Tests for _forward_to_pty."""

    def test_writes_data(self) -> None:
        with patch("os.write", return_value=9) as mock_write:
            result = relay_process._forward_to_pty(10, b"test data")
            assert result is True
            mock_write.assert_called_once_with(10, b"test data")

    def test_skips_empty_data(self) -> None:
        with patch("os.write") as mock_write:
            result = relay_process._forward_to_pty(10, b"")
            assert result is True
            mock_write.assert_not_called()

    def test_handles_os_error(self) -> None:
        with patch("os.write", side_effect=OSError("write error")):
            result = relay_process._forward_to_pty(10, b"test")
            assert result is False

    def test_handles_broken_pipe(self) -> None:
        with patch("os.write", side_effect=BrokenPipeError()):
            result = relay_process._forward_to_pty(10, b"test")
            assert result is False


class TestMainEntryPoint:
    """Tests for main() entry point."""

    def _make_args(
        self, tmp_path: Path, extra: list[str] | None = None
    ) -> list[str]:
        """Build mock sys.argv for process main()."""
        return [
            "process.py",
            "--id",
            "testvm",
            "--name",
            "test-vm",
            "--pty-controller-fd",
            "10",
            "--socket-path",
            str(tmp_path / "console.sock"),
            "--pid-file",
            str(tmp_path / "console.pid"),
            "--log-file",
            str(tmp_path / "console.log"),
            *(extra or []),
        ]

    def test_exits_on_shutdown(self, tmp_path: Path) -> None:
        """Should exit immediately when shutdown is requested."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(
                            relay_process, "_setup_signal_handlers"
                        ):
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch(
                                    "sys.argv", self._make_args(tmp_path)
                                ):
                                    result = relay_process.main()
                                    assert result == 0

    def test_reads_from_pty(self, tmp_path: Path) -> None:
        """Should read from PTY when it's ready."""
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                return ([rlist[0]], [], [])  # PTY is readable
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch.object(
                relay_process, "_read_from_pty", return_value=b"test"
            ):
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(
                                relay_process, "_setup_signal_handlers"
                            ):
                                with patch.object(
                                    relay_process, "_write_pid_file"
                                ):
                                    with patch.object(
                                        relay_process,
                                        "_forward_to_client",
                                        return_value=True,
                                    ):
                                        with patch(
                                            "sys.argv",
                                            self._make_args(tmp_path),
                                        ):
                                            result = relay_process.main()
                                            assert result == 0

    def test_shuts_down_on_pty_eof(self, tmp_path: Path) -> None:
        """Should shutdown when PTY returns empty data (EOF)."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            return ([rlist[0]], [], [])  # PTY is readable

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch.object(
                relay_process, "_read_from_pty", return_value=b""
            ):
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(
                                relay_process, "_setup_signal_handlers"
                            ):
                                with patch.object(
                                    relay_process, "_write_pid_file"
                                ):
                                    with patch(
                                        "sys.argv", self._make_args(tmp_path)
                                    ):
                                        result = relay_process.main()
                                        assert result == 0
                                        assert (
                                            relay_process._shutdown_state[
                                                "requested"
                                            ]
                                            is True
                                        )

    def test_calls_setup_signal_handlers(self, tmp_path: Path) -> None:
        """Initialization should set up signal handlers."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(
                            relay_process, "_setup_signal_handlers"
                        ) as mock_setup:
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch(
                                    "sys.argv", self._make_args(tmp_path)
                                ):
                                    relay_process.main()
                                    mock_setup.assert_called_once()

    def test_calls_write_pid_file(self, tmp_path: Path) -> None:
        """Initialization should write the PID file."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        pid_file = tmp_path / "console.pid"

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(
                            relay_process, "_setup_signal_handlers"
                        ):
                            with patch.object(
                                relay_process, "_write_pid_file"
                            ) as mock_write:
                                with patch(
                                    "sys.argv", self._make_args(tmp_path)
                                ):
                                    relay_process.main()
                                    mock_write.assert_called_once_with(pid_file)

    def test_uses_default_buffer_size(self, tmp_path: Path) -> None:
        """Default buffer size should be 4096."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([rlist[0]], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch.object(
                relay_process, "_read_from_pty", return_value=b""
            ) as mock_read:
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(
                                relay_process, "_setup_signal_handlers"
                            ):
                                with patch.object(
                                    relay_process, "_write_pid_file"
                                ):
                                    with patch(
                                        "sys.argv", self._make_args(tmp_path)
                                    ):
                                        relay_process.main()
                                        mock_read.assert_called_with(10, 4096)

    def test_uses_custom_buffer_size(self, tmp_path: Path) -> None:
        """Custom buffer size from arguments should be used."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([rlist[0]], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch.object(
                relay_process, "_read_from_pty", return_value=b""
            ) as mock_read:
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(relay_process, "_cleanup_pid_file"):
                        with patch.object(relay_process, "_cleanup_socket"):
                            with patch.object(
                                relay_process, "_setup_signal_handlers"
                            ):
                                with patch.object(
                                    relay_process, "_write_pid_file"
                                ):
                                    with patch(
                                        "sys.argv",
                                        self._make_args(
                                            tmp_path, ["--buffer-size", "8192"]
                                        ),
                                    ):
                                        relay_process.main()
                                        mock_read.assert_called_with(10, 8192)
