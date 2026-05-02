"""Tests for ConsoleRelayManager.

Tests lifecycle management of console relay processes: init, start, stop,
signal handling, PID file management, socket cleanup, and orphan cleanup.
"""

from __future__ import annotations

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.services.console_relay.exceptions import (
    ConsoleRelayAlreadyRunningError,
    ConsoleRelayProcessError,
)
from mvmctl.services.console_relay.manager import ConsoleRelayManager

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def vm_dir(tmp_path: Path) -> Path:
    """Create a temporary VM directory."""
    d = tmp_path / "cache" / "vms" / "testvm"
    d.mkdir(parents=True, exist_ok=True)
    return d


@pytest.fixture
def manager(vm_dir: Path) -> ConsoleRelayManager:
    """Create a ConsoleRelayManager for a specific VM."""
    return ConsoleRelayManager(id="testvm", path=vm_dir, name="test-vm")


# ---------------------------------------------------------------------------
# Init
# ---------------------------------------------------------------------------


class TestInit:
    """Tests for ConsoleRelayManager.__init__()."""

    def test_init_sets_id_and_path(self, vm_dir: Path) -> None:
        mgr = ConsoleRelayManager(id="myvm", path=vm_dir)
        assert mgr.id == "myvm"
        assert mgr.name == "myvm"

    def test_init_with_custom_name(self, vm_dir: Path) -> None:
        mgr = ConsoleRelayManager(id="myvm", path=vm_dir, name="My VM")
        assert mgr.name == "My VM"

    def test_init_initializes_pid_to_none(self, vm_dir: Path) -> None:
        mgr = ConsoleRelayManager(id="myvm", path=vm_dir)
        assert mgr.pid is None

    def test_init_creates_paths(self, vm_dir: Path) -> None:
        mgr = ConsoleRelayManager(id="myvm", path=vm_dir)
        assert mgr.pid_path == vm_dir / "console.pid"
        assert mgr.socket_path == vm_dir / "console.sock"
        assert mgr.log_path == vm_dir / "firecracker.console.log"

    def test_init_lazy_lock(self, vm_dir: Path) -> None:
        mgr = ConsoleRelayManager(id="myvm", path=vm_dir)
        assert mgr._lock is None


# ---------------------------------------------------------------------------
# PID property
# ---------------------------------------------------------------------------


class TestPidProperty:
    """Tests for the pid property."""

    def test_returns_pid_from_memory(
        self, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        assert manager.pid == 12345

    def test_returns_pid_from_file(
        self, tmp_path: Path, manager: ConsoleRelayManager
    ) -> None:
        pid_file = manager._pid_path
        pid_file.parent.mkdir(parents=True, exist_ok=True)
        pid_file.write_text("54321")
        manager._pid = None
        assert manager.pid == 54321

    def test_returns_none_when_no_file(
        self, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = None
        assert manager.pid is None

    def test_returns_none_on_invalid_file(
        self, manager: ConsoleRelayManager
    ) -> None:
        pid_file = manager._pid_path
        pid_file.parent.mkdir(parents=True, exist_ok=True)
        pid_file.write_text("not-a-number")
        manager._pid = None
        assert manager.pid is None


# ---------------------------------------------------------------------------
# is_running
# ---------------------------------------------------------------------------


class TestIsRunning:
    """Tests for is_running()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_returns_true_when_process_alive(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.return_value = None
        assert manager.is_running() is True

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_returns_false_when_process_dead(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.side_effect = ProcessLookupError()
        assert manager.is_running() is False

    def test_returns_false_when_no_pid(
        self, manager: ConsoleRelayManager
    ) -> None:
        assert manager.is_running() is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_returns_false_when_pid_file_stale(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        """PID file exists but process is dead."""
        manager._pid_path.parent.mkdir(parents=True, exist_ok=True)
        manager._pid_path.write_text("99999")
        mock_kill.side_effect = ProcessLookupError()
        assert manager.is_running() is False


# ---------------------------------------------------------------------------
# start
# ---------------------------------------------------------------------------


class TestStart:
    """Tests for start()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.console_relay.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_spawns_process(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345)

        sock_path, pid = manager.start(pty_controller_fd=10)

        assert pid == 12345
        assert sock_path == manager.socket_path
        mock_popen.assert_called_once()

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.console_relay.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_passes_correct_args(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start(pty_controller_fd=10)

        call_args = mock_popen.call_args[0][0]
        assert "--id" in call_args
        assert "testvm" in call_args
        assert "--pty-controller-fd" in call_args
        assert "10" in call_args
        assert "--socket-path" in call_args
        assert "--pid-file" in call_args
        assert "--log-file" in call_args

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.console_relay.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_starts_new_session(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start(pty_controller_fd=10)

        call_kwargs = mock_popen.call_args[1]
        assert call_kwargs.get("start_new_session") is True

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.console_relay.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_passes_fds(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start(pty_controller_fd=10)

        call_kwargs = mock_popen.call_args[1]
        assert "pass_fds" in call_kwargs
        assert 10 in call_kwargs["pass_fds"]

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch(
        "mvmctl.services.console_relay.manager.sys.executable",
        "/usr/bin/python",
    )
    def test_start_raises_if_already_running(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.return_value = MagicMock(pid=12345)

        manager.start(pty_controller_fd=10)
        with pytest.raises(
            ConsoleRelayAlreadyRunningError, match="already running"
        ):
            manager.start(pty_controller_fd=10)

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_handles_spawn_error(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_popen.side_effect = OSError("spawn failed")

        with pytest.raises(ConsoleRelayProcessError, match="Failed to spawn"):
            manager.start(pty_controller_fd=10)


# ---------------------------------------------------------------------------
# stop
# ---------------------------------------------------------------------------


class TestStop:
    """Tests for stop()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_sends_sigterm(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        result = manager.stop()

        assert result is True
        mock_kill.assert_called_with(12345, signal.SIGTERM)

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_clears_pid(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        manager.stop()
        assert manager._pid is None
        assert manager.pid is None

    def test_stop_returns_false_when_not_running(
        self, manager: ConsoleRelayManager
    ) -> None:
        result = manager.stop()
        assert result is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_handles_process_already_dead(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        """ProcessLookupError from kill should be handled gracefully."""
        mock_kill.side_effect = ProcessLookupError()
        manager._pid = 12345
        result = manager.stop()
        assert result is True

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_handles_permission_error(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        mock_kill.side_effect = PermissionError()
        manager._pid = 12345
        result = manager.stop()
        assert result is True

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_cleans_up_pid_file(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid_path.parent.mkdir(parents=True, exist_ok=True)
        manager._pid_path.write_text("12345")
        mock_kill.side_effect = ProcessLookupError()
        manager._pid = 12345

        manager.stop()
        assert not manager._pid_path.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_cleans_up_socket(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid_path.parent.mkdir(parents=True, exist_ok=True)
        manager._pid_path.write_text("12345")
        manager._socket_path.touch()
        mock_kill.side_effect = ProcessLookupError()
        manager._pid = 12345

        manager.stop()
        assert not manager._socket_path.exists()


# ---------------------------------------------------------------------------
# terminate
# ---------------------------------------------------------------------------


class TestTerminate:
    """Tests for terminate()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_terminate_sends_sigterm(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.return_value = None
        manager.terminate()
        mock_kill.assert_any_call(12345, signal.SIGTERM)

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_terminate_sends_sigkill_if_no_response(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        # First SIGTERM succeeds, sig 0 (check) always returns alive
        mock_kill.side_effect = [None] * 50 + [ProcessLookupError()]
        manager.terminate()
        # Should have sent SIGKILL at least once
        mock_kill.assert_any_call(12345, signal.SIGKILL)

    def test_terminate_returns_false_when_not_running(
        self, manager: ConsoleRelayManager
    ) -> None:
        result = manager.terminate()
        assert result is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_terminate_clears_pid(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.return_value = None
        manager.terminate()
        assert manager._pid is None


# ---------------------------------------------------------------------------
# get_pid
# ---------------------------------------------------------------------------


class TestGetPid:
    """Tests for get_pid()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_pid_returns_pid_when_alive(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.return_value = None
        assert manager.get_pid() == 12345

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_pid_returns_none_when_dead(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid = 12345
        mock_kill.side_effect = ProcessLookupError()
        assert manager.get_pid() is None

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_pid_recovers_from_pid_file(
        self, mock_kill: MagicMock, manager: ConsoleRelayManager
    ) -> None:
        manager._pid_path.parent.mkdir(parents=True, exist_ok=True)
        manager._pid_path.write_text("54321")
        mock_kill.return_value = None

        pid = manager.get_pid()
        assert pid == 54321

    def test_get_pid_returns_none_when_no_pid(
        self, manager: ConsoleRelayManager
    ) -> None:
        assert manager.get_pid() is None


# ---------------------------------------------------------------------------
# cleanup_orphans
# ---------------------------------------------------------------------------


class TestCleanupOrphans:
    """Tests for cleanup_orphans()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.utils.common.CacheUtils.get_vms_dir")
    def test_removes_stale_pid_files(
        self, mock_get_vms_dir: MagicMock, mock_kill: MagicMock, tmp_path: Path
    ) -> None:
        """Stale PID files (process dead) should be removed."""
        vms_dir = tmp_path / "vms"
        vms_dir.mkdir(parents=True)
        vm_dir = vms_dir / "oldvm"
        vm_dir.mkdir()
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("99999")

        mock_get_vms_dir.return_value = vms_dir
        mock_kill.side_effect = ProcessLookupError()

        # Use path="." so self.pid_path is just "console.pid" (relative)
        # matching the entry / self.pid_path pattern in cleanup_orphans
        mgr = ConsoleRelayManager(
            id="test", path=Path("."), pid_filename="console.pid"
        )
        mgr.cleanup_orphans()

        assert not pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.utils.common.CacheUtils.get_vms_dir")
    def test_skips_running_processes(
        self, mock_get_vms_dir: MagicMock, mock_kill: MagicMock, tmp_path: Path
    ) -> None:
        """Running processes should be left untouched."""
        vms_dir = tmp_path / "vms"
        vms_dir.mkdir(parents=True)
        vm_dir = vms_dir / "runningvm"
        vm_dir.mkdir()
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")

        mock_get_vms_dir.return_value = vms_dir
        mock_kill.return_value = None  # Process is alive

        mgr = ConsoleRelayManager(
            id="test", path=Path("."), pid_filename="console.pid"
        )
        mgr.cleanup_orphans()

        assert pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.utils.common.CacheUtils.get_vms_dir")
    def test_handles_invalid_pid_file(
        self, mock_get_vms_dir: MagicMock, mock_kill: MagicMock, tmp_path: Path
    ) -> None:
        """Invalid PID content should be cleaned up."""
        vms_dir = tmp_path / "vms"
        vms_dir.mkdir(parents=True)
        vm_dir = vms_dir / "badvm"
        vm_dir.mkdir()
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("not-a-number")

        mock_get_vms_dir.return_value = vms_dir
        mock_kill.side_effect = ProcessLookupError()

        mgr = ConsoleRelayManager(
            id="test", path=Path("."), pid_filename="console.pid"
        )
        mgr.cleanup_orphans()

        assert not pid_file.exists()

    @patch("mvmctl.utils.common.CacheUtils.get_vms_dir")
    def test_handles_missing_vms_dir(
        self, mock_get_vms_dir: MagicMock, tmp_path: Path
    ) -> None:
        """Missing vms directory should not raise."""
        mock_get_vms_dir.return_value = tmp_path / "nonexistent"

        mgr = ConsoleRelayManager(
            id="test", path=Path("."), pid_filename="console.pid"
        )
        mgr.cleanup_orphans()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.utils.common.CacheUtils.get_vms_dir")
    def test_cleans_up_socket_along_with_pid(
        self, mock_get_vms_dir: MagicMock, mock_kill: MagicMock, tmp_path: Path
    ) -> None:
        """Socket file should also be cleaned up when removing stale PID."""
        vms_dir = tmp_path / "vms"
        vms_dir.mkdir(parents=True)
        vm_dir = vms_dir / "deadvm"
        vm_dir.mkdir()
        (vm_dir / "console.pid").write_text("99999")
        socket_file = vm_dir / "console.sock"
        socket_file.touch()

        mock_get_vms_dir.return_value = vms_dir
        mock_kill.side_effect = ProcessLookupError()

        mgr = ConsoleRelayManager(
            id="test",
            path=Path("."),
            pid_filename="console.pid",
            socket_filename="console.sock",
        )
        mgr.cleanup_orphans()

        assert not socket_file.exists()
