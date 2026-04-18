"""Process signal handling and lifecycle management.

Provides robust Linux process lifecycle operations: zombie detection,
graceful shutdown, exit code capture, and PID reuse mitigation.
"""

from __future__ import annotations

import errno
import logging
import os
import signal
import time
from collections.abc import Callable

logger = logging.getLogger(__name__)

CONST_SIGNAL_EXIT_CODE_BASE: int = 128


class ProcessSignalHandler:
    """Robust Linux process lifecycle manager.

    Handles: zombie detection, graceful shutdown, exit code capture,
    PID reuse mitigation, D-state awareness.

    Args:
        pid: Process ID to manage.
        is_child: True if this process was spawned by us (can waitpid).
                  False for external/orphaned processes.
        expected_start_time: Process start time for PID reuse detection.
        graceful_timeout: Seconds to wait after SIGTERM before SIGKILL.
        kill_timeout: Seconds to wait after SIGKILL before giving up.
        poll_interval: Seconds between poll checks.
    """

    def __init__(
        self,
        pid: int,
        *,
        is_child: bool = True,
        expected_start_time: int | None = None,
        graceful_timeout: float = 30.0,
        kill_timeout: float = 5.0,
        poll_interval: float = 0.1,
    ) -> None:
        self.pid = pid
        self.is_child = is_child
        self.expected_start_time = expected_start_time
        self.graceful_timeout = graceful_timeout
        self.kill_timeout = kill_timeout
        self.poll_interval = poll_interval
        self._exit_code: int | None = None
        self._reaped = False

    @staticmethod
    # Migrated from module-level function (originally lines 21-31)
    def _decode_exit_status(status: int) -> int:
        """Decode os.waitpid() status into conventional exit code.

        Returns:
            Normal exit code (0-255) or 128+signal for signal death.
        """
        if os.WIFEXITED(status):
            return os.WEXITSTATUS(status)
        if os.WIFSIGNALED(status):
            return CONST_SIGNAL_EXIT_CODE_BASE + os.WTERMSIG(status)
        return -1

    @staticmethod
    # Migrated from module-level function (originally lines 34-52)
    def _get_process_start_time(pid: int) -> int | None:
        """Get process start time from /proc/<pid>/stat (field 22, clock ticks).

        Returns None if process doesn't exist or is unreadable.
        """
        try:
            with open(f"/proc/{pid}/stat") as f:
                content = f.read()
            # Find last ')' to handle comm names with spaces/parens
            fields = content[content.rfind(")") + 2 :].split()
            return int(fields[19])  # field 22 overall, index 19 after comm
        except (
            FileNotFoundError,
            PermissionError,
            ProcessLookupError,
            ValueError,
            IndexError,
        ):
            return None

    @staticmethod
    # Migrated from module-level function (originally lines 55-64)
    def _is_pid_reused(pid: int, expected_start_time: int) -> bool:
        """Check if PID has been reused by comparing start times.

        Returns True if the current process with this PID has a different
        start time than expected (meaning the original process is gone).
        """
        current_start_time = ProcessSignalHandler._get_process_start_time(pid)
        if current_start_time is None:
            return False  # Process doesn't exist, so no reuse concern
        return current_start_time != expected_start_time

    def is_alive(self) -> bool:
        """Check if process is genuinely running (not zombie, not dead, not reused).

        Returns False for: dead, zombie, already reaped, PID reused.
        Returns True for: running, sleeping, D-state.
        """
        if self._reaped:
            return False

        # Check PID reuse first
        if self.expected_start_time is not None:
            if self._is_pid_reused(self.pid, self.expected_start_time):
                return False

        # Check for zombie state via /proc
        if self._is_zombie():
            if self.is_child:
                self._try_reap()
            return False

        # os.kill(pid, 0) check
        try:
            os.kill(self.pid, 0)
            return True
        except OSError as e:
            if e.errno == errno.ESRCH:
                return False
            if e.errno == errno.EPERM:
                return True  # Exists but no permission to signal
            raise

    def kill(self) -> bool:
        """Send SIGKILL. Returns True if signal was sent."""
        return self.send_signal(signal.SIGKILL)

    def send_signal(self, sig: int) -> bool:
        """Send signal. Returns True if signal was delivered."""
        try:
            os.kill(self.pid, sig)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    def graceful_shutdown(
        self,
        *,
        pre_signal_hook: Callable[[], bool] | None = None,
    ) -> int | None:
        """Full graceful shutdown: optional hook -> SIGTERM -> wait -> SIGKILL -> wait.

        Args:
            pre_signal_hook: Called before SIGTERM. Return False to skip SIGTERM
                and only wait for exit (e.g., for Firecracker: call SendCtrlAltDel
                here, then return False to wait for guest OS shutdown).

        Returns:
            Exit code if captured, None if process survived SIGKILL or is not a child.
        """
        if not self.is_alive():
            return self._exit_code

        # Optional pre-signal hook (e.g., Firecracker SendCtrlAltDel)
        if pre_signal_hook is not None:
            if not pre_signal_hook():
                # Hook handled the shutdown, just wait for exit
                return self._wait_for_exit(self.graceful_timeout)

        # Phase 1: SIGTERM
        try:
            os.kill(self.pid, signal.SIGTERM)
        except OSError as e:
            if e.errno == errno.ESRCH:
                self._try_reap()
                return self._exit_code
            raise

        # Phase 2: Wait for graceful exit
        exit_code = self._wait_for_exit(self.graceful_timeout)
        if exit_code is not None:
            return exit_code

        # Phase 3: SIGKILL
        try:
            os.kill(self.pid, signal.SIGKILL)
        except OSError as e:
            if e.errno == errno.ESRCH:
                self._try_reap()
                return self._exit_code
            raise

        # Phase 4: Wait for SIGKILL (should be near-instant)
        return self._wait_for_exit(self.kill_timeout)

    def wait_and_capture_exit(self) -> int | None:
        """Reap child process and capture exit code. Safe to call multiple times."""
        if self._reaped:
            return self._exit_code
        if not self.is_child:
            return None
        try:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid != 0:
                self._exit_code = self._decode_exit_status(status)
                self._reaped = True
        except ChildProcessError:
            self._reaped = True
        return self._exit_code

    def _is_zombie(self) -> bool:
        """Check /proc/<pid>/stat for Z state. Handles comm names with parens."""
        try:
            with open(f"/proc/{self.pid}/stat") as f:
                content = f.read()
            state_idx = content.rfind(")") + 2
            return content[state_idx] == "Z"
        except (
            FileNotFoundError,
            PermissionError,
            ProcessLookupError,
            IndexError,
        ):
            return False

    def _try_reap(self) -> None:
        """Attempt to reap a zombie child. Safe to call multiple times."""
        if not self.is_child or self._reaped:
            return
        try:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid != 0:
                self._exit_code = self._decode_exit_status(status)
                self._reaped = True
        except ChildProcessError:
            self._reaped = True

    def _wait_for_exit(self, timeout: float) -> int | None:
        """Poll for process exit with monotonic deadline."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.is_child:
                try:
                    pid, status = os.waitpid(self.pid, os.WNOHANG)
                    if pid != 0:
                        self._exit_code = self._decode_exit_status(status)
                        self._reaped = True
                        return self._exit_code
                except ChildProcessError:
                    self._reaped = True
                    return self._exit_code
            else:
                if not self.is_alive():
                    self._reaped = True
                    return self._exit_code
            time.sleep(self.poll_interval)
        return None
