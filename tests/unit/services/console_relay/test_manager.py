"""Comprehensive tests for ConsoleRelayManager."""

import signal
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.services.console_relay.manager import ConsoleRelayManager

# =============================================================================
# Fixtures
# =============================================================================


@pytest.fixture
def vm_dir(tmp_path: Path) -> Path:
    """Create a temporary VM directory."""
    vm = tmp_path / "cache" / "vms" / "testvm"
    vm.mkdir(parents=True, exist_ok=True)
    return vm


@pytest.fixture
def manager(tmp_path: Path) -> ConsoleRelayManager:
    """Create a fresh manager instance with isolated cache."""
    return ConsoleRelayManager()


# =============================================================================
# TestInit
# =============================================================================


class TestInit:
    """Tests for ConsoleRelayManager.__init__()."""

    def test_init_creates_empty_registry(self, tmp_path: Path):
        """Manager initializes with empty _relays registry."""
        mgr = ConsoleRelayManager()
        assert mgr._relays == {}

    def test_init_lazy_lock(self, tmp_path: Path):
        """Manager initializes with None lock (lazy initialization)."""
        mgr = ConsoleRelayManager()
        assert mgr._lock is None

    def test_init_calls_cleanup_orphans(self, tmp_path: Path):
        """Manager calls cleanup_orphans on initialization."""
        with patch.object(ConsoleRelayManager, "cleanup_orphans") as mock_cleanup:
            ConsoleRelayManager()
            mock_cleanup.assert_called_once()


# =============================================================================
# TestSocketPath
# =============================================================================


class TestSocketPath:
    """Tests for get_socket_path()."""

    def test_get_socket_path_returns_correct_path(
        self, manager: ConsoleRelayManager, tmp_path: Path
    ):
        """get_socket_path returns correct socket file path."""
        path = manager.get_socket_path("testvm")
        assert path.name == "console.sock"
        assert "testvm" in str(path)

    def test_get_socket_path_uses_vm_dir(self, manager: ConsoleRelayManager, tmp_path: Path):
        """get_socket_path returns path under VM directory."""
        path = manager.get_socket_path("my-vm")
        assert "vms" in str(path)
        assert "my-vm" in str(path)


# =============================================================================
# TestGetRelayPid
# =============================================================================


class TestGetRelayPid:
    """Tests for get_relay_pid()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_get_relay_pid_returns_pid_from_registry(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """get_relay_pid returns PID when relay is in registry."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)
        pid = manager.get_relay_pid("testvm")

        assert pid == 12345

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_relay_pid_returns_pid_from_file(self, mock_kill: MagicMock, tmp_path: Path):
        """get_relay_pid returns PID from file when not in registry."""
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("54321")

        mgr = ConsoleRelayManager()
        pid = mgr.get_relay_pid("testvm")

        assert pid == 54321

    def test_get_relay_pid_returns_none_when_not_found(self, manager: ConsoleRelayManager):
        """get_relay_pid returns None when no relay exists."""
        pid = manager.get_relay_pid("nonexistent")
        assert pid is None

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_relay_pid_returns_none_when_file_invalid(
        self, mock_kill: MagicMock, tmp_path: Path
    ):
        """get_relay_pid returns None when PID file has invalid content."""
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("not-a-number")

        mgr = ConsoleRelayManager()
        pid = mgr.get_relay_pid("testvm")

        assert pid is None

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_get_relay_pid_returns_none_when_process_dead(
        self, mock_kill: MagicMock, tmp_path: Path
    ):
        """get_relay_pid returns None when process in PID file is dead."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("99999")

        mgr = ConsoleRelayManager()
        pid = mgr.get_relay_pid("testvm")

        assert pid is None


# =============================================================================
# TestIsRelayRunning
# =============================================================================


class TestIsRelayRunning:
    """Tests for is_relay_running()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_is_relay_running_returns_true_when_running(
        self,
        mock_kill: MagicMock,
        mock_popen: MagicMock,
        manager: ConsoleRelayManager,
        vm_dir: Path,
    ):
        """is_relay_running returns True when PID exists and process running."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)
        running = manager.is_relay_running("testvm")

        assert running is True

    def test_is_relay_running_returns_false_when_pid_file_missing(
        self, manager: ConsoleRelayManager
    ):
        """is_relay_running returns False when no PID file exists."""
        running = manager.is_relay_running("nonexistent")
        assert running is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_is_relay_running_returns_false_when_pid_invalid(
        self, mock_kill: MagicMock, tmp_path: Path
    ):
        """is_relay_running returns False when PID is invalid."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("99999")

        mgr = ConsoleRelayManager()
        running = mgr.is_relay_running("testvm")

        assert running is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_is_relay_running_returns_false_when_process_not_running(
        self, mock_kill: MagicMock, tmp_path: Path
    ):
        """is_relay_running returns False when process is stale (PID exists but process dead)."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("88888")

        mgr = ConsoleRelayManager()
        running = mgr.is_relay_running("testvm")

        assert running is False

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_is_relay_running_false_for_unknown_vm(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """is_relay_running returns False for unknown VM."""
        running = manager.is_relay_running("unknown-vm")
        assert running is False


# =============================================================================
# TestStartRelay
# =============================================================================


class TestStartRelay:
    """Tests for start_relay()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.sys.executable", "/usr/bin/python")
    def test_start_relay_spawns_process_with_correct_args(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay spawns process with correct command arguments."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        socket_path, pid = manager.start_relay("testvm", 10, vm_dir)

        assert pid == 12345
        mock_popen.assert_called_once()
        call_args = mock_popen.call_args[0][0]
        assert "--vm-name" in call_args
        assert "testvm" in call_args
        assert "--pty-master-fd" in call_args
        assert "10" in call_args
        assert "--socket-path" in call_args
        assert "--pid-file" in call_args
        assert "--log-file" in call_args

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_creates_relay_entry(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay creates relay entry in registry."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        assert "testvm" in manager._relays
        assert manager._relays["testvm"]["pid"] == 12345

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_returns_socket_path_and_pid(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay returns tuple of (socket_path, pid)."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        socket_path, pid = manager.start_relay("testvm", 10, vm_dir)

        assert socket_path.name == "console.sock"
        assert pid == 12345

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_raises_if_already_running(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay raises MVMError if relay already running."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        with pytest.raises(MVMError, match="already running"):
            manager.start_relay("testvm", 10, vm_dir)

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_start_relay_handles_spawn_error(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay raises MVMError if subprocess spawn fails."""
        mock_popen.side_effect = OSError("spawn failed")

        with pytest.raises(MVMError, match="Failed to spawn"):
            manager.start_relay("testvm", 10, vm_dir)

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.sys.executable", "/usr/bin/python")
    def test_start_relay_uses_pass_fds(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay passes file descriptor to subprocess."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        call_kwargs = mock_popen.call_args[1]
        assert "pass_fds" in call_kwargs
        assert 10 in call_kwargs["pass_fds"]

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.sys.executable", "/usr/bin/python")
    def test_start_relay_starts_new_session(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """start_relay starts subprocess in new session."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        call_kwargs = mock_popen.call_args[1]
        assert call_kwargs.get("start_new_session") is True


# =============================================================================
# TestStopRelay
# =============================================================================


class TestStopRelay:
    """Tests for stop_relay()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_terminates_process_gracefully(
        self,
        mock_kill: MagicMock,
        mock_popen: MagicMock,
        manager: ConsoleRelayManager,
        vm_dir: Path,
    ):
        """stop_relay sends SIGTERM to terminate process."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)
        manager.stop_relay("testvm")

        mock_kill.assert_called_with(12345, signal.SIGTERM)

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_removes_from_registry(
        self,
        mock_kill: MagicMock,
        mock_popen: MagicMock,
        manager: ConsoleRelayManager,
        vm_dir: Path,
    ):
        """stop_relay removes relay from registry after stopping."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)
        assert "testvm" in manager._relays

        manager.stop_relay("testvm")
        assert "testvm" not in manager._relays

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_handles_process_already_dead(self, mock_kill: MagicMock, tmp_path: Path):
        """stop_relay handles ProcessLookupError gracefully."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        # Should not raise
        mgr.stop_relay("testvm")

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_handles_permission_error(self, mock_kill: MagicMock, tmp_path: Path):
        """stop_relay handles PermissionError gracefully."""
        mock_kill.side_effect = PermissionError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        # Should not raise
        mgr.stop_relay("testvm")

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_cleans_up_pid_file(self, mock_kill: MagicMock, tmp_path: Path):
        """stop_relay cleans up PID file after stopping."""
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        def kill_side_effect(pid, sig):
            if sig == 0:
                return None  # Process check
            raise ProcessLookupError()  # SIGTERM

        mock_kill.side_effect = kill_side_effect

        mgr = ConsoleRelayManager()
        mgr.stop_relay("testvm")

        # PID file should be cleaned up
        assert not pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_relay_cleans_up_socket(self, mock_kill: MagicMock, tmp_path: Path):
        """stop_relay cleans up socket file after stopping."""
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)
        socket_path = vm_dir / "console.sock"
        socket_path.touch()

        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")

        def kill_side_effect(pid, sig):
            if sig == 0:
                return None
            raise ProcessLookupError()

        mock_kill.side_effect = kill_side_effect

        mgr = ConsoleRelayManager()
        mgr.stop_relay("testvm")

        # Socket file should be cleaned up
        assert not socket_path.exists()

    def test_stop_relay_handles_already_stopped(self, manager: ConsoleRelayManager):
        """stop_relay is safe to call when relay not running."""
        # Should not raise
        manager.stop_relay("nonexistent")


# =============================================================================
# TestKillRelay
# =============================================================================


class TestKillRelay:
    """Tests for kill_relay()."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_kill_relay_returns_true_when_process_dies_on_sigterm(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """kill_relay returns True when process dies after SIGTERM."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        # os.kill(pid, 0) - check if running -> None (running, first call)
        # os.kill(pid, SIGTERM) -> ProcessLookupError (process died, second call)
        with patch("mvmctl.services.console_relay.manager.os.kill") as mock_kill:
            mock_kill.side_effect = [None, ProcessLookupError()]
            result = manager.kill_relay("testvm")

        assert result is True

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_kill_relay_removes_from_registry(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """kill_relay removes relay from registry after killing."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)
        assert "testvm" in manager._relays

        with patch("mvmctl.services.console_relay.manager.os.kill") as mock_kill:
            mock_kill.side_effect = [None, ProcessLookupError()]
            result = manager.kill_relay("testvm")

        assert result is True
        assert "testvm" not in manager._relays

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_kill_relay_returns_false_when_not_running(self, mock_kill: MagicMock, tmp_path: Path):
        """kill_relay returns False when no relay is running."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("99999")

        mgr = ConsoleRelayManager()
        result = mgr.kill_relay("testvm")

        assert result is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_kill_relay_cleans_up_files(self, mock_kill: MagicMock, tmp_path: Path):
        """kill_relay cleans up PID and socket files."""
        vm_dir = tmp_path / "cache" / "vms" / "testvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")
        socket_path = vm_dir / "console.sock"
        socket_path.touch()

        def kill_side_effect(pid, sig):
            if sig == 0:
                return None
            raise ProcessLookupError()

        mock_kill.side_effect = kill_side_effect

        mgr = ConsoleRelayManager()
        mgr.kill_relay("testvm")

        assert not pid_file.exists()
        assert not socket_path.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_kill_relay_handles_already_stopped(self, mock_kill: MagicMock, tmp_path: Path):
        """kill_relay handles case where relay already stopped."""
        mock_kill.side_effect = ProcessLookupError()

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("99999")

        mgr = ConsoleRelayManager()
        # Should return False, not raise
        result = mgr.kill_relay("testvm")
        assert result is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_kill_relay_handles_permission_error(self, mock_kill: MagicMock, tmp_path: Path):
        """kill_relay handles PermissionError gracefully."""

        def kill_side_effect(pid, sig):
            if sig == 0:
                return None
            if sig == signal.SIGTERM:
                raise PermissionError()
            return None

        mock_kill.side_effect = kill_side_effect

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        # Should not raise
        mgr.kill_relay("testvm")

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_kill_relay_sends_sigterm(
        self, mock_popen: MagicMock, manager: ConsoleRelayManager, vm_dir: Path
    ):
        """kill_relay sends SIGTERM to the process."""
        mock_proc = MagicMock()
        mock_proc.pid = 12345
        mock_popen.return_value = mock_proc

        manager.start_relay("testvm", 10, vm_dir)

        with patch("mvmctl.services.console_relay.manager.os.kill") as mock_kill:
            mock_kill.side_effect = [None, ProcessLookupError()]
            manager.kill_relay("testvm")

            # Check that SIGTERM was sent
            mock_kill.assert_any_call(12345, signal.SIGTERM)


# =============================================================================
# TestCleanupOrphans
# =============================================================================


class TestCleanupOrphans:
    """Tests for cleanup_orphans()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_removes_stale_pid_files(self, mock_kill: MagicMock, tmp_path: Path):
        """cleanup_orphans removes PID files for dead processes."""
        mock_kill.side_effect = ProcessLookupError()

        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "oldvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("99999")

        ConsoleRelayManager()

        assert not pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_skips_running_processes(self, mock_kill: MagicMock, tmp_path: Path):
        """cleanup_orphans leaves PID files for running processes."""
        mock_kill.return_value = None  # Process is running

        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "runningvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")

        ConsoleRelayManager()

        assert pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_handles_invalid_pid(self, mock_kill: MagicMock, tmp_path: Path):
        """cleanup_orphans handles invalid PID file content."""
        mock_kill.side_effect = OSError("invalid")

        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "badvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("not-a-number")

        ConsoleRelayManager()

        assert not pid_file.exists()

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_cleanup_orphans_handles_permission_error(self, mock_kill: MagicMock, tmp_path: Path):
        """cleanup_orphans handles permission errors gracefully."""
        mock_kill.side_effect = PermissionError()

        vms_dir = tmp_path / "cache" / "vms"
        vm_dir = vms_dir / "permvm"
        vm_dir.mkdir(parents=True)
        pid_file = vm_dir / "console.pid"
        pid_file.write_text("12345")

        # Should not raise
        ConsoleRelayManager()

    def test_cleanup_orphans_handles_missing_vms_dir(self, tmp_path: Path):
        """cleanup_orphans handles missing vms directory."""
        # No vms directory created

        mgr = ConsoleRelayManager()
        # Should not raise
        mgr.cleanup_orphans()


# =============================================================================
# TestStopByPidFile
# =============================================================================


class TestStopByPidFile:
    """Tests for _stop_by_pid_file()."""

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_by_pid_file_sends_sigterm(self, mock_kill: MagicMock, tmp_path: Path):
        """_stop_by_pid_file sends SIGTERM to process."""
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")

        assert result is True
        mock_kill.assert_any_call(12345, 0)  # Check process
        mock_kill.assert_any_call(12345, signal.SIGTERM)

    def test_stop_by_pid_file_returns_false_if_no_pid_file(self, tmp_path: Path):
        """_stop_by_pid_file returns False when no PID file."""
        mgr = ConsoleRelayManager()

        result = mgr._stop_by_pid_file("nonexistent")
        assert result is False

    def test_stop_by_pid_file_handles_invalid_pid(self, tmp_path: Path):
        """_stop_by_pid_file handles invalid PID content."""
        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("invalid")

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")
        assert result is False

    @patch("mvmctl.services.console_relay.manager.os.kill")
    def test_stop_by_pid_file_handles_process_already_dead(
        self, mock_kill: MagicMock, tmp_path: Path
    ):
        """_stop_by_pid_file handles process already terminated."""
        mock_kill.side_effect = [None, ProcessLookupError()]  # Check succeeds, SIGTERM fails

        pid_file = tmp_path / "cache" / "vms" / "testvm" / "console.pid"
        pid_file.parent.mkdir(parents=True)
        pid_file.write_text("12345")

        mgr = ConsoleRelayManager()
        result = mgr._stop_by_pid_file("testvm")

        assert result is True
        # PID file should be cleaned up
        assert not pid_file.exists()


# =============================================================================
# TestThreadSafety
# =============================================================================


class TestThreadSafety:
    """Tests for thread safety of the manager."""

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_thread_lock_lazy_initialization(self, mock_popen: MagicMock, tmp_path: Path):
        """Thread lock is lazily initialized."""
        mgr = ConsoleRelayManager()

        assert mgr._lock is None
        lock = mgr._thread_lock
        assert lock is not None
        assert mgr._lock is lock

    @patch("mvmctl.services.console_relay.manager.subprocess.Popen")
    def test_thread_lock_returns_same_instance(self, mock_popen: MagicMock, tmp_path: Path):
        """Thread lock returns the same instance on multiple accesses."""
        mgr = ConsoleRelayManager()

        lock1 = mgr._thread_lock
        lock2 = mgr._thread_lock
        assert lock1 is lock2
