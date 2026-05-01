"""
Console relay manager for VM serial console sessions.

Manages the lifecycle of console relay processes, including starting,
stopping, and cleanup of relay instances.
"""

import logging
import os
import signal
import subprocess
import sys
import threading
from pathlib import Path

from mvmctl.services.console_relay._defaults import (
    CONST_CONSOLE_KILL_TIMEOUT_S,
    DEFAULT_CONSOLE_LOG_FILENAME,
    DEFAULT_CONSOLE_PID_FILENAME,
    DEFAULT_CONSOLE_SOCKET_FILENAME,
)
from mvmctl.services.console_relay.exceptions import (
    ConsoleRelayAlreadyRunningError,
    ConsoleRelayProcessError,
)

logger = logging.getLogger(__name__)


class ConsoleRelayManager:
    """
    Manager for a single console relay subprocess instance.

    Coordinates one console relay process, ensuring proper
    lifecycle management and cleanup.

    Attributes:
        _id: Unique identifier for this relay
        _path: Directory path for socket, PID, and log files
        _name: Human-readable name for logging
        _info: Relay info dict or None if not running
        _lock: Lock for thread-safe access

    """

    _pid: int | None = None

    def __init__(
        self,
        id: str,
        path: Path,
        name: str | None = None,
        pid_filename: str = DEFAULT_CONSOLE_PID_FILENAME,
        socket_filename: str = DEFAULT_CONSOLE_SOCKET_FILENAME,
        log_filename: str = DEFAULT_CONSOLE_LOG_FILENAME,
    ) -> None:
        """
        Initialize the relay manager for a specific resource.

        Args:
            id: Unique identifier for registry and file paths
            path: Directory path where socket, PID, and log files will be created
            name: Human-readable name for logging (uses id if None)
            pid_filename: Name of the PID file (default: console.pid)
            socket_filename: Name of the socket file (default: console.sock)
            log_filename: Name of the log file (default: firecracker.console.log)

        """
        self._id = id
        self._path = path
        self._name = name or id
        self._pid_path = path / pid_filename
        self._socket_path = path / socket_filename
        self._log_path = path / log_filename
        self._pid: int | None = None
        self._lock: threading.Lock | None = None

    @property
    def _thread_lock(self) -> threading.Lock:
        """Lazy initialization of threading lock."""
        if self._lock is None:
            self._lock = threading.Lock()
        return self._lock

    @property
    def id(self) -> str:
        """Return the relay's unique identifier."""
        return self._id

    @property
    def name(self) -> str:
        """Return the relay's human-readable name."""
        return self._name

    @property
    def pid(self) -> int | None:
        if self._pid is not None:
            return self._pid
        if self._pid_path.exists():
            try:
                return int(self._pid_path.read_text().strip())
            except (ValueError, OSError):
                pass
        return None

    @property
    def pid_path(self) -> Path:
        return self._pid_path

    @property
    def socket_path(self) -> Path:
        return self._socket_path

    @property
    def log_path(self) -> Path:
        return self._log_path

    def start(self, pty_controller_fd: int) -> tuple[Path, int]:
        """
        Start the console relay subprocess.

        Spawns a relay subprocess that reads from the PTY controller and
        writes to both the console.log file and a Unix socket.

        Args:
            pty_controller_fd: File descriptor of the PTY controller (primary)

        Returns:
            Tuple of (socket_path, pid)

        Raises:
            ConsoleRelayAlreadyRunningError: If relay is already running

        """
        with self._thread_lock:
            if self._pid is not None:
                raise ConsoleRelayAlreadyRunningError(
                    f"Console relay already running for ID: {self._id}"
                )

            relay_cmd = [
                sys.executable,
                "-m",
                "mvmctl.services.console_relay.process",
                "--id",
                self._id,
                "--name",
                self._name,
                "--pty-controller-fd",
                str(pty_controller_fd),
                "--socket-path",
                str(self._socket_path),
                "--pid-file",
                str(self._pid_path),
                "--log-file",
                str(self._log_path),
            ]

            try:
                proc = subprocess.Popen(
                    relay_cmd,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                    pass_fds=[pty_controller_fd],
                )
            except OSError as e:
                raise ConsoleRelayProcessError(
                    f"Failed to spawn console relay process: {e}"
                ) from e

            self._pid = proc.pid

            logger.info(
                "Started console relay for %s (ID: %s, PID: %d)",
                self._name,
                self._id,
                proc.pid,
            )

            return self._socket_path, self._pid

    def _send_signal(self, pid: int, sig: int) -> bool:
        try:
            os.kill(pid, sig)
            return True
        except ProcessLookupError:
            logger.debug(
                "Console relay process (PID: %d) already terminated", pid
            )
            return False
        except PermissionError:
            logger.warning(
                "Cannot signal console relay (PID: %d) - permission denied", pid
            )
            return False

    def _cleanup_files(self) -> None:
        if self._pid_path.exists():
            try:
                self._pid_path.unlink()
            except OSError:
                pass
        if self._socket_path.exists():
            try:
                self._socket_path.unlink()
            except OSError:
                pass

    def stop(self) -> bool:
        with self._thread_lock:
            if self.pid is None:
                return False

            self._send_signal(self.pid, signal.SIGTERM)
            self._cleanup_files()
            self._pid = None
            logger.info("Stopped console relay for %s", self._name)
            return True

    def terminate(self) -> bool:
        with self._thread_lock:
            if self.pid is None:
                return False

            if not self._send_signal(self.pid, signal.SIGTERM):
                self._cleanup_files()
                self._pid = None
                return True

            import time

            for _ in range(int(CONST_CONSOLE_KILL_TIMEOUT_S * 10)):
                time.sleep(0.1)
                if not self._send_signal(self.pid, 0):
                    break
            else:
                self._send_signal(self.pid, signal.SIGKILL)

            self._cleanup_files()
            self._pid = None
            logger.info("Terminated console relay for %s", self._name)
            return True

    def get_pid(self) -> int | None:
        """
        Get the PID of the running relay.

        Returns:
            The subprocess PID if running, None otherwise

        """
        with self._thread_lock:
            if self._pid is not None:
                try:
                    os.kill(self._pid, 0)
                    return self._pid
                except (ProcessLookupError, PermissionError):
                    return None
            if self._pid_path.exists():
                try:
                    pid = int(self._pid_path.read_text().strip())
                    os.kill(pid, 0)
                    return pid
                except (
                    ValueError,
                    OSError,
                    ProcessLookupError,
                    PermissionError,
                ):
                    pass
            return None

    def is_running(self) -> bool:
        """
        Check if the relay is currently running.

        Returns:
            True if relay is running, False otherwise

        """
        return self.get_pid() is not None

    # FIXME: this needs review because orphan cleanup requires looping through all vm dirs
    def cleanup_orphans(self) -> None:
        """
        Clean up any orphaned relays from previous crashed sessions.

        This method is called during initialization to ensure no stale
        relays remain. It scans for console.pid files where the
        associated processes are no longer running and cleans them up.
        """
        from mvmctl.utils.common import CacheUtils

        logger.debug("Running console relay orphan cleanup check")

        vms_dir = CacheUtils.get_vms_dir()
        if not vms_dir.exists():
            return

        for entry in vms_dir.iterdir():
            if not entry.is_dir():
                continue

            id = entry.name
            pid_file = entry / DEFAULT_CONSOLE_PID_FILENAME
            if not pid_file.exists():
                continue

            try:
                pid = int(pid_file.read_text().strip())
                os.kill(pid, 0)
                logger.debug(
                    "Skipping orphan cleanup for %s - process %d still running",
                    id,
                    pid,
                )
            except (
                ValueError,
                OSError,
                ProcessLookupError,
                PermissionError,
            ) as e:
                if isinstance(e, ProcessLookupError):
                    # Process terminated - clean up stale PID file and socket
                    try:
                        pid_file.unlink()
                        logger.info(
                            "Cleaned up stale PID file for %s (process terminated)",
                            id,
                        )
                    except OSError:
                        pass
                    socket_path = entry / DEFAULT_CONSOLE_SOCKET_FILENAME
                    if socket_path.exists():
                        try:
                            socket_path.unlink()
                        except OSError:
                            pass
                elif isinstance(e, PermissionError):
                    # Get pid from the file for logging
                    pid_str = "unknown"
                    try:
                        pid_str = pid_file.read_text().strip()
                    except OSError:
                        pass
                    logger.debug(
                        "Skipping orphan cleanup for %s - permission denied on process %s",
                        id,
                        pid_str,
                    )
                else:
                    # Invalid PID file
                    try:
                        pid_file.unlink()
                        logger.info("Cleaned up invalid PID file for %s", id)
                    except OSError:
                        pass
