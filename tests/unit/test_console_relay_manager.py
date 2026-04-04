"""Tests for console relay manager."""

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.services.console_relay import ConsoleRelayManager


class TestConsoleRelayManagerInit:
    def test_init_creates_empty_registry(self):
        mgr = ConsoleRelayManager()
        assert mgr._relays == {}
        assert mgr._lock is None

    def test_init_calls_cleanup_orphans(self):
        with patch.object(ConsoleRelayManager, "cleanup_orphans") as mock_cleanup:
            ConsoleRelayManager()
            mock_cleanup.assert_called_once()


class TestThreadLock:
    def test_thread_lock_lazy_initialization(self):
        mgr = ConsoleRelayManager()
        assert mgr._lock is None
        lock = mgr._thread_lock
        assert lock is not None
        assert mgr._lock is lock

    def test_thread_lock_returns_existing_lock(self):
        mgr = ConsoleRelayManager()
        lock1 = mgr._thread_lock
        lock2 = mgr._thread_lock
        assert lock1 is lock2


class TestGetPidFilePath:
    def test_get_pid_file_path(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        path = mgr._get_pid_file_path("testvm")
        assert path.name == "console.pid"
        assert "testvm" in str(path)


class TestGetSocketPath:
    def test_get_socket_path(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        path = mgr._get_socket_path("testvm")
        assert path.name == "console.sock"
        assert "testvm" in str(path)


class TestStartRelay:
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.sys.executable", "/usr/bin/python")
    def test_start_relay_spawns_process(self, mock_popen, tmp_path: Path):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        socket_path, pid = mgr.start_relay("testvm", 10, vm_dir)

        assert pid == 12345
        assert socket_path.name == "console.sock"
        mock_popen.assert_called_once()
        assert "testvm" in mgr._relays

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_raises_if_already_running(self, mock_popen, tmp_path: Path):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        mgr.start_relay("testvm", 10, vm_dir)

        with pytest.raises(MVMError, match="already running"):
            mgr.start_relay("testvm", 10, vm_dir)

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_handles_spawn_error(self, mock_popen, tmp_path: Path):
        mock_popen.side_effect = OSError("spawn failed")

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        with pytest.raises(MVMError, match="Failed to spawn"):
            mgr.start_relay("testvm", 10, vm_dir)


class TestStopRelay:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_stop_relay_sends_sigterm(self, mock_popen, mock_kill, tmp_path: Path):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        mgr.start_relay("testvm", 10, vm_dir)
        mgr.stop_relay("testvm")

        mock_kill.assert_called_once_with(12345, signal.SIGTERM)
        assert "testvm" not in mgr._relays

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_handles_process_lookup_error(self, mock_kill, tmp_path: Path):
        mock_kill.side_effect = ProcessLookupError()

        mgr = ConsoleRelayManager()
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr.stop_relay("testvm")

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_handles_permission_error(self, mock_kill, tmp_path: Path):
        mock_kill.side_effect = PermissionError()

        mgr = ConsoleRelayManager()
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr.stop_relay("testvm")

    def test_stop_relay_noop_if_not_running(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        mgr.stop_relay("nonexistent")


class TestStopByPidFile:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_by_pid_file_sends_sigterm(self, mock_kill, tmp_path: Path):
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")

        assert result is True
        mock_kill.assert_any_call(12345, 0)
        mock_kill.assert_any_call(12345, signal.SIGTERM)

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_by_pid_file_handles_process_lookup_error(self, mock_kill, tmp_path: Path):
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")
        mock_kill.side_effect = [None, ProcessLookupError()]

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")

        assert result is True

    def test_stop_by_pid_file_returns_false_if_no_pid_file(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")
        assert result is False

    def test_stop_by_pid_file_handles_invalid_pid(self, tmp_path: Path):
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("invalid")

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")
        assert result is False


class TestKillRelay:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_kill_relay_sends_sigterm_then_sigkill(self, mock_popen, mock_kill, tmp_path: Path):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        call_count = [0]

        def kill_side_effect(pid, sig):
            call_count[0] += 1
            if call_count[0] == 1 and sig == 0:
                return None
            elif sig == signal.SIGTERM:
                raise ProcessLookupError()
            elif sig == signal.SIGKILL:
                raise ProcessLookupError()

        mock_kill.side_effect = kill_side_effect

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        mgr.start_relay("testvm", 10, vm_dir)
        result = mgr.kill_relay("testvm")

        assert result is True

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_kill_relay_returns_false_if_not_running(self, mock_kill, tmp_path: Path):
        mock_kill.side_effect = ProcessLookupError()

        mgr = ConsoleRelayManager()
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        result = mgr.kill_relay("testvm")
        assert result is False


class TestGetRelayPid:
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_get_relay_pid_from_registry(self, mock_popen, tmp_path: Path):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        mgr.start_relay("testvm", 10, vm_dir)
        pid = mgr.get_relay_pid("testvm")

        assert pid == 12345

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_relay_pid_from_file(self, mock_kill, tmp_path: Path):
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        pid = mgr.get_relay_pid("testvm")

        assert pid == 12345

    def test_get_relay_pid_returns_none_if_not_found(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        pid = mgr.get_relay_pid("nonexistent")
        assert pid is None


class TestIsRelayRunning:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_is_relay_running_returns_true_when_running(
        self, mock_popen, mock_kill, tmp_path: Path
    ):
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        mgr = ConsoleRelayManager()
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)

        mgr.start_relay("testvm", 10, vm_dir)
        running = mgr.is_relay_running("testvm")

        assert running is True

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_is_relay_running_returns_false_when_not_running(self, mock_kill, tmp_path: Path):
        mock_kill.side_effect = ProcessLookupError()

        mgr = ConsoleRelayManager()
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        running = mgr.is_relay_running("testvm")
        assert running is False

    def test_is_relay_running_returns_false_if_not_in_registry(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        running = mgr.is_relay_running("nonexistent")
        assert running is False


class TestCleanupOrphans:
    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_removes_stale_pid_files(self, mock_kill, tmp_path: Path):
        mock_kill.side_effect = ProcessLookupError()

        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "oldvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("99999")

        ConsoleRelayManager()

        assert not pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_skips_running_processes(self, mock_kill, tmp_path: Path):
        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "runningvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")

        ConsoleRelayManager()

        assert pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_handles_invalid_pid(self, mock_kill, tmp_path: Path):
        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "badvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("invalid")

        ConsoleRelayManager()

        assert not pid_file.exists()

    def test_cleanup_orphans_no_vms_dir(self, tmp_path: Path):
        mgr = ConsoleRelayManager()
        mgr.cleanup_orphans()
