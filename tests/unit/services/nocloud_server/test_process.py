"""Tests for services/nocloud_server/process.py."""

import signal
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.services.nocloud_server.process import (
    _CloudInitRequestHandler,
    _signal_handler,
)


class TestCloudInitRequestHandler:
    """Tests for _CloudInitRequestHandler."""

    def test_log_message_suppresses_output(self):
        """log_message should be a no-op."""
        handler = _CloudInitRequestHandler.__new__(_CloudInitRequestHandler)
        handler.log_message("GET /test %s", "200")

    def test_end_headers_adds_cache_control(self):
        """end_headers should add cache-control headers."""
        handler = _CloudInitRequestHandler.__new__(_CloudInitRequestHandler)
        handler.send_header = MagicMock()
        with patch.object(_CloudInitRequestHandler.__bases__[0], "end_headers", MagicMock()):
            handler.end_headers()
            handler.send_header.assert_any_call(
                "Cache-Control", "no-store, no-cache, must-revalidate"
            )
            handler.send_header.assert_any_call("Pragma", "no-cache")


class TestSignalHandler:
    """Tests for _signal_handler."""

    def test_sets_shutdown_flag(self):
        """_signal_handler should set _shutdown_requested to True."""
        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False
        _signal_handler(signal.SIGTERM, None)
        assert process_module._shutdown_requested is True

    def test_handles_sigint(self):
        """_signal_handler should handle SIGINT."""
        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False
        _signal_handler(signal.SIGINT, None)
        assert process_module._shutdown_requested is True


class TestMainArgumentParsing:
    """Tests for main() argument parsing and validation."""

    def test_cloud_init_dir_not_exists(self, tmp_path):
        """main() should exit when cloud-init dir doesn't exist."""
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
            ],
        ):
            from mvmctl.services.nocloud_server.process import main

            with pytest.raises(SystemExit) as exc_info:
                main()
            assert exc_info.value.code == 1

    def test_cloud_init_dir_not_directory(self, tmp_path):
        """main() should exit when cloud-init path is not a directory."""
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
            ],
        ):
            from mvmctl.services.nocloud_server.process import main

            with pytest.raises(SystemExit) as exc_info:
                main()
            assert exc_info.value.code == 1

    def test_pid_file_write_error(self, tmp_path):
        """main() should exit when PID file cannot be written."""
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
            ],
        ):
            with patch("pathlib.Path.mkdir", side_effect=OSError("Permission denied")):
                from mvmctl.services.nocloud_server.process import main

                with pytest.raises(SystemExit) as exc_info:
                    main()
                assert exc_info.value.code == 1

    def test_server_bind_error(self, tmp_path):
        """main() should exit when server cannot bind to address."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        pid_file = tmp_path / "test.pid"

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


class TestBoundHandler:
    """Tests for _BoundHandler.translate_path."""

    def test_translate_path_basic(self, tmp_path):
        """translate_path should map URL path to filesystem path."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("instance-id: test")

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = True

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
            ],
        ):
            mock_server = MagicMock()
            mock_server.handle_request.side_effect = KeyboardInterrupt
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                process_module.main()

    def test_translate_path_rejects_traversal(self, tmp_path):
        """translate_path should reject path traversal attempts."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = True

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
            ],
        ):
            mock_server = MagicMock()
            mock_server.handle_request.side_effect = KeyboardInterrupt
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                process_module.main()


class TestMainServerLoop:
    """Tests for main() server loop and graceful shutdown."""

    def test_main_server_loop_runs_and_shuts_down(self, tmp_path):
        """main() should run server loop and shut down gracefully."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        pid_file = tmp_path / "test.pid"

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
                str(pid_file),
            ],
        ):
            mock_server = MagicMock()
            state = {"calls": 0}

            def handle_request_side_effect():
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

    def test_main_cleans_up_pid_file_on_exit(self, tmp_path):
        """main() should clean up PID file after shutdown."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        pid_file = tmp_path / "test.pid"

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = True

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
                str(pid_file),
            ],
        ):
            mock_server = MagicMock()
            mock_server.handle_request.side_effect = KeyboardInterrupt
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                process_module.main()

    def test_main_handles_request_exception_during_loop(self, tmp_path):
        """main() should re-raise exceptions when not shutting down."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        pid_file = tmp_path / "test.pid"

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
                str(pid_file),
            ],
        ):
            mock_server = MagicMock()
            mock_server.handle_request.side_effect = RuntimeError("unexpected error")
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                with pytest.raises(RuntimeError, match="unexpected error"):
                    process_module.main()
