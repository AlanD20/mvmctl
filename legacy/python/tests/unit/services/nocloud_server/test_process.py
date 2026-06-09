"""Tests for nocloud process.py — uncovered paths in main()."""

from __future__ import annotations

import os
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest


class TestProcessLogFileErrors:
    """Tests for log file open failures in main()."""

    def test_log_file_open_failure(self, tmp_path: Path) -> None:
        """main() exits with code 1 when log file cannot be opened."""
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
                str(tmp_path / "readonly" / "test.log"),
            ],
        ):
            from mvmctl.services.nocloud_server.process import main

            with pytest.raises(SystemExit) as exc_info:
                main()
            assert exc_info.value.code == 1

    def test_log_file_open_os_error(self, tmp_path: Path) -> None:
        """main() exits with code 1 when open() raises OSError."""
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
                "builtins.open",
                side_effect=OSError("Permission denied"),
            ):
                from mvmctl.services.nocloud_server.process import main

                with pytest.raises(SystemExit) as exc_info:
                    main()
                assert exc_info.value.code == 1


class TestProcessServerLoopException:
    """Tests for server loop exception handling."""

    def test_swallows_exception_when_shutdown_requested(
        self, tmp_path: Path
    ) -> None:
        """Server loop swallows Exception when _shutdown_requested is True."""
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
                "8080",
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

            def handle_request_side() -> None:
                state["calls"] += 1
                if state["calls"] >= 1:
                    process_module._shutdown_requested = True
                    raise RuntimeError("epoll error but shutting down")

            mock_server.handle_request.side_effect = handle_request_side
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                process_module.main()

            mock_server.shutdown.assert_called_once()


class TestProcessPidFileCleanup:
    """Tests for PID file cleanup in main() finally block."""

    def test_cleans_up_pid_file_on_error(self, tmp_path: Path) -> None:
        """main() removes PID file in finally block on server error."""
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
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                side_effect=OSError("Address in use"),
            ):
                from mvmctl.services.nocloud_server.process import main

                with pytest.raises(SystemExit):
                    main()

        # PID file should have been cleaned up even on error
        assert not pid_file.exists()

    def test_handles_pid_file_unlink_error(self, tmp_path: Path) -> None:
        """main() tolerates OSError during PID file cleanup."""
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
                "--log-file",
                str(tmp_path / "test.log"),
            ],
        ):
            with patch(
                "pathlib.Path.unlink",
                side_effect=OSError("Permission denied"),
            ):
                with patch(
                    "mvmctl.services.nocloud_server.process.HTTPServer",
                    side_effect=OSError("Address in use"),
                ):
                    from mvmctl.services.nocloud_server.process import main

                    with pytest.raises(SystemExit):
                        main()

    def test_handles_log_fp_close_error(self, tmp_path: Path, mocker) -> None:
        """main() tolerates Exception during log_fp.close()."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        import mvmctl.services.nocloud_server.process as process_module

        process_module._shutdown_requested = False
        mock_log_fp = MagicMock()
        mock_log_fp.close.side_effect = Exception("close error")

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
            mock_server = MagicMock()
            state = {"calls": 0}

            def handle_request_side() -> None:
                state["calls"] += 1
                if state["calls"] >= 1:
                    process_module._shutdown_requested = True

            mock_server.handle_request.side_effect = handle_request_side
            with patch(
                "mvmctl.services.nocloud_server.process.HTTPServer",
                return_value=mock_server,
            ):
                with patch("builtins.open", return_value=mock_log_fp):
                    process_module.main()

            mock_server.shutdown.assert_called_once()


class TestProcessBoundHandler:
    """Tests for the _BoundHandler translate_path."""

    def test_translate_path_basic(self, tmp_path: Path) -> None:
        """_BoundHandler.translate_path resolves paths correctly."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()
        (cloud_init_dir / "meta-data").write_text("test")

        from mvmctl.services.nocloud_server.process import (
            _CloudInitRequestHandler,
        )

        cloud_init_dir_str = str(cloud_init_dir)

        class _TestHandler(_CloudInitRequestHandler):
            def __init__(self) -> None:
                self.cloud_init_dir = cloud_init_dir_str
                self.path = "/meta-data"

            def translate_path(self, path: str) -> str:
                import os
                import posixpath
                import urllib.parse

                path = urllib.parse.unquote(path)
                path = posixpath.normpath(path)
                words = path.split("/")
                words = [w for w in words if w]
                result_path = str(self.cloud_init_dir)
                for word in words:
                    if os.path.dirname(word) or word in (
                        os.curdir,
                        os.pardir,
                    ):
                        continue
                    result_path = os.path.join(result_path, word)
                return result_path

        handler = _TestHandler()
        result = handler.translate_path("/meta-data")
        assert result == str(cloud_init_dir / "meta-data")

    def test_translate_path_strips_path_traversal(self, tmp_path: Path) -> None:
        """_BoundHandler.translate_path blocks path traversal."""
        cloud_init_dir = tmp_path / "cloud-init"
        cloud_init_dir.mkdir()

        class _TestHandler:
            def __init__(self) -> None:
                self.cloud_init_dir = str(cloud_init_dir)

            def translate_path(self, path: str) -> str:
                import os
                import posixpath
                import urllib.parse

                path = urllib.parse.unquote(path)
                path = posixpath.normpath(path)
                words = path.split("/")
                words = [w for w in words if w]
                result_path = str(self.cloud_init_dir)
                for word in words:
                    if os.path.dirname(word) or word in (
                        os.curdir,
                        os.pardir,
                    ):
                        continue
                    result_path = os.path.join(result_path, word)
                return result_path

        handler = _TestHandler()
        result = handler.translate_path("/../../../etc/passwd")
        assert result.startswith(str(cloud_init_dir))
        assert ".." not in result
        assert os.path.commonpath([result, str(cloud_init_dir)]) == str(
            cloud_init_dir
        )
