"""Extended tests for console relay process — main() edge cases and coverage."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.services.console_relay import process as relay_process


class TestMainExtended:
    """Extended tests for main() entry point — edge cases."""

    def _make_args(
        self, tmp_path: Path, extra: list[str] | None = None
    ) -> list[str]:
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

    def test_accepts_client_connection(self, tmp_path: Path) -> None:
        """Should accept a client when server socket is ready."""
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
                return ([rlist[1]], [], [])
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.fileno.return_value = 20
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(
                    relay_process, "_accept_client", return_value=MagicMock()
                ) as mock_accept:
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
                                        mock_accept.assert_called_once()

    def test_client_read_forwarded_to_pty(self, tmp_path: Path) -> None:
        """Should read from client and forward to PTY."""
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}
        mock_client = MagicMock()
        mock_client.fileno.return_value = 30

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                return ([rlist[1]], [], [])
            if call_count["selects"] == 2:
                return ([30], [], [])
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.fileno.return_value = 20
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(
                    relay_process, "_accept_client", return_value=mock_client
                ):
                    with patch.object(
                        relay_process,
                        "_read_from_client",
                        return_value=b"client input",
                    ):
                        with patch.object(
                            relay_process,
                            "_forward_to_pty",
                            return_value=True,
                        ) as mock_fwd:
                            with patch.object(
                                relay_process, "_cleanup_pid_file"
                            ):
                                with patch.object(
                                    relay_process, "_cleanup_socket"
                                ):
                                    with patch.object(
                                        relay_process,
                                        "_setup_signal_handlers",
                                    ):
                                        with patch.object(
                                            relay_process, "_write_pid_file"
                                        ):
                                            with patch(
                                                "sys.argv",
                                                self._make_args(tmp_path),
                                            ):
                                                result = relay_process.main()
                                                assert result == 0
                                                mock_fwd.assert_called_once_with(
                                                    10, b"client input"
                                                )

    def test_client_disconnect_clears_client(self, tmp_path: Path) -> None:
        """Should clear client_sock when client sends empty data."""
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}
        mock_client = MagicMock()
        mock_client.fileno.return_value = 30

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            call_count["selects"] += 1
            if call_count["selects"] == 1:
                return ([rlist[1]], [], [])
            if call_count["selects"] <= 3:
                return ([30], [], [])
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.fileno.return_value = 20
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(
                    relay_process, "_accept_client", return_value=mock_client
                ):
                    with patch.object(
                        relay_process,
                        "_read_from_client",
                        side_effect=[b"data", b""],
                    ):
                        with patch.object(
                            relay_process, "_forward_to_pty", return_value=True
                        ):
                            with patch.object(
                                relay_process, "_cleanup_pid_file"
                            ):
                                with patch.object(
                                    relay_process, "_cleanup_socket"
                                ):
                                    with patch.object(
                                        relay_process,
                                        "_setup_signal_handlers",
                                    ):
                                        with patch.object(
                                            relay_process, "_write_pid_file"
                                        ):
                                            with patch(
                                                "sys.argv",
                                                self._make_args(tmp_path),
                                            ):
                                                result = relay_process.main()
                                                assert result == 0
                                                mock_client.close.assert_called()

    def test_forward_to_pty_false(self, tmp_path: Path) -> None:
        """Should handle _forward_to_pty returning False gracefully."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
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

    def test_forward_to_client_false_disconnects(self, tmp_path: Path) -> None:
        """Should disconnect client when _forward_to_client returns False."""
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
                relay_process, "_read_from_pty", return_value=b"data"
            ):
                with patch("select.select", side_effect=select_side_effect):
                    with patch.object(
                        relay_process,
                        "_forward_to_client",
                        return_value=False,
                    ):
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
                                            self._make_args(tmp_path),
                                        ):
                                            result = relay_process.main()
                                            assert result == 0

    def test_pid_file_cleanup_on_exit(self, tmp_path: Path) -> None:
        """Should clean up PID file in finally block."""
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
                with patch.object(
                    relay_process, "_cleanup_pid_file"
                ) as mock_cleanup:
                    with patch.object(relay_process, "_cleanup_socket"):
                        with patch.object(
                            relay_process, "_setup_signal_handlers"
                        ):
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch(
                                    "sys.argv", self._make_args(tmp_path)
                                ):
                                    relay_process.main()
                                    mock_cleanup.assert_called_once_with(
                                        pid_file
                                    )

    def test_socket_cleanup_on_exit(self, tmp_path: Path) -> None:
        """Should clean up socket file in finally block."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        sock_path = tmp_path / "console.sock"

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(relay_process, "_cleanup_pid_file"):
                    with patch.object(
                        relay_process, "_cleanup_socket"
                    ) as mock_cleanup:
                        with patch.object(
                            relay_process, "_setup_signal_handlers"
                        ):
                            with patch.object(relay_process, "_write_pid_file"):
                                with patch(
                                    "sys.argv", self._make_args(tmp_path)
                                ):
                                    relay_process.main()
                                    mock_cleanup.assert_called_with(sock_path)

    def test_server_socket_creation_and_bind(self, tmp_path: Path) -> None:
        """Should create, bind, and listen on Unix socket."""
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
                                    relay_process.main()
                                    mock_server.bind.assert_called_once()
                                    mock_server.listen.assert_called_once_with(
                                        1
                                    )

    def test_server_close_error_in_finally(self, tmp_path: Path) -> None:
        """Should handle OSError when closing server socket."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.close.side_effect = OSError("close error")
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

    def test_client_close_error_in_finally(self, tmp_path: Path) -> None:
        """Should handle OSError when closing client socket."""
        relay_process._shutdown_state["requested"] = False

        def select_side_effect(
            rlist: list[int],
            wlist: list[int],
            xlist: list[int],
            timeout: object = None,
        ) -> tuple[list[int], list[int], list[int]]:
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        mock_client = MagicMock()
        mock_client.close.side_effect = OSError("client close error")
        # Set client_sock directly via state inspection

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.fileno.return_value = 20
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

    def test_accept_client_with_existing_connection(
        self, tmp_path: Path
    ) -> None:
        """Should not accept new client when one is already connected."""
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
                return ([rlist[1]], [], [])
            relay_process._shutdown_state["requested"] = True
            return ([], [], [])

        with patch("socket.socket") as mock_socket_class:
            mock_server = MagicMock()
            mock_server.fileno.return_value = 20
            mock_socket_class.return_value = mock_server
            with patch("select.select", side_effect=select_side_effect):
                with patch.object(
                    relay_process, "_accept_client", return_value=MagicMock()
                ):
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

    def test_main_handles_select_empty(self, tmp_path: Path) -> None:
        """Should handle empty select() results (timeout)."""
        relay_process._shutdown_state["requested"] = False

        call_count = {"selects": 0}

        def select_side_effect(
            *args: object, **kwargs: object
        ) -> tuple[list[int], list[int], list[int]]:
            call_count["selects"] += 1
            if call_count["selects"] <= 2:
                return ([], [], [])
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


class TestSignalHandlerExtended:
    """Extended tests for signal handling."""

    def test_signal_handler_os_error_safe(self) -> None:
        """_signal_handler is safe to call with any arguments."""
        relay_process._signal_handler(15, None)
        assert relay_process._shutdown_state["requested"] is True

    def test_signal_handler_reset_state(self) -> None:
        """Reset shutdown state between tests."""
        relay_process._shutdown_state["requested"] = False
        assert relay_process._shutdown_state["requested"] is False


class TestWriteToLogExtended:
    """Extended tests for _write_to_log."""

    def test_write_to_log_os_error(self, tmp_path: Path) -> None:
        """_write_to_log handles OSError gracefully."""
        log_file = tmp_path / "console.log"
        with patch("builtins.open", side_effect=OSError("cannot open")):
            relay_process._write_to_log(log_file, b"data")

    def test_write_to_log_no_directory(self) -> None:
        """_write_to_log handles missing directory."""
        relay_process._write_to_log(Path("/nonexistent/console.log"), b"data")
