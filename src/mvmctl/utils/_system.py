"""System-level utilities: subprocess, signal handling, and process lifecycle."""

from __future__ import annotations

import errno
import logging
import os
import signal
import subprocess
import time
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from typing import Any

from mvmctl.constants import MVM_UNIX_GROUP
from mvmctl.exceptions import ProcessError

logger = logging.getLogger(__name__)

_STDERR_PREVIEW_LIMIT = 100
_MVM_GROUP_VERIFIED: bool = False

__all__ = [
    "run_cmd",
    "stream_cmd",
    "require_mvm_group_membership",
    "is_process_running",
    "has_python_ancestor",
    "SigtermContext",
    "sigterm_context",
    "ProcessSignalHandler",
]


# ==================== Signal handling ====================


class SigtermContext:
    """
    Context manager for SIGTERM signal handling.

    Sets up a signal handler on entry, restores original handler on exit.
    The signal handler calls the provided cleanup function.
    """

    def __init__(self, cleanup_fn: Callable[[], None]) -> None:
        self._cleanup_fn = cleanup_fn
        self._old_handler: Any = None

    def _handle_signal(self, signum: int, frame: Any) -> None:
        self._cleanup_fn()

    def __enter__(self) -> SigtermContext:
        self._old_handler = signal.signal(signal.SIGTERM, self._handle_signal)
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        if self._old_handler is not None:
            signal.signal(signal.SIGTERM, self._old_handler)
        return None


@contextmanager
def sigterm_context(cleanup_fn: Callable[[], None]) -> Any:
    """
    Create a SigtermContext as a context manager.

    Usage:
        with sigterm_context(my_cleanup):
            # do work
    """
    ctx = SigtermContext(cleanup_fn)
    ctx.__enter__()
    try:
        yield ctx
    finally:
        ctx.__exit__(None, None, None)


# ==================== Process lifecycle ====================


class ProcessSignalHandler:
    """
    Robust Linux process lifecycle manager.

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
    def _decode_exit_status(status: int) -> int:
        """
        Decode os.waitpid() status into conventional exit code.

        Returns:
            Normal exit code (0-255) or 128+signal for signal death.

        """
        if os.WIFEXITED(status):
            return os.WEXITSTATUS(status)
        if os.WIFSIGNALED(status):
            return CONST_SIGNAL_EXIT_CODE_BASE + os.WTERMSIG(status)
        return -1

    @staticmethod
    def _get_process_start_time(pid: int) -> int | None:
        """
        Get process start time from /proc/<pid>/stat (field 22, clock ticks).

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
    def _is_pid_reused(pid: int, expected_start_time: int) -> bool:
        """
        Check if PID has been reused by comparing start times.

        Returns True if the current process with this PID has a different
        start time than expected (meaning the original process is gone).
        """
        current_start_time = ProcessSignalHandler._get_process_start_time(pid)
        if current_start_time is None:
            return False  # Process doesn't exist, so no reuse concern
        return current_start_time != expected_start_time

    def is_alive(self) -> bool:
        """
        Check if process is genuinely running (not zombie, not dead, not reused).

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

    def kill_and_wait(self, kill_timeout: float = 2.0) -> bool:
        """Send SIGKILL and poll until the process is dead.

        Args:
            kill_timeout: Seconds to wait for the process to die.

        Returns:
            True if the process was confirmed dead within timeout.
            False if the process still exists after timeout.

        """
        # Already dead — nothing to do
        if not self.is_alive():
            return True

        self.send_signal(signal.SIGKILL)

        _waited = 0.0
        while _waited < kill_timeout:
            if not self.is_alive():
                return True
            time.sleep(0.1)
            _waited += 0.1

        return False

    @classmethod
    def terminate_batch(
        cls,
        pids: list[int],
        *,
        graceful_timeout: float = 0.5,
    ) -> list[int]:
        """Batch-terminate orphaned PIDs: SIGTERM all → wait → SIGKILL survivors.

        Designed for abandoned/orphaned processes where we cannot waitpid
        (is_child=False). Each PID gets a lightweight ProcessSignalHandler
        so reuse detection can be layered on.

        Args:
            pids: List of PIDs to terminate.
            graceful_timeout: Seconds to wait after SIGTERM before SIGKILL.

        Returns:
            List of PIDs that were confirmed dead (either from SIGTERM or SIGKILL).
        """
        terminated: list[int] = []

        # Phase 1: SIGTERM all
        for pid in pids:
            handler = cls(pid, is_child=False)
            if handler.send_signal(signal.SIGTERM):
                terminated.append(pid)

        # Phase 2: Wait, then SIGKILL survivors
        if terminated:
            time.sleep(graceful_timeout)
            for pid in terminated:
                handler = cls(pid, is_child=False)
                if handler.is_alive():
                    if handler.kill():
                        logger.debug(
                            "Sent SIGKILL to abandoned process %d", pid
                        )
                    else:
                        # Process already exited since our check
                        pass

        return terminated

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
        """
        Full graceful shutdown: optional hook -> SIGTERM -> wait -> SIGKILL -> wait.

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
            if e.errno in (errno.ESRCH, errno.EPERM):
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
            if e.errno in (errno.ESRCH, errno.EPERM):
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


# ==================== Subprocess utilities ====================

CONST_SIGNAL_EXIT_CODE_BASE: int = 128


def _sanitize_stderr(stderr: str | None) -> str:
    cleaned = (stderr or "").strip()
    if len(cleaned) > _STDERR_PREVIEW_LIMIT:
        return f"{cleaned[:_STDERR_PREVIEW_LIMIT]}..."
    return cleaned


def run_cmd(
    args: list[str],
    *,
    check: bool = True,
    capture: bool = True,
    cwd: str | None = None,
    timeout: float | None = None,
    input: str | None = None,
    env: dict[str, str] | None = None,
    privileged: bool = False,
    text: bool = True,
) -> subprocess.CompletedProcess[str]:
    """
    Run a subprocess command and return the completed-process result.

    Args:
        args: Command and arguments to execute.
        check: Raise ``ProcessError`` on non-zero exit code when ``True``.
        capture: Capture stdout/stderr when ``True``; inherit from parent otherwise.
        cwd: Working directory for the subprocess, or ``None`` for the current directory.
        timeout: Optional timeout in seconds. Raises ``ProcessError`` if exceeded.
        input: Optional string to pass to the subprocess's stdin.
        env: Optional environment variables for the subprocess.
        privileged: When ``True``, run the command with ``sudo`` (prepend
            ``sudo`` to the command if not running as root).
        text: When ``True`` (default), return stdout/stderr as strings;
            when ``False``, return as bytes.

    Returns:
        The ``subprocess.CompletedProcess`` result.

    Raises:
        ProcessError: If the command is not found, exits with a non-zero code,
            or exceeds the optional timeout.

    """
    if privileged:
        if os.getuid() != 0:
            require_mvm_group_membership()
            args = ["sudo", *args]
    try:
        result = subprocess.run(
            args,
            capture_output=capture,
            text=text,
            check=check,
            cwd=cwd,
            timeout=timeout,
            input=input,
            env=env,
        )
    except FileNotFoundError as e:
        raise ProcessError(f"Command not found: {args[0]}") from e
    except subprocess.TimeoutExpired as e:
        raise ProcessError(
            f"Command timed out after {timeout}s: {args[0]}"
        ) from e
    except subprocess.CalledProcessError as e:
        stderr = (e.stderr or "").strip()
        sanitized_stderr = _sanitize_stderr(stderr)
        raise ProcessError(
            f"Command failed (exit {e.returncode}): {args[0]}"
            + (f"\n{sanitized_stderr}" if sanitized_stderr else "")
        ) from e
    return result


def stream_cmd(
    args: list[str],
    *,
    cwd: str | None = None,
) -> Iterator[str]:
    """
    Stream stdout lines from a subprocess command as they are produced.

    Args:
        args: Command and arguments to execute.
        cwd: Working directory for the subprocess, or ``None`` for the current directory.

    Yields:
        Each output line with the trailing newline stripped.

    Raises:
        ProcessError: If the command is not found or exits with a non-zero code.

    """
    try:
        proc = subprocess.Popen(
            args,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            cwd=cwd,
        )
    except FileNotFoundError as e:
        raise ProcessError(f"Command not found: {args[0]}") from e

    if proc.stdout is None:
        raise ProcessError("stdout is None — stdout=PIPE was not set")
    try:
        for line in proc.stdout:
            yield line.rstrip("\n")
    finally:
        proc.stdout.close()
        returncode = proc.wait()
        if returncode != 0:
            # Sanitize error message: only show command name
            raise ProcessError(f"Command failed (exit {returncode}): {args[0]}")


def require_mvm_group_membership() -> None:
    """Warn if user is not in the mvm group, but do NOT block execution.

    Prints advisory warnings for each missing precondition (group doesn't
    exist, user not a member, session doesn't have the group active), then
    lets ``sudo`` handle authentication with its normal password prompt.

    Results are cached per-process via a module-level flag since group
    membership is immutable within a process lifetime (os.getgroups()
    returns what was set at login/newgrp time).
    """
    global _MVM_GROUP_VERIFIED
    if _MVM_GROUP_VERIFIED:
        return

    import grp
    import pwd

    try:
        g = grp.getgrnam(MVM_UNIX_GROUP)
    except KeyError:
        logger.warning(
            "Group '%s' does not exist. Run 'sudo mvm host init' to set up "
            "privilege management and avoid password prompts.",
            MVM_UNIX_GROUP,
        )
        _MVM_GROUP_VERIFIED = True
        return

    user_pw = pwd.getpwuid(os.getuid())
    username = user_pw.pw_name

    is_supplementary_member = username in g.gr_mem
    is_primary_group = user_pw.pw_gid == g.gr_gid
    if not (is_supplementary_member or is_primary_group):
        logger.warning(
            "User '%s' is not in the '%s' group. "
            "Run 'sudo mvm host init' to configure privileges, "
            "then 'newgrp %s' or log out and back in.",
            username,
            MVM_UNIX_GROUP,
            MVM_UNIX_GROUP,
        )

    process_gids = set(os.getgroups()) | {os.getgid(), os.getegid()}
    if g.gr_gid not in process_gids:
        logger.warning(
            "Your user is in the '%s' group, but your current session "
            "does not have the group active yet. "
            "Please log out and log back in, or run: newgrp %s",
            MVM_UNIX_GROUP,
            MVM_UNIX_GROUP,
        )

    _MVM_GROUP_VERIFIED = True


def is_process_running(pid: int | None) -> bool:
    """
    Check if a process is still running by PID.

    Args:
        pid: Process ID to check

    Returns:
        True if process is running, False if not running or PID is None

    """
    if pid is None:
        return False
    try:
        os.kill(pid, 0)
        return True
    except (ProcessLookupError, OSError):
        return False


def has_python_ancestor(pid: int) -> bool:
    """Walk the PPID chain for *pid* upward through /proc.

    Returns True if any ancestor process has ``"python"`` or ``"mvm"`` in
    its command line (case-insensitive), indicating the process tree is
    managed by a Python / mvmctl process.  Returns False if the parent
    chain reaches PID 1 without finding a Python ancestor.
    """
    visited: set[int] = set()
    current = pid

    while current > 1 and current not in visited:
        visited.add(current)
        try:
            # Read /proc/<pid>/cmdline
            cmdline_path = f"/proc/{current}/cmdline"
            with open(cmdline_path, "rb") as f:
                raw = f.read()
            # cmdline uses null bytes as separators; decode with 'replace'
            cmdline = raw.decode("utf-8", errors="replace").lower()
            if "python" in cmdline or "mvm" in cmdline:
                return True

            # Read PPid from /proc/<pid>/status
            status_path = f"/proc/{current}/status"
            with open(status_path) as f:
                for line in f:
                    if line.startswith("PPid:"):
                        current = int(line.split(":")[1].strip())
                        break
                else:
                    break  # PPid not found — stop walking
        except (FileNotFoundError, PermissionError, OSError, ValueError):
            break

    return False
