"""Tests for ConsoleController."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, PropertyMock, patch

import pytest

from mvmctl.core.console._controller import ConsoleController
from mvmctl.exceptions import ConsoleError


@pytest.fixture
def vm_id() -> str:
    """Return a VM ID."""
    return "abc123def456"


@pytest.fixture
def vm_dir(tmp_path: Path) -> Path:
    """Return a VM directory."""
    d = tmp_path / "vms" / "testvm"
    d.mkdir(parents=True)
    return d


class TestConsoleController:
    """Tests for ConsoleController."""

    def test_init_creates_manager(self, vm_id: str, vm_dir: Path) -> None:
        """__init__ creates a ConsoleRelayManager."""
        controller = ConsoleController(vm_id, vm_dir)
        assert controller.manager is not None
        assert controller.socket_path is None
        assert controller.pid is None

    def test_create_pty_returns_fd(self, vm_id: str, vm_dir: Path) -> None:
        """create_pty returns a client file descriptor."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch(
            "mvmctl.core.console._controller.os.openpty", return_value=(3, 4)
        ):
            fd = controller.create_pty()
            assert fd == 4
            assert controller.controller_fd == 3
            assert controller.client_fd == 4

    def test_create_pty_called_twice_is_idempotent(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """create_pty returns same FD on second call."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch(
            "mvmctl.core.console._controller.os.openpty", return_value=(3, 4)
        ):
            fd1 = controller.create_pty()
            fd2 = controller.create_pty()
            assert fd1 == fd2

    def test_create_pty_raises_on_none_client_fd(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """create_pty raises ConsoleError when client_fd is None after creation."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch(
            "mvmctl.core.console._controller.os.openpty", return_value=(3, None)
        ):
            with pytest.raises(ConsoleError, match="PTY allocation failed"):
                controller.create_pty()

    def test_close_client_fd_closes(self, vm_id: str, vm_dir: Path) -> None:
        """close_client_fd closes the client FD."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch(
            "mvmctl.core.console._controller.os.openpty", return_value=(3, 4)
        ):
            controller.create_pty()
            with patch(
                "mvmctl.core.console._controller.os.close"
            ) as mock_close:
                controller.close_client_fd()
                mock_close.assert_called_once_with(4)
                assert controller.client_fd is None

    def test_close_client_fd_safe_multiple_calls(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """close_client_fd can be called multiple times safely."""
        controller = ConsoleController(vm_id, vm_dir)
        controller.close_client_fd()  # No-op when not created
        controller.close_client_fd()  # Should not raise

    def test_close_pty_closes_both(self, vm_id: str, vm_dir: Path) -> None:
        """close_pty closes both controller and client FDs."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch(
            "mvmctl.core.console._controller.os.openpty", return_value=(3, 4)
        ):
            controller.create_pty()
            with patch(
                "mvmctl.core.console._controller.os.close"
            ) as mock_close:
                controller.close_pty()
                assert mock_close.call_count >= 1

    def test_start_raises_without_create_pty(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """start raises RuntimeError if create_pty not called first."""
        controller = ConsoleController(vm_id, vm_dir)
        with pytest.raises(RuntimeError, match="create_pty"):
            controller.start()

    def test_start_success(self, vm_id: str, vm_dir: Path) -> None:
        """start returns socket_path and pid."""
        controller = ConsoleController(vm_id, vm_dir)

        with (
            patch(
                "mvmctl.core.console._controller.os.openpty",
                return_value=(3, 4),
            ),
            patch.object(
                type(controller.manager),
                "start",
                return_value=(Path("/tmp/test.sock"), 12345),
            ) as mock_start,
        ):
            controller.create_pty()
            socket_path, pid = controller.start()

            assert socket_path == Path("/tmp/test.sock")
            assert pid == 12345
            assert controller.socket_path == socket_path
            assert controller.pid == pid
            mock_start.assert_called_once_with(3)

    def test_stop_calls_manager_stop(self, vm_id: str, vm_dir: Path) -> None:
        """stop delegates to manager.stop()."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch.object(controller.manager, "stop") as mock_stop:
            controller.stop()
            mock_stop.assert_called_once()

    def test_cleanup_calls_stop_and_close(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """cleanup calls stop, close_pty, and close_client_fd."""
        controller = ConsoleController(vm_id, vm_dir)
        with (
            patch.object(controller.manager, "stop") as mock_stop,
            patch.object(controller, "close_pty"),
            patch.object(controller, "close_client_fd"),
        ):
            controller.cleanup()
            mock_stop.assert_called_once()

    def test_stop_force_delegates_to_manager_stop(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """stop(force=True) delegates to manager.stop(force=True)."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch.object(
            controller.manager, "stop", return_value=True
        ) as mock_stop:
            result = controller.stop(force=True)
            assert result is True
            mock_stop.assert_called_once_with(force=True)

    def test_is_running_delegates_to_manager(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """is_running delegates to manager.is_running()."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch.object(
            controller.manager, "is_running", return_value=True
        ) as mock_check:
            assert controller.is_running() is True
            mock_check.assert_called_once()

    def test_get_pid_delegates_to_manager(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """get_pid delegates to manager.get_pid()."""
        controller = ConsoleController(vm_id, vm_dir)
        with patch.object(
            controller.manager, "get_pid", return_value=12345
        ) as mock_get:
            assert controller.get_pid() == 12345
            mock_get.assert_called_once()

    def test_connect_creates_client(self, vm_id: str, vm_dir: Path) -> None:
        """connect creates a ConsoleRelayClient and connects."""
        controller = ConsoleController(vm_id, vm_dir)

        with (
            patch(
                "mvmctl.core.console._controller.ConsoleRelayClient"
            ) as mock_client_cls,
            patch.object(
                type(controller.manager),
                "socket_path",
                new_callable=PropertyMock,
                return_value=Path("/tmp/test.sock"),
            ),
        ):
            mock_client = MagicMock()
            mock_client_cls.return_value = mock_client

            client = controller.connect()
            assert client is mock_client
            mock_client.connect.assert_called_once()

    def test_disconnect_disconnects_client(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """disconnect disconnects the client."""
        controller = ConsoleController(vm_id, vm_dir)

        mock_client = MagicMock()
        controller._client = mock_client

        controller.disconnect()
        mock_client.disconnect.assert_called_once()
        assert controller._client is None

    def test_disconnect_safe_when_no_client(
        self, vm_id: str, vm_dir: Path
    ) -> None:
        """disconnect is safe when no client is connected."""
        controller = ConsoleController(vm_id, vm_dir)
        controller.disconnect()  # Should not raise
