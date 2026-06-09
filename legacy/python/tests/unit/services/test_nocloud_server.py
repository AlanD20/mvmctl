"""Tests for NoCloud-net server — manager and process.

Tests:
- NoCloudNetServerManager: init, start, stop, terminate, is_running, port allocation
- process.py: _signal_handler, _CloudInitRequestHandler, main() argument parsing and server loop
"""

from __future__ import annotations

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.services.nocloud_server.exceptions import (
    NoCloudServerAlreadyRunningError,
    NoCloudServerError,
)
from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager
from mvmctl.services.nocloud_server.process import (
    _CloudInitRequestHandler,
    _signal_handler,
)

# ==============================================================================
# NoCloudNetServerManager tests
# ==============================================================================


@pytest.fixture
def vm_path(tmp_path: Path) -> Path:
    """Create a temporary VM directory."""
    d = tmp_path / "vms" / "test-vm"
    d.mkdir(parents=True, exist_ok=True)
    return d


@pytest.fixture
def manager(vm_path: Path) -> NoCloudNetServerManager:
    """Create a NoCloudNetServerManager with a specific port."""
    return NoCloudNetServerManager(
        id="test-vm",
        path=vm_path,
        ipv4_gateway="127.0.0.1",
        port=0,
        name="test-vm",
    )


class TestInit:
    """Tests for NoCloudNetServerManager.__init__()."""

    def test_init_stores_properties(self, vm_path: Path) -> None:
        mgr = NoCloudNetServerManager(
            id="vm1", path=vm_path, ipv4_gateway="10.0.0.1", port=8080
        )
        assert mgr.id == "vm1"
        assert mgr.name == "vm1"
        assert mgr.port == 8080
        assert mgr.url is None
        assert mgr.pid is None

    def test_init_sets_paths(self, vm_path: Path) -> None:
        mgr = NoCloudNetServerManager(
            id="vm1", path=vm_path, ipv4_gateway="10.0.0.1", port=8080
        )
        assert mgr.pid_path == vm_path / "nocloud-server.pid"
        assert mgr.log_path == vm_path / "cloud-init.log"

    def test_init_raises_on_invalid_port_range(self, vm_path: Path) -> None:
        with pytest.raises(ValueError, match="Port range"):
            NoCloudNetServerManager(
                id="vm1",
                path=vm_path,
                ipv4_gateway="10.0.0.1",
                port=8080,
                port_range_start=9000,
                port_range_end=8000,
            )

    def test_lazy_lock(self, vm_path: Path) -> None:
        mgr = NoCloudNetServerManager(
            id="vm1", path=vm_path, ipv4_gateway="10.0.0.1", port=8080
        )
        assert mgr._lock is None


class TestStart:
    """Tests for start()."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_spawns_subprocess(
        self, mock_popen: MagicMock, manager: NoCloudNetServerManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)

        url, port, pid = manager.start()

        assert pid == 12345
        assert url is not None
        assert "127.0.0.1" in url
        mock_popen.assert_called_once()

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_passes_correct_args(
        self, mock_popen: MagicMock, manager: NoCloudNetServerManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)

        manager.start()

        call_args = mock_popen.call_args[0][0]
        assert "--cloud-init-dir" in call_args
        assert "--port" in call_args
        assert "--host" in call_args
        assert "127.0.0.1" in call_args
        assert "--pid-file" in call_args

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_raises_if_already_running(
        self, mock_popen: MagicMock, manager: NoCloudNetServerManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)

        manager.start()
        with pytest.raises(
            NoCloudServerAlreadyRunningError, match="already running"
        ):
            manager.start()

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    def test_start_handles_spawn_error(
        self, mock_popen: MagicMock, vm_path: Path
    ) -> None:
        # Use fixed port to exercise the pre-allocated port error path
        manager = NoCloudNetServerManager(
            id="test-vm",
            path=vm_path,
            ipv4_gateway="127.0.0.1",
            port=8080,
            name="test-vm",
        )
        mock_popen.side_effect = OSError("spawn failed")

        with pytest.raises(NoCloudServerError, match="Failed to spawn"):
            manager.start()

    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("socket.socket")
    def test_start_auto_allocates_port(
        self,
        mock_socket_class: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        mock_socket = MagicMock()
        mock_socket_class.return_value.__enter__.return_value = mock_socket

        manager.start()

        # Port should be allocated in the valid range
        port = manager.port
        assert port is not None
        assert 8000 <= port <= 9000

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch("socket.socket")
    def test_start_raises_when_no_port_available(
        self,
        mock_sock_class: MagicMock,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        # Mock all port bind attempts to fail
        mock_sock = MagicMock()
        mock_sock.__enter__.return_value = mock_sock
        mock_sock.bind.side_effect = OSError("Address in use")
        mock_sock_class.return_value = mock_sock

        with pytest.raises(NoCloudServerError, match="No available port"):
            manager.start()


class TestStop:
    """Tests for stop()."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_stop_sends_sigterm(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        manager.start()

        with patch(
            "mvmctl.services.nocloud_server.manager.os.kill"
        ) as mock_kill:
            result = manager.stop()
            assert result is True
            mock_kill.assert_called_once_with(12345, signal.SIGTERM)

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_stop_clears_pid(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        manager.start()

        with patch("mvmctl.services.nocloud_server.manager.os.kill"):
            manager.stop()
            assert manager.pid is None

    def test_stop_returns_false_when_not_running(
        self, manager: NoCloudNetServerManager
    ) -> None:
        result = manager.stop()
        assert result is False

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_stop_is_idempotent(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        manager.start()

        with patch("mvmctl.services.nocloud_server.manager.os.kill"):
            manager.stop()
            manager.stop()  # Second call should not raise

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_stop_cleans_up_pid_file(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        pid_file = manager._pid_path
        pid_file.parent.mkdir(parents=True, exist_ok=True)

        manager.start()

        with patch("mvmctl.services.nocloud_server.manager.os.kill"):
            manager.stop()
            assert not pid_file.exists()


class TestTerminate:
    """Tests for terminate()."""

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_terminate_sends_sigterm(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        manager.start()

        with patch(
            "mvmctl.services.nocloud_server.manager.os.kill"
        ) as mock_kill:
            result = manager.terminate()
            assert result is True
            mock_kill.assert_called_once_with(12345, signal.SIGTERM)

    def test_terminate_returns_false_when_not_running(
        self, manager: NoCloudNetServerManager
    ) -> None:
        result = manager.terminate()
        assert result is False


class TestIsRunning:
    """Tests for is_running()."""

    def test_returns_false_when_not_started(
        self, manager: NoCloudNetServerManager
    ) -> None:
        assert manager.is_running() is False

    @patch("mvmctl.services.nocloud_server.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.nocloud_server.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_returns_true_when_running(
        self,
        mock_popen: MagicMock,
        manager: NoCloudNetServerManager,
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345, poll=lambda: None)
        manager.start()

        with patch(
            "mvmctl.services.nocloud_server.manager.os.kill", return_value=True
        ):
            assert manager.is_running() is True


# ==============================================================================
# Nocloud process tests
# ==============================================================================


class TestProcessSignalHandler:
    """Tests for _signal_handler in process.py."""

    def test_sets_shutdown_flag(self) -> None:
        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False
        _signal_handler(signal.SIGTERM, None)
        assert process_module._shutdown_requested is True

    def test_handles_sigint(self) -> None:
        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False
        _signal_handler(signal.SIGINT, None)
        assert process_module._shutdown_requested is True


class TestCloudInitRequestHandler:
    """Tests for _CloudInitRequestHandler."""

    def test_log_message_is_noop(self) -> None:
        handler = _CloudInitRequestHandler.__new__(_CloudInitRequestHandler)
        # Should not raise
        handler.log_message("GET /test %s", "200")

    def test_end_headers_adds_cache_control(self) -> None:
        handler = _CloudInitRequestHandler.__new__(_CloudInitRequestHandler)
        handler.send_header = MagicMock()  # type: ignore[method-assign]
        with patch.object(
            _CloudInitRequestHandler.__bases__[0], "end_headers", MagicMock()
        ):
            handler.end_headers()
            handler.send_header.assert_any_call(
                "Cache-Control", "no-store, no-cache, must-revalidate"
            )
            handler.send_header.assert_any_call("Pragma", "no-cache")


class TestMainArgumentParsing:
    """Tests for main() argument parsing and validation."""

    def test_cloud_init_dir_not_exists(self, tmp_path: Path) -> None:
        nonexistent = tmp_path / "nonexistent"

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(nonexistent),
                "--port",
                "8080",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(tmp_path / "test.pid"),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            from mvmctl.services.nocloud_server.process import main

            with pytest.raises(SystemExit) as exc_info:
                main()
            assert exc_info.value.code == 1

    def test_cloud_init_dir_not_directory(self, tmp_path: Path) -> None:
        not_a_dir = tmp_path / "file.txt"
        not_a_dir.write_text("test")

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(not_a_dir),
                "--port",
                "8080",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(tmp_path / "test.pid"),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            from mvmctl.services.nocloud_server.process import main

            with pytest.raises(SystemExit) as exc_info:
                main()
            assert exc_info.value.code == 1

    def test_pid_file_write_error(self, tmp_path: Path) -> None:
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        pid_file = tmp_path / "readonly" / "test.pid"

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(cloud_init_dir),
                "--port",
                "8080",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(pid_file),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            with patch(
                "pathlib.Path.mkdir", side_effect=OSError("Permission denied")
            ):
                from mvmctl.services.nocloud_server.process import main

                with pytest.raises(SystemExit) as exc_info:
                    main()
                assert exc_info.value.code == 1

    def test_server_bind_error(self, tmp_path: Path) -> None:
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(cloud_init_dir),
                "--port",
                "8080",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(tmp_path / "test.pid"),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                side_effect=OSError("Address already in use"),
            ):
                from mvmctl.services.nocloud_server.process import main

                with pytest.raises(SystemExit) as exc_info:
                    main()
                assert exc_info.value.code == 1


class TestMainServerLoop:
    """Tests for main() server loop execution."""

    def test_server_loop_shuts_down_gracefully(self, tmp_path: Path) -> None:
        """Main should run server loop and shut down gracefully."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(cloud_init_dir),
                "--port",
                "0",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(tmp_path / "test.pid"),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            mock_server = MagicMock()
            state = {"calls": 0}

            def handle_request_side_effect() -> None:
                state["calls"] += 1
                if state["calls"] >= 1:
                    process_module._shutdown_requested = True

            mock_server.handle_request.side_effect = handle_request_side_effect
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                process_module.main()

            mock_server.shutdown.assert_called_once()

    def test_handles_keyboard_interrupt(self, tmp_path: Path) -> None:
        """KeyboardInterrupt from handle_request propagates (not caught by except Exception)."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False

        with patch(
            "mvmctl.services.nocloud_server.process.signal.signal",
        ):
            with patch(
                "sys.argv",
                [
                    "process.py",
                    "--cloud-init-dir",
                    str(cloud_init_dir),
                    "--port",
                    "0",
                    "--host",
                    "127.0.0.1",
                    "--pid-file",
                    str(tmp_path / "test.pid"),
                    "--log-file",
                    str(tmp_path / "test.log"),
                ],
            ):
                mock_server = MagicMock()
                mock_server.handle_request.side_effect = KeyboardInterrupt
                with patch(
                    "mvmctl.services.nocloud_server.process.HTTPServer",
                    return_value=mock_server,
                ):
                    with pytest.raises(KeyboardInterrupt):
                        process_module.main()

    def test_re_raises_unexpected_exception(self, tmp_path: Path) -> None:
        """Unexpected exceptions during the loop should propagate."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False

        with patch(
            "sys.argv",
            [
                "process.py",
                "--cloud-init-dir",
                str(cloud_init_dir),
                "--port",
                "0",
                "--host",
                "127.0.0.1",
                "--pid-file",
                str(tmp_path / "test.pid"),
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            mock_server = MagicMock()
            mock_server.handle_request.side_effect = RuntimeError("unexpected")
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                with pytest.raises(RuntimeError, match="unexpected"):
                    process_module.main()
